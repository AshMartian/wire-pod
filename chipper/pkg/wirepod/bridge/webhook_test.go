package bridge

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/kercre123/wire-pod/chipper/pkg/wirepod/sdkapp"
)

func TestWebhookTargetsRejectInvalidConfig(t *testing.T) {
	if _, err := webhookTargetsFromEnv(`{"ESN-A":{"url":"file:///tmp/no","secret":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`); err == nil {
		t.Fatal("accepted non-HTTP webhook URL")
	}
	if _, err := webhookTargetsFromEnv(`{"ESN-A":{"url":"http://localhost:8644/webhooks/vector","secret":"short"}}`); err == nil {
		t.Fatal("accepted weak webhook secret")
	}
}

func TestWebhookForwarderSignsReplayResistantPayload(t *testing.T) {
	const secret = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var once sync.Once
	received := make(chan bool, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		timestamp := r.Header.Get("X-Webhook-Timestamp")
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(timestamp + "."))
		_, _ = mac.Write(body)
		expected := hex.EncodeToString(mac.Sum(nil))
		validTime := false
		if seconds, err := strconv.ParseInt(timestamp, 10, 64); err == nil && seconds > 0 {
			validTime = true
		}
		once.Do(func() {
			received <- validTime && hmac.Equal([]byte(expected), []byte(r.Header.Get("X-Webhook-Signature-V2"))) && r.Header.Get("X-GitHub-Event") == "wirepod.face_recognized"
		})
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	forwarder := newWebhookForwarder(webhookTarget{URL: server.URL, Secret: secret}, nil)
	forwarder.enqueue(sdkapp.HermesRobotEvent{Type: "wirepod.face_recognized", ESN: "ESN-A", FaceID: 42, Name: "Ash", ObservedAt: 1})
	select {
	case ok := <-received:
		if !ok {
			t.Fatal("webhook did not carry a valid V2 signature")
		}
	case <-time.After(time.Second):
		t.Fatal("webhook was not delivered")
	}
}
