package bridge

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

const (
	tokenA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tokenB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestBridgeIsDisabledWithoutCredentials(t *testing.T) {
	t.Setenv("WIREPOD_HERMES_BRIDGE_TOKENS", "")
	if RegisterFromEnv(http.NewServeMux(), "test") {
		t.Fatal("bridge registered without credentials")
	}
}

func TestBridgeRequiresBearerAndListsOnlyNonSensitiveRobotFields(t *testing.T) {
	mux := http.NewServeMux()
	server := &Server{credentials: []credential{{esn: "ESN-A", token: []byte(tokenA)}}, sourceSHA: "test-sha", snapshot: func() ([]robot, error) {
		return []robot{{ESN: "ESN-A", Activated: true}}, nil
	}}
	server.Register(mux)
	request := httptest.NewRequest(http.MethodGet, "/bridge/v1/robots", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, request)
	if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("unauthenticated response: %d %q", w.Code, w.Body.String())
	}
	request.Header.Set("Authorization", "Bearer "+tokenA)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, request)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("authenticated response: %d %q", w.Code, w.Body.String())
	}
	if got := w.Body.String(); strings.Contains(got, "ip_address") || strings.Contains(got, "guid") {
		t.Fatalf("bridge exposed private robot fields: %q", got)
	}
}

func TestBridgeStatusAndMethods(t *testing.T) {
	mux := http.NewServeMux()
	server := &Server{credentials: []credential{{esn: "ESN-A", token: []byte(tokenA)}}, sourceSHA: "test-sha", snapshot: func() ([]robot, error) { return []robot{}, nil }}
	server.Register(mux)
	request := httptest.NewRequest(http.MethodPost, "/bridge/v1/robots", nil)
	request.Header.Set("Authorization", "Bearer "+tokenA)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, request)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("mutation method accepted: %d", w.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/bridge/v1/robots/unknown/status", nil)
	request.Header.Set("Authorization", "Bearer "+tokenA)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, request)
	if w.Code != http.StatusForbidden {
		t.Fatalf("out-of-scope robot response: %d", w.Code)
	}
}

func TestBridgeReturnsUnavailableForInvalidSnapshot(t *testing.T) {
	mux := http.NewServeMux()
	server := &Server{credentials: []credential{{esn: "ESN-A", token: []byte(tokenA)}}, sourceSHA: "test-sha", snapshot: func() ([]robot, error) { return nil, os.ErrInvalid }}
	server.Register(mux)
	request := httptest.NewRequest(http.MethodGet, "/bridge/v1/robots", nil)
	request.Header.Set("Authorization", "Bearer "+tokenA)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, request)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("invalid snapshot response: %d", w.Code)
	}
}

func TestCredentialsScopeEachTokenToOneRobot(t *testing.T) {
	mux := http.NewServeMux()
	server := &Server{credentials: []credential{{esn: "ESN-A", token: []byte(tokenA)}, {esn: "ESN-B", token: []byte(tokenB)}}, sourceSHA: "test-sha", snapshot: func() ([]robot, error) {
		return []robot{{ESN: "ESN-A", Activated: true}, {ESN: "ESN-B", Activated: true}}, nil
	}}
	server.Register(mux)
	request := httptest.NewRequest(http.MethodGet, "/bridge/v1/robots/ESN-B/status", nil)
	request.Header.Set("Authorization", "Bearer "+tokenA)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, request)
	if w.Code != http.StatusForbidden {
		t.Fatalf("token A accessed robot B: %d", w.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/bridge/v1/robots", nil)
	request.Header.Set("Authorization", "Bearer "+tokenA)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, request)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "ESN-B") {
		t.Fatalf("inventory escaped token scope: %d %q", w.Code, w.Body.String())
	}
}

func TestCredentialsFromEnvRejectsWeakOrDuplicateTokens(t *testing.T) {
	if _, err := credentialsFromEnv(`{"ESN-A":"short"}`); err == nil {
		t.Fatal("accepted weak credential")
	}
	if _, err := credentialsFromEnv(`{"ESN-A":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ESN-B":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`); err == nil {
		t.Fatal("accepted duplicate credential")
	}
}
