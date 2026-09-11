package bridge

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/kercre123/wire-pod/chipper/pkg/wirepod/sdkapp"
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

func TestBridgeCommandIsScopedAndPassesOnlyDecodedAction(t *testing.T) {
	mux := http.NewServeMux()
	called := false
	server := &Server{credentials: []credential{{esn: "ESN-A", token: []byte(tokenA)}}, sourceSHA: "test-sha", snapshot: func() ([]robot, error) {
		return []robot{{ESN: "ESN-A", Activated: true}}, nil
	}, control: func(esn string, command sdkapp.HermesCommand) (sdkapp.HermesCommandResult, error) {
		called = esn == "ESN-A" && command.Action == "say" && command.Text == "hello"
		return sdkapp.HermesCommandResult{Action: command.Action}, nil
	}}
	server.Register(mux)
	request := httptest.NewRequest(http.MethodPost, "/bridge/v1/robots/ESN-A/commands", strings.NewReader(`{"action":"say","text":"hello"}`))
	request.Header.Set("Authorization", "Bearer "+tokenA)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, request)
	if w.Code != http.StatusAccepted || !called {
		t.Fatalf("command was not dispatched safely: %d %q", w.Code, w.Body.String())
	}
	var body struct {
		Result sdkapp.HermesCommandResult `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Result.Action != "say" {
		t.Fatalf("bad command response: %v %q", err, w.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/bridge/v1/robots/ESN-B/commands", strings.NewReader(`{"action":"stop"}`))
	request.Header.Set("Authorization", "Bearer "+tokenA)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, request)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-scope command accepted: %d", w.Code)
	}
}

func TestBridgeExpressionIsScopedToTheCuratedCatalog(t *testing.T) {
	mux := http.NewServeMux()
	called := false
	server := &Server{credentials: []credential{{esn: "ESN-A", token: []byte(tokenA)}}, sourceSHA: "test-sha", snapshot: func() ([]robot, error) {
		return []robot{{ESN: "ESN-A", Activated: true}}, nil
	}, control: func(esn string, command sdkapp.HermesCommand) (sdkapp.HermesCommandResult, error) {
		called = esn == "ESN-A" && command.Action == "express" && command.Expression == "happy"
		return sdkapp.HermesCommandResult{Action: command.Action, Expression: command.Expression}, nil
	}}
	server.Register(mux)
	request := httptest.NewRequest(http.MethodPost, "/bridge/v1/robots/ESN-A/commands", strings.NewReader(`{"action":"express","expression":"happy"}`))
	request.Header.Set("Authorization", "Bearer "+tokenA)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, request)
	if w.Code != http.StatusAccepted || !called || !strings.Contains(w.Body.String(), `"expression":"happy"`) {
		t.Fatalf("curated expression was not dispatched: %d %q", w.Code, w.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/bridge/v1/robots/ESN-A/commands", strings.NewReader(`{"action":"express","expression":"anim_arbitrary_01"}`))
	request.Header.Set("Authorization", "Bearer "+tokenA)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, request)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("arbitrary firmware animation was accepted: %d %q", w.Code, w.Body.String())
	}
}

func TestBridgeSnapshotIsScopedAndNeverSerializedAsJSON(t *testing.T) {
	mux := http.NewServeMux()
	called := false
	image := []byte{0xff, 0xd8, 0xff, 0xe0, 0x01, 0x02, 0xff, 0xd9}
	server := &Server{credentials: []credential{{esn: "ESN-A", token: []byte(tokenA)}}, snapshot: func() ([]robot, error) {
		return []robot{{ESN: "ESN-A", Activated: true}, {ESN: "ESN-B", Activated: true}}, nil
	}, capture: func(esn string) (sdkapp.HermesSnapshot, error) {
		called = esn == "ESN-A"
		return sdkapp.HermesSnapshot{JPEG: image}, nil
	}, snapshots: newCameraSnapshotVault()}
	server.Register(mux)
	request := httptest.NewRequest(http.MethodPost, "/bridge/v1/robots/ESN-A/camera/snapshots", nil)
	request.Header.Set("Authorization", "Bearer "+tokenA)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, request)
	if w.Code != http.StatusCreated || !called || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("unexpected snapshot creation response: %d %q", w.Code, w.Body.String())
	}
	var reference cameraSnapshotReference
	if err := json.Unmarshal(w.Body.Bytes(), &reference); err != nil || reference.ID == "" {
		t.Fatalf("invalid snapshot reference: %v %q", err, w.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/bridge/v1/robots/ESN-A/camera/snapshots/"+reference.ID, nil)
	request.Header.Set("Authorization", "Bearer "+tokenA)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, request)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/jpeg" || !bytes.Equal(w.Body.Bytes(), image) {
		t.Fatalf("unexpected snapshot image response: %d %q", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "data:") || strings.Contains(w.Body.String(), "base64") {
		t.Fatalf("snapshot response was encoded into a text payload: %q", w.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/bridge/v1/robots/ESN-B/camera/snapshots/"+reference.ID, nil)
	request.Header.Set("Authorization", "Bearer "+tokenA)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, request)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-scope snapshot accepted: %d", w.Code)
	}
}
