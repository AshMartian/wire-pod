package sdkapp

import "testing"

func TestHermesCommandValidationRejectsUnsafeMotion(t *testing.T) {
	if err := ValidateHermesCommand(HermesCommand{Action: "drive", LeftWheelMMPS: 201, DurationMS: 50}); err == nil {
		t.Fatal("accepted unsafe wheel speed")
	}
	if err := ValidateHermesCommand(HermesCommand{Action: "head", SpeedRadPerSec: 1, DurationMS: 49}); err == nil {
		t.Fatal("accepted unsafe motion duration")
	}
}
