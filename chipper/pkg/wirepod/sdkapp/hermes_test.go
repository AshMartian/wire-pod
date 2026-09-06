package sdkapp

import (
	"testing"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
)

func TestHermesCommandValidationRejectsUnsafeMotion(t *testing.T) {
	if err := ValidateHermesCommand(HermesCommand{Action: "drive", LeftWheelMMPS: 201, DurationMS: 50}); err == nil {
		t.Fatal("accepted unsafe wheel speed")
	}
	if err := ValidateHermesCommand(HermesCommand{Action: "head", SpeedRadPerSec: 1, DurationMS: 49}); err == nil {
		t.Fatal("accepted unsafe motion duration")
	}
}

func TestHermesCommandValidationAllowsBoundedAutonomyPrimitives(t *testing.T) {
	for _, action := range []string{"undock", "scan"} {
		if err := ValidateHermesCommand(HermesCommand{Action: action}); err != nil {
			t.Fatalf("rejected %q: %v", action, err)
		}
	}
}

func TestHermesUndockRequiresEveryChargerGate(t *testing.T) {
	fullOnCharger := &vectorpb.BatteryStateResponse{
		BatteryLevel:        vectorpb.BatteryLevel_BATTERY_LEVEL_FULL,
		IsCharging:          true,
		IsOnChargerPlatform: true,
	}
	if !eligibleForHermesUndock(fullOnCharger) {
		t.Fatal("rejected a full Vector that is charging on its charger")
	}

	for name, battery := range map[string]*vectorpb.BatteryStateResponse{
		"nil":            nil,
		"not full":       {BatteryLevel: vectorpb.BatteryLevel_BATTERY_LEVEL_NOMINAL, IsCharging: true, IsOnChargerPlatform: true},
		"not charging":   {BatteryLevel: vectorpb.BatteryLevel_BATTERY_LEVEL_FULL, IsOnChargerPlatform: true},
		"not on charger": {BatteryLevel: vectorpb.BatteryLevel_BATTERY_LEVEL_FULL, IsCharging: true},
	} {
		t.Run(name, func(t *testing.T) {
			if eligibleForHermesUndock(battery) {
				t.Fatal("accepted an unsafe undock state")
			}
		})
	}
}
