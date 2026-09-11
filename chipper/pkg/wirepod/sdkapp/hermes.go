package sdkapp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
)

// HermesCommand is the deliberately small motion and expression vocabulary
// made available to an external agent. Values are validated here as a second
// boundary, even though the bridge validates its JSON request as well.
type HermesCommand struct {
	Action         string `json:"action"`
	Text           string `json:"text,omitempty"`
	Expression     string `json:"expression,omitempty"`
	LeftWheelMMPS  int    `json:"left_wheel_mmps,omitempty"`
	RightWheelMMPS int    `json:"right_wheel_mmps,omitempty"`
	SpeedRadPerSec int    `json:"speed_rad_per_sec,omitempty"`
	DurationMS     int    `json:"duration_ms,omitempty"`
}

type HermesCommandResult struct {
	Action        string `json:"action"`
	Expression    string `json:"expression,omitempty"`
	StopScheduled bool   `json:"stop_scheduled"`
}

type HermesObservation struct {
	BatteryLevel        string          `json:"battery_level"`
	BatteryVolts        float32         `json:"battery_volts"`
	IsCharging          bool            `json:"is_charging"`
	IsOnChargerPlatform bool            `json:"is_on_charger_platform"`
	Faces               json.RawMessage `json:"faces"`
}

var hermesControl = struct {
	sync.Mutex
}{}

const (
	hermesObserveTimeout    = 5 * time.Second
	hermesSnapshotTimeout   = 10 * time.Second
	hermesSpeechTimeout     = 20 * time.Second
	hermesExpressionTimeout = 12 * time.Second
	// Bound only acquisition of behavior control. Once Vector is visibly
	// processing, the caller's conversation context owns its lifetime.
	hermesProcessingAcquireTimeout = 4 * time.Second
	hermesMotionTimeout            = 8 * time.Second
	hermesUndockTimeout            = 30 * time.Second
	// A scan is a short, in-place head sweep. LookAroundInPlace is an
	// unbounded native behavior on Vector 1.0 and cannot provide a truthful
	// command completion result to an external agent.
	hermesScanTimeout   = 6 * time.Second
	maxHermesWheelMMPS  = 200
	maxHermesMotionMS   = 2000
	maxHermesJointRadPS = 2
)

// hermesExpressions is intentionally a small semantic vocabulary rather than
// a pass-through of firmware animation names. This lets an external agent
// express itself without gaining arbitrary SDK/firmware control.
var hermesExpressions = map[string]string{
	"affectionate": "anim_feedback_iloveyou_02",
	"celebrate":    "anim_pounce_success_03",
	"confused":     "anim_meetvictor_lookface_timeout_01",
	"curious":      "anim_observing_self_absorbed_01",
	"excited":      "anim_blackjack_victorwin_01",
	"happy":        "anim_onboarding_reacttoface_happy_01",
	"sad":          "anim_feedback_meanwords_01",
	"thinking":     "anim_explorer_scan_short_04",
}

const (
	hermesProcessingAnimation = "anim_knowledgegraph_searching_01"
	// Play one long native request instead of replaying a short clip from the
	// start. Twenty-four loops cover the 45-second Hermes voice deadline on the
	// shipped animation while still yielding immediately on context cancellation.
	hermesProcessingLoops uint32 = 24
)

// HermesProcessingIndicator owns the short-lived native activity cue shown
// while WirePod waits for a Hermes voice response. It is deliberately not a
// Hermes tool: the agent must not spend a tool call or choose an animation just
// to acknowledge that its own request is still in flight.
//
// Stop is idempotent. It cancels the active SDK operation before the next
// spoken chunk takes the shared behavior-control lock, preventing the cue from
// delaying Vector's first answer.
type HermesProcessingIndicator struct {
	serial     string
	cancel     context.CancelFunc
	done       chan struct{}
	dispatched chan struct{}
	stop       sync.Once
	dispatch   sync.Once
}

var hermesProcessingIndicators = struct {
	sync.Mutex
	active map[string]*HermesProcessingIndicator
}{active: make(map[string]*HermesProcessingIndicator)}

// StartHermesProcessing starts a best-effort, native searching animation while
// one Vector waits on its configured Hermes profile. It neither speaks nor
// moves the wheels. At most one indicator may be active for an ESN; a newer
// voice turn cancels the older one.
func StartHermesProcessing(serial string) *HermesProcessingIndicator {
	serial = strings.ToLower(strings.TrimSpace(serial))
	ctx, cancel := context.WithCancel(context.Background())
	indicator := &HermesProcessingIndicator{
		serial:     serial,
		cancel:     cancel,
		done:       make(chan struct{}),
		dispatched: make(chan struct{}),
	}

	hermesProcessingIndicators.Lock()
	if prior := hermesProcessingIndicators.active[serial]; prior != nil {
		prior.cancel()
	}
	hermesProcessingIndicators.active[serial] = indicator
	hermesProcessingIndicators.Unlock()

	go indicator.run(ctx)
	// Do not race a fast Hermes response against a goroutine which has not yet
	// submitted the visible cue. A bounded wait keeps voice latency intact if
	// Vector is unavailable or another behavior currently owns the robot.
	select {
	case <-indicator.dispatched:
	case <-indicator.done:
	case <-time.After(750 * time.Millisecond):
	}
	return indicator
}

func (indicator *HermesProcessingIndicator) markDispatched() {
	indicator.dispatch.Do(func() {
		close(indicator.dispatched)
		log.Printf("Hermes processing cue dispatched for %s", indicator.serial)
	})
}

func (indicator *HermesProcessingIndicator) run(ctx context.Context) {
	defer close(indicator.done)
	defer func() {
		hermesProcessingIndicators.Lock()
		if hermesProcessingIndicators.active[indicator.serial] == indicator {
			delete(hermesProcessingIndicators.active, indicator.serial)
		}
		hermesProcessingIndicators.Unlock()
	}()

	robot, _, err := getRobot(indicator.serial)
	if err != nil {
		log.Printf("Hermes processing indicator unavailable for %s: %v", indicator.serial, err)
		return
	}
	err = runHermesProcessingAnimation(ctx, robot, indicator.markDispatched)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		log.Printf("Hermes processing indicator failed for %s: %v", indicator.serial, err)
	}
}

func runHermesProcessingAnimation(ctx context.Context, robot Robot, dispatched func()) error {
	// Do not leave a hung acquisition holding the shared SDK lock. Stopping the
	// timer after control is granted preserves the outer voice-turn context for
	// the entire continuous animation.
	controlCtx, cancelControl := context.WithCancel(ctx)
	defer cancelControl()
	timeout := time.AfterFunc(hermesProcessingAcquireTimeout, cancelControl)
	defer timeout.Stop()

	hermesControl.Lock()
	defer hermesControl.Unlock()
	return withHermesBehaviorControlPriority(controlCtx, robot, vectorpb.ControlRequest_OVERRIDE_BEHAVIORS, func(actionCtx context.Context) error {
		if !timeout.Stop() && actionCtx.Err() != nil {
			return context.DeadlineExceeded
		}
		dispatched()
		_, err := robot.Vector.Conn.PlayAnimation(actionCtx, &vectorpb.PlayAnimationRequest{
			Animation: &vectorpb.Animation{Name: hermesProcessingAnimation},
			Loops:     hermesProcessingLoops,
		})
		return err
	})
}

// Stop ends this turn's activity cue. Do not make a healthy spoken reply wait
// forever for a disconnected robot; cancellation normally closes done at once,
// while the bounded fallback leaves the caller free to continue its turn.
func (indicator *HermesProcessingIndicator) Stop() {
	if indicator == nil {
		return
	}
	indicator.stop.Do(func() {
		indicator.cancel()
		log.Printf("Hermes processing cue stop requested for %s", indicator.serial)
		select {
		case <-indicator.done:
		case <-time.After(500 * time.Millisecond):
		}
	})
}

// HermesSnapshot is a fresh camera image captured solely for the owning Hermes
// profile. It is intentionally an in-memory value: the bridge streams it with
// no-store semantics and WirePod never writes a face image to disk.
type HermesSnapshot struct {
	JPEG []byte
}

// HermesObserve returns a bounded live observation from the robot assigned to
// one Hermes profile. It intentionally returns enrolled faces (a roster), not
// a claim that a face was just recognised: recognition events need their own
// subscribed event-stream contract.
func HermesObserve(serial string) (HermesObservation, error) {
	hermesControl.Lock()
	defer hermesControl.Unlock()

	robot, _, err := getRobot(serial)
	if err != nil {
		return HermesObservation{}, err
	}
	ctx, cancel := context.WithTimeout(robot.Ctx, hermesObserveTimeout)
	defer cancel()
	battery, err := robot.Vector.Conn.BatteryState(ctx, &vectorpb.BatteryStateRequest{})
	if err != nil {
		return HermesObservation{}, err
	}
	faces, err := robot.Vector.Conn.RequestEnrolledNames(ctx, &vectorpb.RequestEnrolledNamesRequest{})
	if err != nil {
		return HermesObservation{}, err
	}
	faceJSON, err := json.Marshal(faces.Faces)
	if err != nil {
		return HermesObservation{}, err
	}
	return HermesObservation{
		BatteryLevel:        battery.GetBatteryLevel().String(),
		BatteryVolts:        battery.GetBatteryVolts(),
		IsCharging:          battery.GetIsCharging(),
		IsOnChargerPlatform: battery.GetIsOnChargerPlatform(),
		Faces:               faceJSON,
	}, nil
}

// HermesCaptureSnapshot captures one fresh JPEG from the native camera feed
// without moving or speaking through the robot. It shares the control mutex
// with behaviour calls because the Vector SDK permits only one active client
// operation at a time.
func HermesCaptureSnapshot(serial string) (HermesSnapshot, error) {
	hermesControl.Lock()
	defer hermesControl.Unlock()

	robot, _, err := getRobot(serial)
	if err != nil {
		return HermesSnapshot{}, err
	}
	ctx, cancel := context.WithTimeout(robot.Ctx, hermesSnapshotTimeout)
	defer cancel()
	var image []byte
	err = withHermesBehaviorControl(ctx, robot, func(actionCtx context.Context) error {
		// CaptureSingleImage is Vector's native one-shot primitive. It enables
		// and disables its own default-resolution feed, avoiding a stranded
		// long-lived CameraFeed stream between Hermes requests.
		response, captureErr := robot.Vector.Conn.CaptureSingleImage(actionCtx, &vectorpb.CaptureSingleImageRequest{})
		if captureErr != nil {
			return captureErr
		}
		image = append([]byte(nil), response.GetData()...)
		return nil
	})
	if err != nil {
		return HermesSnapshot{}, err
	}
	if len(image) < 4 || len(image) > 8*1024*1024 || image[0] != 0xff || image[1] != 0xd8 || image[2] != 0xff {
		return HermesSnapshot{}, fmt.Errorf("Vector returned an invalid camera image")
	}
	return HermesSnapshot{JPEG: append([]byte(nil), image...)}, nil
}

// HermesControl performs one bounded robot action. Drive, head, and lift
// commands always receive a matching stop after at most two seconds. Undock is
// deliberately available only to a full Vector that is physically on its
// charger. Scan runs only native, in-place behaviours; it never drives.
func HermesControl(serial string, command HermesCommand) (HermesCommandResult, error) {
	hermesControl.Lock()
	defer hermesControl.Unlock()

	robot, _, err := getRobot(serial)
	if err != nil {
		return HermesCommandResult{}, err
	}
	action := strings.ToLower(strings.TrimSpace(command.Action))
	if err := ValidateHermesCommand(command); err != nil {
		return HermesCommandResult{}, err
	}
	timeout := hermesMotionTimeout
	switch action {
	case "say":
		timeout = hermesSpeechTimeout
	case "express":
		timeout = hermesExpressionTimeout
	case "undock":
		timeout = hermesUndockTimeout
	case "scan":
		timeout = hermesScanTimeout
	}
	ctx, cancel := context.WithTimeout(robot.Ctx, timeout)
	defer cancel()
	switch action {
	case "say":
		text := strings.TrimSpace(command.Text)
		if text == "" || len([]rune(text)) > 280 {
			return HermesCommandResult{}, fmt.Errorf("speech must contain 1 through 280 characters")
		}
		err = withHermesBehaviorControl(ctx, robot, func(actionCtx context.Context) error {
			_, actionErr := robot.Vector.Conn.SayText(actionCtx, &vectorpb.SayTextRequest{DurationScalar: 1, UseVectorVoice: true, Text: text})
			return actionErr
		})
		return HermesCommandResult{Action: action}, err
	case "express":
		expression := strings.ToLower(strings.TrimSpace(command.Expression))
		animation, ok := hermesExpressions[expression]
		if !ok {
			return HermesCommandResult{}, fmt.Errorf("unsupported Hermes expression")
		}
		err = withHermesBehaviorControl(ctx, robot, func(actionCtx context.Context) error {
			_, actionErr := robot.Vector.Conn.PlayAnimation(actionCtx, &vectorpb.PlayAnimationRequest{
				Animation: &vectorpb.Animation{Name: animation},
				Loops:     1,
			})
			return actionErr
		})
		return HermesCommandResult{Action: action, Expression: expression}, err
	case "drive":
		if !validMotion(command.LeftWheelMMPS, command.RightWheelMMPS, command.DurationMS) {
			return HermesCommandResult{}, fmt.Errorf("drive values exceed the bounded control range")
		}
		err = withHermesBehaviorControl(ctx, robot, func(actionCtx context.Context) error {
			if actionErr := driveWheels(actionCtx, robot, command.LeftWheelMMPS, command.RightWheelMMPS); actionErr != nil {
				return actionErr
			}
			return waitThenStop(actionCtx, command.DurationMS, func(stopCtx context.Context) error { return driveWheels(stopCtx, robot, 0, 0) })
		})
		return HermesCommandResult{Action: action, StopScheduled: err == nil}, err
	case "head":
		if !validJointMotion(command.SpeedRadPerSec, command.DurationMS) {
			return HermesCommandResult{}, fmt.Errorf("head values exceed the bounded control range")
		}
		err = withHermesBehaviorControl(ctx, robot, func(actionCtx context.Context) error {
			if _, actionErr := robot.Vector.Conn.MoveHead(actionCtx, &vectorpb.MoveHeadRequest{SpeedRadPerSec: float32(command.SpeedRadPerSec)}); actionErr != nil {
				return actionErr
			}
			return waitThenStop(actionCtx, command.DurationMS, func(stopCtx context.Context) error {
				_, stopErr := robot.Vector.Conn.MoveHead(stopCtx, &vectorpb.MoveHeadRequest{})
				return stopErr
			})
		})
		return HermesCommandResult{Action: action, StopScheduled: err == nil}, err
	case "lift":
		if !validJointMotion(command.SpeedRadPerSec, command.DurationMS) {
			return HermesCommandResult{}, fmt.Errorf("lift values exceed the bounded control range")
		}
		err = withHermesBehaviorControl(ctx, robot, func(actionCtx context.Context) error {
			if _, actionErr := robot.Vector.Conn.MoveLift(actionCtx, &vectorpb.MoveLiftRequest{SpeedRadPerSec: float32(command.SpeedRadPerSec)}); actionErr != nil {
				return actionErr
			}
			return waitThenStop(actionCtx, command.DurationMS, func(stopCtx context.Context) error {
				_, stopErr := robot.Vector.Conn.MoveLift(stopCtx, &vectorpb.MoveLiftRequest{})
				return stopErr
			})
		})
		return HermesCommandResult{Action: action, StopScheduled: err == nil}, err
	case "stop":
		err = withHermesBehaviorControl(ctx, robot, func(actionCtx context.Context) error {
			if actionErr := driveWheels(actionCtx, robot, 0, 0); actionErr != nil {
				return actionErr
			}
			if _, actionErr := robot.Vector.Conn.MoveHead(actionCtx, &vectorpb.MoveHeadRequest{}); actionErr != nil {
				return actionErr
			}
			_, actionErr := robot.Vector.Conn.MoveLift(actionCtx, &vectorpb.MoveLiftRequest{})
			return actionErr
		})
		return HermesCommandResult{Action: action}, err
	case "undock":
		err = withHermesBehaviorControl(ctx, robot, func(actionCtx context.Context) error {
			battery, batteryErr := robot.Vector.Conn.BatteryState(actionCtx, &vectorpb.BatteryStateRequest{})
			if batteryErr != nil {
				return batteryErr
			}
			if !eligibleForHermesUndock(battery) {
				return fmt.Errorf("undock requires a full Vector that is charging on its charger")
			}
			_, actionErr := robot.Vector.Conn.DriveOffCharger(actionCtx, &vectorpb.DriveOffChargerRequest{})
			return actionErr
		})
		return HermesCommandResult{Action: action}, err
	case "scan":
		err = withHermesBehaviorControl(ctx, robot, func(actionCtx context.Context) error {
			// Face detection is handled continuously by the Hermes event stream.
			// Sweep the head rather than invoking Vector's unbounded look-around
			// behavior, which cannot return before the bridge deadline.
			for _, speed := range []float32{1, -1} {
				if _, actionErr := robot.Vector.Conn.MoveHead(actionCtx, &vectorpb.MoveHeadRequest{SpeedRadPerSec: speed}); actionErr != nil {
					return actionErr
				}
				if actionErr := waitThenStop(actionCtx, 700, func(stopCtx context.Context) error {
					_, stopErr := robot.Vector.Conn.MoveHead(stopCtx, &vectorpb.MoveHeadRequest{})
					return stopErr
				}); actionErr != nil {
					return actionErr
				}
			}
			return nil
		})
		return HermesCommandResult{Action: action}, err
	default:
		return HermesCommandResult{}, fmt.Errorf("unsupported Hermes action")
	}
}

// ValidateHermesCommand validates the public control contract before any SDK
// request is made. It is exported so HTTP callers can reject malformed
// commands without presenting a control failure as a robot availability issue.
func ValidateHermesCommand(command HermesCommand) error {
	switch strings.ToLower(strings.TrimSpace(command.Action)) {
	case "say":
		text := strings.TrimSpace(command.Text)
		if text == "" || len([]rune(text)) > 280 {
			return fmt.Errorf("speech must contain 1 through 280 characters")
		}
	case "drive":
		if !validMotion(command.LeftWheelMMPS, command.RightWheelMMPS, command.DurationMS) {
			return fmt.Errorf("drive values exceed the bounded control range")
		}
	case "head", "lift":
		if !validJointMotion(command.SpeedRadPerSec, command.DurationMS) {
			return fmt.Errorf("joint values exceed the bounded control range")
		}
	case "express":
		expression := strings.ToLower(strings.TrimSpace(command.Expression))
		if _, ok := hermesExpressions[expression]; !ok {
			return fmt.Errorf("unsupported Hermes expression")
		}
	case "stop", "undock", "scan":
	default:
		return fmt.Errorf("unsupported Hermes action")
	}
	return nil
}

// eligibleForHermesUndock intentionally has no voltage threshold or partial
// charge exception. A full Vector can report is_charging=false once its
// charger enters maintenance mode, so autonomous charger departure requires
// the two reliable terminal states at the instant before the action.
func eligibleForHermesUndock(battery *vectorpb.BatteryStateResponse) bool {
	return battery != nil &&
		battery.GetBatteryLevel() == vectorpb.BatteryLevel_BATTERY_LEVEL_FULL &&
		battery.GetIsOnChargerPlatform()
}

func validMotion(first, second, durationMS int) bool {
	return first >= -maxHermesWheelMMPS && first <= maxHermesWheelMMPS &&
		second >= -maxHermesWheelMMPS && second <= maxHermesWheelMMPS &&
		durationMS >= 50 && durationMS <= maxHermesMotionMS
}

func validJointMotion(speed, durationMS int) bool {
	return speed >= -maxHermesJointRadPS && speed <= maxHermesJointRadPS &&
		durationMS >= 50 && durationMS <= maxHermesMotionMS
}

func driveWheels(ctx context.Context, robot Robot, left, right int) error {
	_, err := robot.Vector.Conn.DriveWheels(ctx, &vectorpb.DriveWheelsRequest{
		LeftWheelMmps: float32(left), RightWheelMmps: float32(right),
		LeftWheelMmps2: float32(left), RightWheelMmps2: float32(right),
	})
	return err
}

func withHermesBehaviorControl(ctx context.Context, robot Robot, action func(context.Context) error) (result error) {
	return withHermesBehaviorControlPriority(ctx, robot, vectorpb.ControlRequest_DEFAULT, action)
}

func withHermesBehaviorControlPriority(ctx context.Context, robot Robot, priority vectorpb.ControlRequest_Priority, action func(context.Context) error) (result error) {
	stream, err := robot.Vector.Conn.BehaviorControl(ctx)
	if err != nil {
		return err
	}
	if err = stream.Send(&vectorpb.BehaviorControlRequest{RequestType: &vectorpb.BehaviorControlRequest_ControlRequest{ControlRequest: &vectorpb.ControlRequest{Priority: priority}}}); err != nil {
		return err
	}
	for {
		response, receiveErr := stream.Recv()
		if receiveErr != nil {
			return receiveErr
		}
		if response.GetControlGrantedResponse() != nil {
			break
		}
	}
	defer func() {
		releaseErr := stream.Send(&vectorpb.BehaviorControlRequest{RequestType: &vectorpb.BehaviorControlRequest_ControlRelease{ControlRelease: &vectorpb.ControlRelease{}}})
		if result == nil && releaseErr != nil {
			result = releaseErr
		}
	}()
	return action(ctx)
}

func waitThenStop(ctx context.Context, durationMS int, stop func(context.Context) error) error {
	timer := time.NewTimer(time.Duration(durationMS) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return stop(ctx)
	}
}
