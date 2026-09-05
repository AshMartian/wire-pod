package sdkapp

import (
	"context"
	"testing"
	"time"
)

func TestHermesStopsAreIndependentPerAxis(t *testing.T) {
	hermesControl.Lock()
	hermesControl.generation = make(map[string]map[string]uint64)
	headStopped := make(chan struct{}, 1)
	driveStopped := make(chan struct{}, 1)
	scheduleHermesStop("robot", "head", 50, func(context.Context) error {
		headStopped <- struct{}{}
		return nil
	})
	scheduleHermesStop("robot", "drive", 50, func(context.Context) error {
		driveStopped <- struct{}{}
		return nil
	})
	hermesControl.Unlock()
	for axis, stopped := range map[string]chan struct{}{"head": headStopped, "drive": driveStopped} {
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatalf("%s stop was cancelled by a different axis", axis)
		}
	}
}

func TestHermesStopInvalidatesEveryAxisTimer(t *testing.T) {
	hermesControl.Lock()
	hermesControl.generation = make(map[string]map[string]uint64)
	stopped := make(chan struct{}, 1)
	scheduleHermesStop("robot", "lift", 50, func(context.Context) error {
		stopped <- struct{}{}
		return nil
	})
	invalidateHermesStops("robot", "drive", "head", "lift")
	hermesControl.Unlock()
	select {
	case <-stopped:
		t.Fatal("explicit stop failed to cancel the lift timer")
	case <-time.After(100 * time.Millisecond):
	}
}
