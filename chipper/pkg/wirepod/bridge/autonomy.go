package bridge

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/kercre123/wire-pod/chipper/pkg/wirepod/sdkapp"
)

const hermesAutonomyEnv = "WIREPOD_HERMES_AUTONOMY"

type autonomyTarget struct {
	Enabled        bool `json:"enabled"`
	PollSeconds    int  `json:"poll_seconds"`
	StableSeconds  int  `json:"stable_seconds"`
	DayStartHour   int  `json:"day_start_hour"`
	NightStartHour int  `json:"night_start_hour"`
}

func autonomyTargetsFromEnv(value string) (map[string]sdkapp.HermesAutonomyConfig, error) {
	if strings.TrimSpace(value) == "" {
		return map[string]sdkapp.HermesAutonomyConfig{}, nil
	}
	var raw map[string]autonomyTarget
	if err := json.Unmarshal([]byte(value), &raw); err != nil {
		return nil, os.ErrInvalid
	}
	targets := make(map[string]sdkapp.HermesAutonomyConfig, len(raw))
	for esn, target := range raw {
		esn = strings.ToLower(strings.TrimSpace(esn))
		if esn == "" || target.PollSeconds < 60 || target.StableSeconds < 300 || target.DayStartHour < 0 || target.DayStartHour > 23 || target.NightStartHour < 0 || target.NightStartHour > 23 || target.DayStartHour == target.NightStartHour {
			return nil, os.ErrInvalid
		}
		targets[esn] = sdkapp.HermesAutonomyConfig{
			Enabled:        target.Enabled,
			PollInterval:   time.Duration(target.PollSeconds) * time.Second,
			StableFor:      time.Duration(target.StableSeconds) * time.Second,
			DayStartHour:   target.DayStartHour,
			NightStartHour: target.NightStartHour,
		}
	}
	return targets, nil
}
