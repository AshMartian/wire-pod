package sdkapp

import (
	"testing"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
)

func hermesRobotState(status uint32) *vectorpb.EventResponse {
	return &vectorpb.EventResponse{Event: &vectorpb.Event{EventType: &vectorpb.Event_RobotState{RobotState: &vectorpb.RobotState{Status: status}}}}
}

func hermesRobotStateWithTouch(rawTouch uint32) *vectorpb.EventResponse {
	return &vectorpb.EventResponse{Event: &vectorpb.Event{EventType: &vectorpb.Event_RobotState{RobotState: &vectorpb.RobotState{TouchData: &vectorpb.TouchData{RawTouchValue: rawTouch}}}}}
}

func TestHermesEdgeEventOnlyForCliffRisingEdge(t *testing.T) {
	serial := "hermes-edge-test"
	hermesEventStreams.Lock()
	delete(hermesEventStreams.edge, serial)
	hermesEventStreams.Unlock()
	defer func() {
		hermesEventStreams.Lock()
		delete(hermesEventStreams.edge, serial)
		hermesEventStreams.Unlock()
	}()

	detected := uint32(vectorpb.RobotStatus_ROBOT_STATUS_CLIFF_DETECTED)
	event, isState := hermesEdgeEventFromResponse(serial, hermesRobotState(0))
	if !isState || event.Type != "" {
		t.Fatalf("clear cliff state emitted %+v", event)
	}
	event, isState = hermesEdgeEventFromResponse(serial, hermesRobotState(detected))
	if !isState || event.Type != "wirepod.edge_detected" || event.Reason != "cliff_sensor" {
		t.Fatalf("cliff rising edge did not emit the expected event: %+v", event)
	}
	event, isState = hermesEdgeEventFromResponse(serial, hermesRobotState(detected))
	if !isState || event.Type != "" {
		t.Fatalf("sustained cliff state emitted %+v", event)
	}
	_, _ = hermesEdgeEventFromResponse(serial, hermesRobotState(0))
	event, isState = hermesEdgeEventFromResponse(serial, hermesRobotState(detected))
	if !isState || event.Type != "wirepod.edge_detected" || event.Reason != "cliff_sensor" {
		t.Fatalf("cliff rising edge did not emit the expected event: %+v", event)
	}
}

func TestHermesTouchEventRequiresSustainedBaselineRelativeContact(t *testing.T) {
	serial := "hermes-touch-test"
	hermesEventStreams.Lock()
	delete(hermesEventStreams.touch, serial)
	hermesEventStreams.Unlock()
	defer func() {
		hermesEventStreams.Lock()
		delete(hermesEventStreams.touch, serial)
		hermesEventStreams.Unlock()
	}()

	if event := hermesTouchEventFromResponse(serial, hermesRobotStateWithTouch(100)); event.Type != "" {
		t.Fatalf("baseline emitted %+v", event)
	}
	for sample := 0; sample < 5; sample++ {
		if event := hermesTouchEventFromResponse(serial, hermesRobotStateWithTouch(151)); event.Type != "" {
			t.Fatalf("short contact emitted at sample %d: %+v", sample, event)
		}
	}
	event := hermesTouchEventFromResponse(serial, hermesRobotStateWithTouch(151))
	if event.Type != "wirepod.touch_detected" || event.Reason != "touch_sensor" {
		t.Fatalf("sustained touch did not emit the expected event: %+v", event)
	}
	if event := hermesTouchEventFromResponse(serial, hermesRobotStateWithTouch(151)); event.Type != "" {
		t.Fatalf("sustained contact emitted twice: %+v", event)
	}
	_ = hermesTouchEventFromResponse(serial, hermesRobotStateWithTouch(100))
	for sample := 0; sample < 6; sample++ {
		event = hermesTouchEventFromResponse(serial, hermesRobotStateWithTouch(151))
	}
	if event.Type != "wirepod.touch_detected" {
		t.Fatalf("released and renewed contact did not emit: %+v", event)
	}
}
