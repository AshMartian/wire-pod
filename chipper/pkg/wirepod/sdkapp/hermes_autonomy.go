package sdkapp

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kercre123/wire-pod/chipper/pkg/logger"
)

// HermesAutonomyConfig controls the Pi-owned eligibility monitor. It never
// drives a robot: a fresh, signed readiness event lets the robot's own Hermes
// profile choose the narrow undock and stationary scan tools.
type HermesAutonomyConfig struct {
	Enabled        bool
	PollInterval   time.Duration
	StableFor      time.Duration
	DayStartHour   int
	NightStartHour int
}

type hermesAutonomyState struct {
	eligibleSince time.Time
	emitted       bool
}

var hermesAutonomy = struct {
	sync.Mutex
	active map[string]struct{}
}{active: make(map[string]struct{})}

func StartHermesAutonomy(serial string, config HermesAutonomyConfig, sink func(HermesRobotEvent)) {
	serial = cliffSerial(serial)
	config = normalizedAutonomyConfig(config)
	if serial == "" || !config.Enabled || sink == nil {
		return
	}
	hermesAutonomy.Lock()
	if _, exists := hermesAutonomy.active[serial]; exists {
		hermesAutonomy.Unlock()
		return
	}
	hermesAutonomy.active[serial] = struct{}{}
	hermesAutonomy.Unlock()
	go runHermesAutonomy(serial, config, sink)
}

func normalizedAutonomyConfig(config HermesAutonomyConfig) HermesAutonomyConfig {
	if config.PollInterval < time.Minute {
		config.PollInterval = time.Minute
	}
	if config.StableFor < 5*time.Minute {
		config.StableFor = 5 * time.Minute
	}
	if config.DayStartHour == 0 && config.NightStartHour == 0 {
		config.DayStartHour = 8
		config.NightStartHour = 22
	}
	if config.DayStartHour < 0 || config.DayStartHour > 23 {
		config.DayStartHour = 8
	}
	if config.NightStartHour < 0 || config.NightStartHour > 23 || config.NightStartHour == config.DayStartHour {
		config.NightStartHour = 22
		if config.NightStartHour == config.DayStartHour {
			config.NightStartHour = 8
		}
	}
	return config
}

func runHermesAutonomy(serial string, config HermesAutonomyConfig, sink func(HermesRobotEvent)) {
	defer func() {
		hermesAutonomy.Lock()
		delete(hermesAutonomy.active, serial)
		hermesAutonomy.Unlock()
	}()
	state := hermesAutonomyState{}
	ticker := time.NewTicker(config.PollInterval)
	defer ticker.Stop()
	for now := range ticker.C {
		if !isAutonomyDaytime(now, config) {
			if !state.eligibleSince.IsZero() {
				logger.Println(fmt.Sprintf("Hermes autonomy: pausing %s for the nighttime exclusion", serial))
			}
			state.eligibleSince = time.Time{}
			continue
		}
		observation, err := HermesObserve(serial)
		if err != nil {
			logger.Println(fmt.Sprintf("Hermes autonomy: observation unavailable for %s: %v", serial, err))
			state = hermesAutonomyState{}
			continue
		}
		if !isAutonomyEligible(observation) {
			if !state.eligibleSince.IsZero() {
				logger.Println(fmt.Sprintf("Hermes autonomy: %s is no longer full and docked; resetting eligibility", serial))
			}
			state = hermesAutonomyState{}
			continue
		}
		if state.eligibleSince.IsZero() {
			state.eligibleSince = now
			logger.Println(fmt.Sprintf("Hermes autonomy: monitoring full, docked Vector %s for %s", serial, config.StableFor))
			continue
		}
		if state.emitted || now.Sub(state.eligibleSince) < config.StableFor {
			continue
		}
		state.emitted = true
		logger.Println(fmt.Sprintf("Hermes autonomy: emitting full-charge readiness for %s", serial))
		sink(HermesRobotEvent{
			SchemaVersion: 1,
			Type:          "wirepod.autonomy_ready",
			EventID:       "full-charge-" + strings.ToLower(serial) + "-" + now.Format("20060102"),
			ESN:           serial,
			Reason:        "battery_full_on_charger",
			ObservedAt:    now.UnixMilli(),
			ExpiresAt:     now.Add(time.Minute).UnixMilli(),
			Observation: &HermesAutonomyObservation{
				BatteryLevel:        observation.BatteryLevel,
				BatteryVolts:        observation.BatteryVolts,
				IsCharging:          observation.IsCharging,
				IsOnChargerPlatform: observation.IsOnChargerPlatform,
			},
		})
	}
}

func isAutonomyDaytime(now time.Time, config HermesAutonomyConfig) bool {
	hour := now.Hour()
	return hour >= config.DayStartHour && hour < config.NightStartHour
}

func isAutonomyEligible(observation HermesObservation) bool {
	return observation.BatteryLevel == "BATTERY_LEVEL_FULL" && observation.IsOnChargerPlatform
}
