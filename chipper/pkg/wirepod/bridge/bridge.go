// Package bridge exposes a small, authenticated contract for external agent
// runtimes. It deliberately starts read-only; robot motion remains outside of
// this API until one shared control manager exists.
package bridge

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"github.com/kercre123/wire-pod/chipper/pkg/wirepod/sdkapp"
)

const prefix = "/bridge/v1/"

type Server struct {
	sourceSHA   string
	snapshot    func() ([]robot, error)
	credentials []credential
	control     func(string, sdkapp.HermesCommand) (sdkapp.HermesCommandResult, error)
	observe     func(string) (sdkapp.HermesObservation, error)
	capture     func(string) (sdkapp.HermesSnapshot, error)
	snapshots   *cameraSnapshotVault
}

type credential struct {
	esn   string
	token []byte
}

type robot struct {
	ESN       string `json:"esn"`
	Activated bool   `json:"activated"`
}

type robotsResponse struct {
	SourceSHA string  `json:"source_sha"`
	Robots    []robot `json:"robots"`
}

// RegisterFromEnv installs no routes unless WIREPOD_HERMES_BRIDGE_TOKENS is
// configured. The JSON value maps one ESN to one random token, keeping every
// Hermes profile confined to its own enrolled Vector.
func RegisterFromEnv(mux *http.ServeMux, sourceSHA string) bool {
	credentials, err := credentialsFromEnv(os.Getenv("WIREPOD_HERMES_BRIDGE_TOKENS"))
	if err != nil {
		return false
	}
	server := &Server{credentials: credentials, sourceSHA: sourceSHA, snapshot: robotsFromDisk, snapshots: newCameraSnapshotVault()}
	server.Register(mux)
	server.startEventForwarding()
	return true
}

func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc(prefix, s.handle)
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	scope, ok := s.authorized(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer realm="wire-pod bridge"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, prefix)
	switch {
	case path == "robots":
		if !allowMethod(w, r, http.MethodGet) {
			return
		}
		robots, err := s.robots()
		if err != nil {
			http.Error(w, "robot inventory is temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		entry, ok := robotByESN(robots, scope)
		if !ok {
			http.Error(w, "robot not enrolled", http.StatusNotFound)
			return
		}
		s.writeJSON(w, http.StatusOK, robotsResponse{SourceSHA: s.sourceSHA, Robots: []robot{entry}})
	case strings.HasPrefix(path, "robots/"):
		esn, resource, valid := bridgeRobotPath(path)
		if !valid {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if !strings.EqualFold(esn, scope) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		robots, err := s.robots()
		if err != nil {
			http.Error(w, "robot inventory is temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		if _, ok := robotByESN(robots, scope); !ok {
			http.Error(w, "robot not enrolled", http.StatusNotFound)
			return
		}
		s.handleRobotResource(w, r, scope, resource)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func bridgeRobotPath(path string) (string, string, bool) {
	parts := strings.Split(strings.TrimPrefix(path, "robots/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], strings.Join(parts[1:], "/"), true
}

func (s *Server) handleRobotResource(w http.ResponseWriter, r *http.Request, esn, resource string) {
	switch {
	case resource == "status":
		if !allowMethod(w, r, http.MethodGet) {
			return
		}
		robots, err := s.robots()
		if err != nil {
			http.Error(w, "robot inventory is temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		entry, ok := robotByESN(robots, esn)
		if !ok {
			http.Error(w, "robot not enrolled", http.StatusNotFound)
			return
		}
		s.writeJSON(w, http.StatusOK, struct {
			SourceSHA string `json:"source_sha"`
			Robot     robot  `json:"robot"`
		}{s.sourceSHA, entry})
	case resource == "observation":
		if !allowMethod(w, r, http.MethodGet) {
			return
		}
		observation, err := s.observation(esn)
		if err != nil {
			http.Error(w, "robot observation is temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		s.writeJSON(w, http.StatusOK, struct {
			SourceSHA   string                   `json:"source_sha"`
			Observation sdkapp.HermesObservation `json:"observation"`
		}{s.sourceSHA, observation})
	case resource == "camera/snapshots":
		if !allowMethod(w, r, http.MethodPost) {
			return
		}
		reference, ok := s.captureAndStore(esn)
		if !ok {
			http.Error(w, "robot camera is temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		s.writeJSON(w, http.StatusCreated, reference)
	case strings.HasPrefix(resource, "camera/snapshots/"):
		if !allowMethod(w, r, http.MethodGet) {
			return
		}
		id := strings.TrimPrefix(resource, "camera/snapshots/")
		if strings.Contains(id, "/") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		image, ok := s.cameraVault().get(esn, id)
		if !ok {
			http.Error(w, "camera snapshot not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Content-Length", strconv.Itoa(len(image)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(image)
	case resource == "commands":
		if !allowMethod(w, r, http.MethodPost) {
			return
		}
		command, err := decodeCommand(w, r)
		if err != nil {
			http.Error(w, "invalid command", http.StatusBadRequest)
			return
		}
		result, err := s.command(esn, command)
		if err != nil {
			http.Error(w, "robot control is temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		s.writeJSON(w, http.StatusAccepted, struct {
			SourceSHA string                     `json:"source_sha"`
			Result    sdkapp.HermesCommandResult `json:"result"`
		}{s.sourceSHA, result})
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func allowMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

func decodeCommand(w http.ResponseWriter, r *http.Request) (sdkapp.HermesCommand, error) {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	var command sdkapp.HermesCommand
	if err := decoder.Decode(&command); err != nil {
		return sdkapp.HermesCommand{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return sdkapp.HermesCommand{}, os.ErrInvalid
	}
	command.Action = strings.ToLower(strings.TrimSpace(command.Action))
	if err := sdkapp.ValidateHermesCommand(command); err != nil {
		return sdkapp.HermesCommand{}, os.ErrInvalid
	}
	return command, nil
}

func (s *Server) robots() ([]robot, error) {
	if s.snapshot == nil {
		return robotsFromDisk()
	}
	return s.snapshot()
}

func (s *Server) command(esn string, command sdkapp.HermesCommand) (sdkapp.HermesCommandResult, error) {
	started := time.Now()
	detail := commandActivityDetail(command)
	recordActivity("hermes_to_wirepod", esn, "robot_command", detail, "requested", 0)
	var result sdkapp.HermesCommandResult
	var err error
	if s.control != nil {
		result, err = s.control(esn, command)
	} else {
		result, err = sdkapp.HermesControl(esn, command)
	}
	if err != nil {
		recordActivity("wirepod_to_hermes", esn, "robot_command", detail, "failed", time.Since(started))
		return result, err
	}
	recordActivity("wirepod_to_hermes", esn, "robot_command", detail, "accepted", time.Since(started))
	return result, nil
}

func (s *Server) observation(esn string) (sdkapp.HermesObservation, error) {
	started := time.Now()
	recordActivity("hermes_to_wirepod", esn, "observation", "live state requested", "requested", 0)
	var observation sdkapp.HermesObservation
	var err error
	if s.observe != nil {
		observation, err = s.observe(esn)
	} else {
		observation, err = sdkapp.HermesObserve(esn)
	}
	if err != nil {
		recordActivity("wirepod_to_hermes", esn, "observation", "live state", "failed", time.Since(started))
		return observation, err
	}
	recordActivity("wirepod_to_hermes", esn, "observation", "live state", "returned", time.Since(started))
	return observation, nil
}

func (s *Server) captureSnapshot(esn string) (sdkapp.HermesSnapshot, error) {
	started := time.Now()
	recordActivity("hermes_to_wirepod", esn, "camera_snapshot", "fresh image requested", "requested", 0)
	var snapshot sdkapp.HermesSnapshot
	var err error
	if s.capture != nil {
		snapshot, err = s.capture(esn)
	} else {
		snapshot, err = sdkapp.HermesCaptureSnapshot(esn)
	}
	if err != nil {
		recordActivity("wirepod_to_hermes", esn, "camera_snapshot", "fresh image", "failed", time.Since(started))
		return snapshot, err
	}
	recordActivity("wirepod_to_hermes", esn, "camera_snapshot", "fresh image", "returned", time.Since(started))
	return snapshot, nil
}

func commandActivityDetail(command sdkapp.HermesCommand) string {
	switch command.Action {
	case "say":
		return "speech command (content withheld)"
	case "express":
		return "expression: " + command.Expression
	case "drive", "head", "lift":
		return "bounded " + command.Action
	default:
		return command.Action
	}
}

func (s *Server) authorized(r *http.Request) (string, bool) {
	const bearer = "Bearer "
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, bearer) {
		return "", false
	}
	candidate := []byte(strings.TrimPrefix(value, bearer))
	for _, credential := range s.credentials {
		if len(candidate) == len(credential.token) && subtle.ConstantTimeCompare(candidate, credential.token) == 1 {
			return credential.esn, true
		}
	}
	return "", false
}

func credentialsFromEnv(value string) ([]credential, error) {
	var tokenByESN map[string]string
	if err := json.Unmarshal([]byte(value), &tokenByESN); err != nil || len(tokenByESN) == 0 {
		return nil, os.ErrInvalid
	}
	credentials := make([]credential, 0, len(tokenByESN))
	for esn, token := range tokenByESN {
		esn, token = strings.TrimSpace(esn), strings.TrimSpace(token)
		if esn == "" || len(token) < 32 {
			return nil, os.ErrInvalid
		}
		for _, existing := range credentials {
			if strings.EqualFold(esn, existing.esn) || subtle.ConstantTimeCompare([]byte(token), existing.token) == 1 {
				return nil, os.ErrInvalid
			}
		}
		credentials = append(credentials, credential{esn: esn, token: []byte(token)})
	}
	return credentials, nil
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// robotsFromDisk avoids reading the mutable global inventory concurrently with
// Vector enrollment handlers. A writer may briefly be replacing this file, in
// which case the bridge returns 503 rather than serving a racy snapshot.
func robotsFromDisk() ([]robot, error) {
	contents, err := os.ReadFile(vars.BotInfoPath)
	if err != nil {
		return nil, err
	}
	var store struct {
		Robots []robot `json:"robots"`
	}
	if err := json.Unmarshal(contents, &store); err != nil {
		return nil, err
	}
	if store.Robots == nil {
		return []robot{}, nil
	}
	return store.Robots, nil
}

func robotByESN(robots []robot, esn string) (robot, bool) {
	for _, entry := range robots {
		if strings.EqualFold(entry.ESN, esn) {
			return entry, true
		}
	}
	return robot{}, false
}
