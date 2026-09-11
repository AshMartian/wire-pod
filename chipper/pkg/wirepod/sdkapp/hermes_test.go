package sdkapp

import (
	"testing"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
)

func TestHermesProcessingCueUsesOneLongNativeAnimation(t *testing.T) {
	if hermesProcessingAnimation != "anim_knowledgegraph_searching_01" {
		t.Fatalf("unexpected processing animation: %q", hermesProcessingAnimation)
	}
	if hermesProcessingLoops != 24 {
		t.Fatalf("processing cue must use a stable long loop, got %d", hermesProcessingLoops)
	}
}

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

func TestHermesCommandValidationUsesClosedExpressionCatalog(t *testing.T) {
	for _, expression := range []string{"affectionate", "celebrate", "confused", "curious", "excited", "happy", "sad", "thinking"} {
		if err := ValidateHermesCommand(HermesCommand{Action: "express", Expression: expression}); err != nil {
			t.Fatalf("rejected supported expression %q: %v", expression, err)
		}
	}
	for _, expression := range []string{"", "angry", "anim_arbitrary_01"} {
		if err := ValidateHermesCommand(HermesCommand{Action: "express", Expression: expression}); err == nil {
			t.Fatalf("accepted unsupported expression %q", expression)
		}
	}
}

func TestHermesUndockRequiresFullVectorOnCharger(t *testing.T) {
	fullOnCharger := &vectorpb.BatteryStateResponse{
		BatteryLevel:        vectorpb.BatteryLevel_BATTERY_LEVEL_FULL,
		IsOnChargerPlatform: true,
	}
	if !eligibleForHermesUndock(fullOnCharger) {
		t.Fatal("rejected a full Vector on its charger")
	}

	for name, battery := range map[string]*vectorpb.BatteryStateResponse{
		"nil":            nil,
		"not full":       {BatteryLevel: vectorpb.BatteryLevel_BATTERY_LEVEL_NOMINAL, IsOnChargerPlatform: true},
		"not on charger": {BatteryLevel: vectorpb.BatteryLevel_BATTERY_LEVEL_FULL},
	} {
		t.Run(name, func(t *testing.T) {
			if eligibleForHermesUndock(battery) {
				t.Fatal("accepted an unsafe undock state")
			}
		})
	}
}
