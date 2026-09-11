package sdkapp

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
)

// HermesRobotEvent contains only the compact, useful part of a robot event.
// In particular, face landmark geometry and camera frames stay on the Pi and
// are never forwarded to an agent webhook; the profile-scoped snapshot tool
// obtains a fresh frame only when the receiving Hermes route needs one.
type HermesRobotEvent struct {
	SchemaVersion int                        `json:"schema_version,omitempty"`
	Type          string                     `json:"event_type"`
	EventID       string                     `json:"event_id,omitempty"`
	ESN           string                     `json:"esn"`
	FaceID        int32                      `json:"face_id,omitempty"`
	Name          string                     `json:"name,omitempty"`
	Expression    string                     `json:"expression,omitempty"`
	OldFaceID     int32                      `json:"old_face_id,omitempty"`
	NewFaceID     int32                      `json:"new_face_id,omitempty"`
	Reason        string                     `json:"reason,omitempty"`
	Message       string                     `json:"message,omitempty"`
	SnapshotID    string                     `json:"snapshot_id,omitempty"`
	CapturedAt    int64                      `json:"captured_at_unix_ms,omitempty"`
	SnapshotUntil int64                      `json:"snapshot_expires_at_unix_ms,omitempty"`
	ExpiresAt     int64                      `json:"expires_at_unix_ms,omitempty"`
	Observation   *HermesAutonomyObservation `json:"observation,omitempty"`
	ObservedAt    int64                      `json:"observed_at_unix_ms"`
}

// HermesAutonomyObservation is deliberately compact enough for a signed
// webhook. Camera frames and raw proximity data remain on the Pi.
type HermesAutonomyObservation struct {
	BatteryLevel        string  `json:"battery_level"`
	BatteryVolts        float32 `json:"battery_volts"`
	IsCharging          bool    `json:"is_charging"`
	IsOnChargerPlatform bool    `json:"is_on_charger_platform"`
}

type hermesEventStream struct {
	cancel context.CancelFunc
}

type hermesTouchState struct {
	baseline    uint32
	initialized bool
	consecutive int
	active      bool
}

var hermesEventStreams = struct {
	sync.Mutex
	streams map[string]hermesEventStream
	last    map[string]time.Time
	edge    map[string]bool
	touch   map[string]hermesTouchState
}{streams: make(map[string]hermesEventStream), last: make(map[string]time.Time), edge: make(map[string]bool), touch: make(map[string]hermesTouchState)}

// StartHermesEvents consumes the Vector event stream once per robot and hands
// safe face-related events to sink. It never blocks the SDK receive loop on a
// network delivery; callers must enqueue quickly and may deliberately drop an
// event under downstream backpressure.
func StartHermesEvents(serial string, sink func(HermesRobotEvent)) {
	serial = cliffSerial(serial)
	if serial == "" || sink == nil {
		return
	}
	hermesEventStreams.Lock()
	if _, exists := hermesEventStreams.streams[serial]; exists {
		hermesEventStreams.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	hermesEventStreams.streams[serial] = hermesEventStream{cancel: cancel}
	hermesEventStreams.Unlock()
	go runHermesEvents(ctx, cancel, serial, sink)
}

func runHermesEvents(ctx context.Context, cancel context.CancelFunc, serial string, sink func(HermesRobotEvent)) {
	defer cancel()
	defer func() {
		hermesEventStreams.Lock()
		delete(hermesEventStreams.streams, serial)
		delete(hermesEventStreams.edge, serial)
		delete(hermesEventStreams.touch, serial)
		hermesEventStreams.Unlock()
	}()
	backoff := time.Second
	for ctx.Err() == nil {
		err := receiveHermesEvents(ctx, serial, sink)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			logger.Println("Hermes robot event stream ended; retrying")
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func receiveHermesEvents(ctx context.Context, serial string, sink func(HermesRobotEvent)) error {
	robot, _, err := getRobot(serial)
	if err != nil {
		return err
	}
	streamCtx, cancel := context.WithCancel(robot.Ctx)
	defer cancel()
	if err := enableHermesFaceDetection(streamCtx, robot); err != nil {
		logger.Println("Hermes face detection could not be enabled:", err)
	}
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-streamCtx.Done():
		}
	}()
	stream, err := robot.Vector.Conn.EventStream(streamCtx, &vectorpb.EventRequest{
		ListType: &vectorpb.EventRequest_WhiteList{WhiteList: &vectorpb.FilterList{List: []string{
			"robot_observed_face", "robot_changed_observed_face_id", "robot_state", "vision_modes_auto_disabled",
		}}},
		ConnectionId: "wirepod-hermes",
	})
	if err != nil {
		return err
	}
	for streamCtx.Err() == nil {
		response, err := stream.Recv()
		if err != nil {
			return err
		}
		if event, isState := hermesEdgeEventFromResponse(serial, response); isState {
			if event.Type != "" {
				sink(event)
			}
			if event := hermesTouchEventFromResponse(serial, response); event.Type != "" {
				sink(event)
			}
			continue
		}
		if response.GetEvent().GetVisionModesAutoDisabled() != nil {
			if err := enableHermesFaceDetection(streamCtx, robot); err != nil {
				logger.Println("Hermes face detection could not be re-enabled:", err)
			}
			continue
		}
		event := hermesEventFromResponse(serial, response)
		if event.Type == "" || !shouldForwardHermesEvent(event) {
			continue
		}
		sink(event)
	}
	return streamCtx.Err()
}

func enableHermesFaceDetection(ctx context.Context, robot Robot) error {
	_, err := robot.Vector.Conn.EnableFaceDetection(ctx, &vectorpb.EnableFaceDetectionRequest{
		Enable:                     true,
		EnableExpressionEstimation: true,
	})
	return err
}

// hermesEdgeEventFromResponse turns a sustained cliff sensor assertion into a
// single rising-edge event. The state packet stream can be very frequent, and
// taking a camera image for every sample would create an unsafe model storm.
func hermesEdgeEventFromResponse(serial string, response *vectorpb.EventResponse) (HermesRobotEvent, bool) {
	state := response.GetEvent().GetRobotState()
	if state == nil {
		return HermesRobotEvent{}, false
	}
	detected := state.GetStatus()&uint32(vectorpb.RobotStatus_ROBOT_STATUS_CLIFF_DETECTED) != 0
	hermesEventStreams.Lock()
	previous := hermesEventStreams.edge[serial]
	hermesEventStreams.edge[serial] = detected
	hermesEventStreams.Unlock()
	if !detected || previous {
		return HermesRobotEvent{}, true
	}
	return HermesRobotEvent{Type: "wirepod.edge_detected", ESN: serial, Reason: "cliff_sensor", ObservedAt: time.Now().UnixMilli()}, true
}

// hermesTouchEventFromResponse emits one event for a deliberate, sustained
// pet. The touch sensor is a noisy raw signal, so a baseline-relative threshold
// and consecutive samples avoid turning incidental contact into agent turns.
func hermesTouchEventFromResponse(serial string, response *vectorpb.EventResponse) HermesRobotEvent {
	state := response.GetEvent().GetRobotState()
	if state == nil {
		return HermesRobotEvent{}
	}
	rawTouch := state.GetTouchData().GetRawTouchValue()
	hermesEventStreams.Lock()
	touch := hermesEventStreams.touch[serial]
	if !touch.initialized {
		touch.baseline = rawTouch
		touch.initialized = true
		hermesEventStreams.touch[serial] = touch
		hermesEventStreams.Unlock()
		return HermesRobotEvent{}
	}
	if rawTouch > touch.baseline+50 {
		touch.consecutive++
	} else {
		touch.consecutive = 0
		touch.active = false
		// Let the baseline follow slow sensor drift while never adapting upward
		// during a petting contact.
		if rawTouch < touch.baseline {
			touch.baseline = rawTouch
		} else {
			touch.baseline += (rawTouch - touch.baseline) / 8
		}
	}
	shouldEmit := !touch.active && touch.consecutive >= 6
	if shouldEmit {
		touch.active = true
	}
	hermesEventStreams.touch[serial] = touch
	hermesEventStreams.Unlock()
	if !shouldEmit {
		return HermesRobotEvent{}
	}
	return HermesRobotEvent{
		Type:       "wirepod.touch_detected",
		ESN:        serial,
		Reason:     "touch_sensor",
		Message:    "You're being loved!",
		ObservedAt: time.Now().UnixMilli(),
	}
}

func hermesEventsActive(serial string) bool {
	hermesEventStreams.Lock()
	defer hermesEventStreams.Unlock()
	_, active := hermesEventStreams.streams[strings.ToLower(strings.TrimSpace(serial))]
	return active
}

func hermesEventFromResponse(serial string, response *vectorpb.EventResponse) HermesRobotEvent {
	event := response.GetEvent()
	if face := event.GetRobotObservedFace(); face != nil {
		typeName := "wirepod.face_observed"
		if strings.TrimSpace(face.GetName()) != "" {
			typeName = "wirepod.face_recognized"
		}
		return HermesRobotEvent{Type: typeName, ESN: serial, FaceID: face.GetFaceId(), Name: face.GetName(), Expression: face.GetExpression().String(), ObservedAt: time.Now().UnixMilli()}
	}
	if changed := event.GetRobotChangedObservedFaceId(); changed != nil {
		return HermesRobotEvent{Type: "wirepod.face_identity_updated", ESN: serial, OldFaceID: changed.GetOldId(), NewFaceID: changed.GetNewId(), ObservedAt: time.Now().UnixMilli()}
	}
	return HermesRobotEvent{}
}

func shouldForwardHermesEvent(event HermesRobotEvent) bool {
	// Vision reports a tracked face repeatedly. One notification per face/type
	// every fifteen seconds is enough to let Hermes form a memory without
	// creating a storm of model invocations.
	key := event.ESN + "|" + event.Type + "|" + event.Name + "|" + strconv.Itoa(int(event.FaceID))
	now := time.Now()
	hermesEventStreams.Lock()
	defer hermesEventStreams.Unlock()
	cooldown := 15 * time.Second
	if event.Type == "wirepod.touch_detected" {
		// Affection should arrive as a meaningful event, not a stream of model
		// turns while someone is petting the robot. A renewed pet can still be
		// noticed after the current agent turn has had time to act or respond.
		cooldown = 20 * time.Second
	}
	if previous, ok := hermesEventStreams.last[key]; ok && now.Sub(previous) < cooldown {
		return false
	}
	hermesEventStreams.last[key] = now
	return true
}
