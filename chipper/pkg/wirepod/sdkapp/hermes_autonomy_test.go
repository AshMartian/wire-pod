package sdkapp

import (
	"testing"
	"time"
)

func TestAutonomyEligibilityRequiresExactFullChargedDockedState(t *testing.T) {
	eligible := HermesObservation{BatteryLevel: "BATTERY_LEVEL_FULL", IsOnChargerPlatform: true}
	if !isAutonomyEligible(eligible) {
		t.Fatal("expected a full, docked Vector to be eligible")
	}
	for _, observation := range []HermesObservation{
		{BatteryLevel: "BATTERY_LEVEL_NOMINAL", IsOnChargerPlatform: true},
		{BatteryLevel: "BATTERY_LEVEL_FULL", IsOnChargerPlatform: false},
	} {
		if isAutonomyEligible(observation) {
			t.Fatalf("accepted unsafe autonomy state: %+v", observation)
		}
	}
}

func TestAutonomyDaytimeExcludesNight(t *testing.T) {
	config := normalizedAutonomyConfig(HermesAutonomyConfig{Enabled: true})
	for _, hour := range []int{0, 7, 22, 23} {
		now := time.Date(2026, time.September, 6, hour, 0, 0, 0, time.Local)
		if isAutonomyDaytime(now, config) {
			t.Fatalf("hour %d should be excluded", hour)
		}
	}
	if !isAutonomyDaytime(time.Date(2026, time.September, 6, 12, 0, 0, 0, time.Local), config) {
		t.Fatal("midday should be eligible")
	}
}

func TestAutonomyNormalizationNeverLeavesAnEmptyDaytimeWindow(t *testing.T) {
	config := normalizedAutonomyConfig(HermesAutonomyConfig{
		DayStartHour:   22,
		NightStartHour: 22,
	})
	if config.DayStartHour == config.NightStartHour {
		t.Fatal("normalization left an empty daytime window")
	}
}
