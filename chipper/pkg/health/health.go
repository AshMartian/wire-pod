// Package health reports process and service readiness without exposing robot
// credentials, configuration, or a dependency on external version services.
package health

import (
	"encoding/json"
	"net/http"
	"sync"
)

type State struct {
	mu         sync.RWMutex
	sourceSHA  string
	configured bool
	speech     bool
	listener   bool
	generation uint64
}

type Response struct {
	SourceSHA string `json:"source_sha"`
	Status    string `json:"status"`
}

func New(sourceSHA string) *State { return &State{sourceSHA: sourceSHA} }

func (s *State) Speech(configured, ready bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configured, s.speech = configured, ready
}

// BeginListener invalidates previous serving loops so a retiring generation
// cannot mark its replacement unavailable after a restart.
func (s *State) BeginListener() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generation++
	s.listener = false
	return s.generation
}

func (s *State) Listener(generation uint64, ready bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation == s.generation {
		s.listener = ready
	}
}

func (s *State) Register(mux *http.ServeMux) {
	mux.HandleFunc("/health/live", s.Live)
	mux.HandleFunc("/health/ready", s.Ready)
}

func (s *State) Live(w http.ResponseWriter, r *http.Request) {
	s.write(w, r, false)
}

func (s *State) Ready(w http.ResponseWriter, r *http.Request) {
	s.write(w, r, true)
}

func (s *State) write(w http.ResponseWriter, r *http.Request, readiness bool) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	response := Response{SourceSHA: s.sourceSHA, Status: "live"}
	code := http.StatusOK
	if readiness {
		code = http.StatusServiceUnavailable
		switch {
		case !s.configured:
			response.Status = "setup_required"
		case !s.speech:
			response.Status = "speech_unavailable"
		case !s.listener:
			response.Status = "starting"
		default:
			response.Status = "ready"
			code = http.StatusOK
		}
	}
	s.mu.RUnlock()
	w.WriteHeader(code)
	if r.Method != http.MethodHead {
		_ = json.NewEncoder(w).Encode(response)
	}
}
