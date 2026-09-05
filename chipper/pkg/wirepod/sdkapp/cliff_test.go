package sdkapp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
)

type cliffMessage struct {
	response *vectorpb.EventResponse
	err      error
}

type fakeCliffReceiver struct {
	ctx      context.Context
	messages chan cliffMessage
}

func (f *fakeCliffReceiver) Recv() (*vectorpb.EventResponse, error) {
	select {
	case <-f.ctx.Done():
		return nil, f.ctx.Err()
	case message := <-f.messages:
		return message.response, message.err
	}
}

func cliffEvent(status uint32) *vectorpb.EventResponse {
	return &vectorpb.EventResponse{Event: &vectorpb.Event{
		EventType: &vectorpb.Event_RobotState{RobotState: &vectorpb.RobotState{Status: status}},
	}}
}

func waitForCliff(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("cliff stream did not reach expected state")
		}
		time.Sleep(time.Millisecond)
	}
}

func startTestCliff(t *testing.T, registry *cliffRegistry, serial string) *fakeCliffReceiver {
	t.Helper()
	connected := make(chan *fakeCliffReceiver, 1)
	registry.start(context.Background(), serial, func(ctx context.Context) (cliffReceiver, error) {
		receiver := &fakeCliffReceiver{ctx: ctx, messages: make(chan cliffMessage, 8)}
		connected <- receiver
		return receiver, nil
	})
	t.Cleanup(func() { registry.stop(serial) })
	select {
	case receiver := <-connected:
		return receiver
	case <-time.After(2 * time.Second):
		t.Fatal("cliff connection did not start")
		return nil
	}
}

func TestCliffRobotsRemainIndependent(t *testing.T) {
	registry := &cliffRegistry{streams: make(map[string]*cliffStream)}
	first := startTestCliff(t, registry, " ROBOT-A ")
	second := startTestCliff(t, registry, "robot-b")
	const detected = uint32(vectorpb.RobotStatus_ROBOT_STATUS_CLIFF_DETECTED)
	first.messages <- cliffMessage{response: cliffEvent(detected)}
	second.messages <- cliffMessage{response: cliffEvent(1)}
	waitForCliff(t, func() bool {
		a, _ := registry.snapshot("robot-a")
		b, _ := registry.snapshot("ROBOT-B")
		return a.Valid && b.Valid && a.AnyDetected && !b.AnyDetected && b.Status == 1
	})
	registry.stop("robot-a")
	select {
	case <-first.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("stopping did not cancel the blocked receiver")
	}
	second.messages <- cliffMessage{response: cliffEvent(detected | 2)}
	waitForCliff(t, func() bool {
		b, active := registry.snapshot("robot-b")
		return active && b.AnyDetected && b.Status == detected|2
	})
	if _, active := registry.snapshot("robot-a"); active {
		t.Fatal("removed robot still has a stream")
	}
}

func TestCliffStartIsIdempotentAndFailureCanRestart(t *testing.T) {
	registry := &cliffRegistry{streams: make(map[string]*cliffStream)}
	first := startTestCliff(t, registry, "robot")
	registry.start(context.Background(), "ROBOT", func(context.Context) (cliffReceiver, error) {
		t.Error("idempotent begin opened a second connection")
		return nil, errors.New("unexpected connection")
	})
	first.messages <- cliffMessage{err: errors.New("robot disconnected")}
	waitForCliff(t, func() bool { _, active := registry.snapshot("robot"); return !active })
	second := startTestCliff(t, registry, "robot")
	second.messages <- cliffMessage{response: cliffEvent(0)}
	waitForCliff(t, func() bool { status, _ := registry.snapshot("robot"); return status.Valid })
}

// This receiver deliberately returns one buffered sample even after cancel, as
// gRPC can do when data and cancellation become available at the same time.
type lateCliffReceiver struct {
	started chan struct{}
	release chan struct{}
}

func (f *lateCliffReceiver) Recv() (*vectorpb.EventResponse, error) {
	close(f.started)
	<-f.release
	return cliffEvent(uint32(vectorpb.RobotStatus_ROBOT_STATUS_CLIFF_DETECTED)), nil
}

func TestCliffRetiringStreamCannotOverwriteReplacement(t *testing.T) {
	registry := &cliffRegistry{streams: make(map[string]*cliffStream)}
	late := &lateCliffReceiver{started: make(chan struct{}), release: make(chan struct{})}
	registry.start(context.Background(), "robot", func(context.Context) (cliffReceiver, error) { return late, nil })
	select {
	case <-late.started:
	case <-time.After(time.Second):
		t.Fatal("receiver did not start")
	}
	registry.mu.Lock()
	old := registry.streams["robot"]
	registry.mu.Unlock()
	registry.stop("robot")
	replacement := startTestCliff(t, registry, "robot")
	replacement.messages <- cliffMessage{response: cliffEvent(0)}
	waitForCliff(t, func() bool { status, _ := registry.snapshot("robot"); return status.Valid })
	close(late.release)
	select {
	case <-old.done:
	case <-time.After(time.Second):
		t.Fatal("retiring receiver failed to exit")
	}
	status, active := registry.snapshot("robot")
	if !active || !status.Valid || status.AnyDetected {
		t.Fatalf("retired receiver modified its replacement: %+v, active=%v", status, active)
	}
	replacement.messages <- cliffMessage{response: cliffEvent(8)}
	waitForCliff(t, func() bool {
		status, active := registry.snapshot("robot")
		return active && status.Valid && status.Status == 8 && !status.AnyDetected
	})
}

func TestCliffUnknownAndStaleAreNotClearSamples(t *testing.T) {
	registry := &cliffRegistry{streams: make(map[string]*cliffStream)}
	receiver := startTestCliff(t, registry, "robot")
	status, active := registry.snapshot("robot")
	if !active || status.Valid || status.SampleAgeMs != -1 || status.StampMs != 0 {
		t.Fatalf("unsampled status: %+v, active=%v", status, active)
	}
	receiver.messages <- cliffMessage{}
	receiver.messages <- cliffMessage{response: &vectorpb.EventResponse{}}
	receiver.messages <- cliffMessage{response: cliffEvent(0)}
	waitForCliff(t, func() bool { status, _ := registry.snapshot("robot"); return status.Valid })
	registry.mu.Lock()
	registry.streams["robot"].stamp = time.Now().Add(-cliffSampleMaxAge - time.Second)
	registry.mu.Unlock()
	status, active = registry.snapshot("robot")
	if !active || status.Valid || status.SampleAgeMs < cliffSampleMaxAge.Milliseconds() {
		t.Fatalf("stale sample reported usable: %+v", status)
	}
}

func TestCliffConnectFailureCleansUp(t *testing.T) {
	registry := &cliffRegistry{streams: make(map[string]*cliffStream)}
	registry.start(context.Background(), "robot", func(context.Context) (cliffReceiver, error) {
		return nil, errors.New("connection rejected")
	})
	waitForCliff(t, func() bool { _, active := registry.snapshot("robot"); return !active })
}

func TestCliffRobotContextCancellationCleansUp(t *testing.T) {
	registry := &cliffRegistry{streams: make(map[string]*cliffStream)}
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry.start(parent, "robot", func(ctx context.Context) (cliffReceiver, error) {
		return &fakeCliffReceiver{ctx: ctx}, nil
	})
	cancel()
	waitForCliff(t, func() bool { _, active := registry.snapshot("robot"); return !active })
	// A begin racing with removal inherits the cancelled robot context too.
	registry.start(parent, "robot", func(ctx context.Context) (cliffReceiver, error) {
		return &fakeCliffReceiver{ctx: ctx}, nil
	})
	waitForCliff(t, func() bool { _, active := registry.snapshot("robot"); return !active })
}

func TestCliffConcurrentLifecycle(t *testing.T) {
	registry := &cliffRegistry{streams: make(map[string]*cliffStream)}
	var callers sync.WaitGroup
	for i := 0; i < 4; i++ {
		callers.Add(1)
		go func() {
			defer callers.Done()
			for j := 0; j < 50; j++ {
				registry.start(context.Background(), "robot", func(ctx context.Context) (cliffReceiver, error) {
					return &fakeCliffReceiver{ctx: ctx}, nil
				})
				registry.snapshot("robot")
				registry.stop("robot")
			}
		}()
	}
	callers.Wait()
}

func TestCliffHTTPReportsAggregateOnlyAndStopsWithoutRobot(t *testing.T) {
	registry := &cliffRegistry{streams: make(map[string]*cliffStream)}
	startTestCliff(t, registry, "robot")
	w := httptest.NewRecorder()
	registry.handleCachedRequest(w, httptest.NewRequest("GET", "/api-sdk/get_cliff_status?serial=robot", nil))
	var response map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["source"] != "aggregate_robot_status" || response["valid"] != false {
		t.Fatalf("invalid unsampled contract: %v", response)
	}
	for _, fabricated := range []string{"sensors", "detected"} {
		if _, exists := response[fabricated]; exists {
			t.Fatalf("response pretends to have per-sensor data: %v", response)
		}
	}
	original := cliffStreams
	cliffStreams = registry
	t.Cleanup(func() { cliffStreams = original })
	w = httptest.NewRecorder()
	SdkapiHandler(w, httptest.NewRequest("POST", "/api-sdk/stop_cliff_stream?serial=robot", nil))
	if w.Body.String() != "done" {
		t.Fatalf("stop without an SDK robot failed: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	registry.handleCachedRequest(w, httptest.NewRequest("GET", "/api-sdk/get_cliff_status?serial=robot", nil))
	if w.Body.String() != "error: must start cliff stream" {
		t.Fatalf("stopped stream returned samples: %s", w.Body.String())
	}
}
