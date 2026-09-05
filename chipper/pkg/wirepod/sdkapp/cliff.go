package sdkapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
)

const cliffSampleMaxAge = 2 * time.Second

type cliffReceiver interface {
	Recv() (*vectorpb.EventResponse, error)
}

type cliffStream struct {
	cancel context.CancelFunc
	done   chan struct{}
	status uint32
	stamp  time.Time
}

// The registry owns streams independently of the mutable robots slice. Removing
// one robot cannot redirect another robot's receiver to a different slice index.
type cliffRegistry struct {
	mu      sync.Mutex
	streams map[string]*cliffStream
}

var cliffStreams = &cliffRegistry{streams: make(map[string]*cliffStream)}

type cliffStatus struct {
	AnyDetected bool   `json:"any_detected"`
	Status      uint32 `json:"status"`
	StampMs     int64  `json:"stamp_ms"`
	SampleAgeMs int64  `json:"sample_age_ms"`
	Valid       bool   `json:"valid"`
	Source      string `json:"source"`
}

func cliffSerial(serial string) string { return strings.ToLower(strings.TrimSpace(serial)) }

// Stopping a stream must not create a new robot connection. Status reads use the
// existing SDK handler's activity timer so continued polling keeps it connected.
func (r *cliffRegistry) handleCachedRequest(w http.ResponseWriter, request *http.Request) bool {
	if request.URL.Path != "/api-sdk/get_cliff_status" && request.URL.Path != "/api-sdk/stop_cliff_stream" {
		return false
	}
	serial := cliffSerial(request.FormValue("serial"))
	if serial == "" {
		fmt.Fprint(w, "error: must provide serial")
		return true
	}
	if request.URL.Path == "/api-sdk/stop_cliff_stream" {
		r.stop(serial)
		fmt.Fprint(w, "done")
		return true
	}
	state, streaming := r.snapshot(serial)
	if !streaming {
		fmt.Fprint(w, "error: must start cliff stream")
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(state)
	return true
}

func (r *cliffRegistry) start(parent context.Context, serial string, connect func(context.Context) (cliffReceiver, error)) {
	serial = cliffSerial(serial)
	r.mu.Lock()
	if _, exists := r.streams[serial]; exists {
		r.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	stream := &cliffStream{cancel: cancel, done: make(chan struct{})}
	r.streams[serial] = stream
	r.mu.Unlock()
	go r.receive(ctx, serial, stream, connect)
}

func (r *cliffRegistry) receive(ctx context.Context, serial string, stream *cliffStream, connect func(context.Context) (cliffReceiver, error)) {
	defer close(stream.done)
	defer stream.cancel()
	defer func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.streams[serial] == stream {
			delete(r.streams, serial)
		}
	}()
	client, err := connect(ctx)
	if err != nil {
		return
	}
	for ctx.Err() == nil {
		response, err := client.Recv()
		if err != nil {
			return
		}
		state := response.GetEvent().GetRobotState()
		if state == nil {
			continue
		}
		r.mu.Lock()
		if ctx.Err() != nil || r.streams[serial] != stream {
			r.mu.Unlock()
			return
		}
		stream.status, stream.stamp = state.GetStatus(), time.Now()
		r.mu.Unlock()
	}
}

func (r *cliffRegistry) stop(serial string) {
	serial = cliffSerial(serial)
	r.mu.Lock()
	stream := r.streams[serial]
	delete(r.streams, serial)
	r.mu.Unlock()
	if stream != nil {
		// Cancel interrupts gRPC Recv; a boolean alone cannot stop a quiet stream.
		stream.cancel()
	}
}

func (r *cliffRegistry) snapshot(serial string) (cliffStatus, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stream := r.streams[cliffSerial(serial)]
	if stream == nil {
		return cliffStatus{}, false
	}
	result := cliffStatus{Status: stream.status, SampleAgeMs: -1, Source: "aggregate_robot_status"}
	result.AnyDetected = stream.status&uint32(vectorpb.RobotStatus_ROBOT_STATUS_CLIFF_DETECTED) != 0
	if !stream.stamp.IsZero() {
		age := time.Since(stream.stamp)
		result.StampMs, result.SampleAgeMs = stream.stamp.UnixMilli(), age.Milliseconds()
		result.Valid = age >= 0 && age <= cliffSampleMaxAge
	}
	return result, true
}
