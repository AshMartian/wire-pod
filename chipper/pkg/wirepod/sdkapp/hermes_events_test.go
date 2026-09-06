package sdkapp

import (
	"testing"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
)

func hermesRobotState(status uint32) *vectorpb.EventResponse {
	return &vectorpb.EventResponse{Event: &vectorpb.Event{EventType: &vectorpb.Event_RobotState{RobotState: &vectorpb.RobotState{Status: status}}}}
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
