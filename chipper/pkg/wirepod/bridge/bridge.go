// Package bridge exposes a small, authenticated contract for external agent
// runtimes. It deliberately starts read-only; robot motion remains outside of
// this API until one shared control manager exists.
package bridge

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

const prefix = "/bridge/v1/"

type Server struct {
	sourceSHA   string
	snapshot    func() ([]robot, error)
	credentials []credential
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
	(&Server{credentials: credentials, sourceSHA: sourceSHA, snapshot: robotsFromDisk}).Register(mux)
	return true
}

func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc(prefix, s.handle)
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	scope, ok := s.authorized(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer realm="wire-pod bridge"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, prefix)
	switch {
	case path == "robots":
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
	case strings.HasPrefix(path, "robots/") && strings.HasSuffix(path, "/status"):
		esn := strings.TrimSuffix(strings.TrimPrefix(path, "robots/"), "/status")
		if esn == "" || strings.Contains(esn, "/") {
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
		if entry, ok := robotByESN(robots, scope); ok {
			s.writeJSON(w, http.StatusOK, struct {
				SourceSHA string `json:"source_sha"`
				Robot     robot  `json:"robot"`
			}{s.sourceSHA, entry})
			return
		}
		http.Error(w, "robot not enrolled", http.StatusNotFound)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (s *Server) robots() ([]robot, error) {
	if s.snapshot == nil {
		return robotsFromDisk()
	}
	return s.snapshot()
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
