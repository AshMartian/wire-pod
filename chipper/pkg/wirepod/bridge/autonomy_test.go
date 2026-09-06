package bridge

import "testing"

func TestAutonomyTargetsRejectUnsafeCadenceAndAcceptGuardedWindow(t *testing.T) {
	if _, err := autonomyTargetsFromEnv(`{"ESN-A":{"enabled":true,"poll_seconds":59,"stable_seconds":300,"day_start_hour":8,"night_start_hour":22}}`); err == nil {
		t.Fatal("accepted a sub-minute autonomy poll")
	}
	targets, err := autonomyTargetsFromEnv(`{"ESN-A":{"enabled":true,"poll_seconds":60,"stable_seconds":300,"day_start_hour":8,"night_start_hour":22}}`)
	if err != nil || !targets["esn-a"].Enabled {
		t.Fatalf("valid autonomy config rejected: targets=%+v err=%v", targets, err)
	}
}
