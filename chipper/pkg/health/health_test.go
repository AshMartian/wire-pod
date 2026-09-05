package health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadinessLifecycle(t *testing.T) {
	state := New("test-sha")
	mux := http.NewServeMux()
	state.Register(mux)
	check := func(path, status string, code int) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		var got Response
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if w.Code != code || got.Status != status || got.SourceSHA != "test-sha" {
			t.Fatalf("%s: code=%d response=%+v", path, w.Code, got)
		}
	}
	check("/health/live", "live", 200)
	check("/health/ready", "setup_required", 503)
	state.Speech(true, false)
	check("/health/ready", "speech_unavailable", 503)
	state.Speech(true, true)
	check("/health/ready", "starting", 503)
	generation := state.BeginListener()
	state.Listener(generation, true)
	check("/health/ready", "ready", 200)
	state.Listener(generation, false)
	check("/health/ready", "starting", 503)
	check("/health/live", "live", 200)
	state.Listener(generation, true)
	state.Speech(true, false)
	check("/health/ready", "speech_unavailable", 503)
}

func TestRetiringListenerCannotClearNewReadiness(t *testing.T) {
	state := New("test-sha")
	state.Speech(true, true)
	old := state.BeginListener()
	state.Listener(old, true)
	current := state.BeginListener()
	state.Listener(current, true)
	state.Listener(old, false)
	w := httptest.NewRecorder()
	state.Ready(w, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if w.Code != http.StatusOK {
		t.Fatal("old listener cleared replacement readiness")
	}
	state.Listener(current, false)
	w = httptest.NewRecorder()
	state.Ready(w, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatal("failed current listener still reported ready")
	}
}

func TestMethods(t *testing.T) {
	state := New("test-sha")
	for _, method := range []string{http.MethodHead, http.MethodPost} {
		w := httptest.NewRecorder()
		state.Live(w, httptest.NewRequest(method, "/health/live", nil))
		if w.Body.Len() != 0 {
			t.Fatal("unexpected body")
		}
		if method == http.MethodPost && (w.Code != 405 || w.Header().Get("Allow") != "GET, HEAD") {
			t.Fatal("mutation method accepted")
		}
	}
}
