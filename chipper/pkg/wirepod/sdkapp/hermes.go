package sdkapp

import (
	"context"
	"encoding/json"
	"fmt"
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
	LeftWheelMMPS  int    `json:"left_wheel_mmps,omitempty"`
	RightWheelMMPS int    `json:"right_wheel_mmps,omitempty"`
	SpeedRadPerSec int    `json:"speed_rad_per_sec,omitempty"`
	DurationMS     int    `json:"duration_ms,omitempty"`
}

type HermesCommandResult struct {
	Action        string `json:"action"`
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
	generation map[string]map[string]uint64
}{generation: make(map[string]map[string]uint64)}

const (
	hermesCommandTimeout = 5 * time.Second
	maxHermesWheelMMPS   = 200
	maxHermesMotionMS    = 2000
	maxHermesJointRadPS  = 2
)

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
	ctx, cancel := context.WithTimeout(robot.Ctx, hermesCommandTimeout)
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

// HermesControl performs one bounded robot action. Drive, head, and lift
// commands always receive a matching stop after at most two seconds. A newer
// movement invalidates older scheduled stops so an old timer cannot cancel a
// subsequent motion command.
func HermesControl(serial string, command HermesCommand) (HermesCommandResult, error) {
	hermesControl.Lock()
	defer hermesControl.Unlock()

	robot, _, err := getRobot(serial)
	if err != nil {
		return HermesCommandResult{}, err
	}
	ctx, cancel := context.WithTimeout(robot.Ctx, hermesCommandTimeout)
	defer cancel()
	action := strings.ToLower(strings.TrimSpace(command.Action))
	if err := ValidateHermesCommand(command); err != nil {
		return HermesCommandResult{}, err
	}
	switch action {
	case "say":
		text := strings.TrimSpace(command.Text)
		if text == "" || len([]rune(text)) > 280 {
			return HermesCommandResult{}, fmt.Errorf("speech must contain 1 through 280 characters")
		}
		_, err = robot.Vector.Conn.SayText(ctx, &vectorpb.SayTextRequest{DurationScalar: 1, UseVectorVoice: true, Text: text})
		return HermesCommandResult{Action: action}, err
	case "drive":
		if !validMotion(command.LeftWheelMMPS, command.RightWheelMMPS, command.DurationMS) {
			return HermesCommandResult{}, fmt.Errorf("drive values exceed the bounded control range")
		}
		err = driveWheels(ctx, robot, command.LeftWheelMMPS, command.RightWheelMMPS)
		if err == nil {
			scheduleHermesStop(serial, "drive", command.DurationMS, func(stopCtx context.Context) error { return driveWheels(stopCtx, robot, 0, 0) })
		}
		return HermesCommandResult{Action: action, StopScheduled: err == nil}, err
	case "head":
		if !validJointMotion(command.SpeedRadPerSec, command.DurationMS) {
			return HermesCommandResult{}, fmt.Errorf("head values exceed the bounded control range")
		}
		_, err = robot.Vector.Conn.MoveHead(ctx, &vectorpb.MoveHeadRequest{SpeedRadPerSec: float32(command.SpeedRadPerSec)})
		if err == nil {
			scheduleHermesStop(serial, "head", command.DurationMS, func(stopCtx context.Context) error {
				_, stopErr := robot.Vector.Conn.MoveHead(stopCtx, &vectorpb.MoveHeadRequest{})
				return stopErr
			})
		}
		return HermesCommandResult{Action: action, StopScheduled: err == nil}, err
	case "lift":
		if !validJointMotion(command.SpeedRadPerSec, command.DurationMS) {
			return HermesCommandResult{}, fmt.Errorf("lift values exceed the bounded control range")
		}
		_, err = robot.Vector.Conn.MoveLift(ctx, &vectorpb.MoveLiftRequest{SpeedRadPerSec: float32(command.SpeedRadPerSec)})
		if err == nil {
			scheduleHermesStop(serial, "lift", command.DurationMS, func(stopCtx context.Context) error {
				_, stopErr := robot.Vector.Conn.MoveLift(stopCtx, &vectorpb.MoveLiftRequest{})
				return stopErr
			})
		}
		return HermesCommandResult{Action: action, StopScheduled: err == nil}, err
	case "stop":
		invalidateHermesStops(serial, "drive", "head", "lift")
		err = driveWheels(ctx, robot, 0, 0)
		if err == nil {
			_, err = robot.Vector.Conn.MoveHead(ctx, &vectorpb.MoveHeadRequest{})
		}
		if err == nil {
			_, err = robot.Vector.Conn.MoveLift(ctx, &vectorpb.MoveLiftRequest{})
		}
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
	case "stop":
	default:
		return fmt.Errorf("unsupported Hermes action")
	}
	return nil
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

func scheduleHermesStop(serial, axis string, durationMS int, stop func(context.Context) error) {
	generation := nextHermesGeneration(serial, axis)
	time.AfterFunc(time.Duration(durationMS)*time.Millisecond, func() {
		hermesControl.Lock()
		defer hermesControl.Unlock()
		if hermesControl.generation[serial][axis] != generation {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), hermesCommandTimeout)
		defer cancel()
		_ = stop(ctx)
	})
}

func nextHermesGeneration(serial, axis string) uint64 {
	if hermesControl.generation[serial] == nil {
		hermesControl.generation[serial] = make(map[string]uint64)
	}
	hermesControl.generation[serial][axis]++
	return hermesControl.generation[serial][axis]
}

func invalidateHermesStops(serial string, axes ...string) {
	for _, axis := range axes {
		_ = nextHermesGeneration(serial, axis)
	}
}
