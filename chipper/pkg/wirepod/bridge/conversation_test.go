package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHermesConversationUsesScopedDailySessionAndMemoryKey(t *testing.T) {
	const key = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+key {
			http.Error(w, "bad request", http.StatusForbidden)
			return
		}
		if !strings.HasPrefix(r.Header.Get("X-Hermes-Session-Id"), "wirepod-esn-a-") || r.Header.Get("X-Hermes-Session-Key") != "wirepod:vector:esn-a" {
			http.Error(w, "bad session", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Hello from your own daily session."}}]}`))
	}))
	defer server.Close()
	t.Setenv(hermesConversationEnv, `{"ESN-A":{"url":"`+server.URL+`","key":"`+key+`","model":"vector-n8a4"}}`)
	if !HermesConversationEnabled("esn-a") || HermesConversationEnabled("esn-b") {
		t.Fatal("conversation target scope was not respected")
	}
	answer, configured, err := HermesConversation(context.Background(), "ESN-A", "hello")
	if err != nil || !configured || answer != "Hello from your own daily session." {
		t.Fatalf("conversation failed: configured=%v err=%v answer=%q", configured, err, answer)
	}
}

func TestHermesConversationStreamEmitsBoundedSentenceChunks(t *testing.T) {
	const key = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Stream       bool `json:"stream"`
			ModelOptions struct {
				Reasoning conversationReasoning `json:"reasoning"`
			} `json:"model_options"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || !request.Stream || request.ModelOptions.Reasoning != (conversationReasoning{Enabled: false}) {
			http.Error(w, "streaming was not requested", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"Hello "}}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"from Vector. How"}}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":" are you?"}}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()
	t.Setenv(hermesConversationEnv, `{"esn-a":{"url":"`+server.URL+`","key":"`+key+`","model":"vector-n8a4"}}`)
	var chunks []string
	answer, configured, streamed, err := HermesConversationStream(context.Background(), "esn-a", "hello", func(chunk string) error {
		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil || !configured || !streamed {
		t.Fatalf("stream failed: configured=%v streamed=%v err=%v", configured, streamed, err)
	}
	if answer != "Hello from Vector. How are you?" {
		t.Fatalf("unexpected answer: %q", answer)
	}
	if got, want := strings.Join(chunks, "|"), "Hello from Vector.|How are you?"; got != want {
		t.Fatalf("unexpected chunks: got %q, want %q", got, want)
	}
	for _, chunk := range chunks {
		if len([]rune(chunk)) > 280 {
			t.Fatalf("chunk exceeds Vector speech limit: %d", len([]rune(chunk)))
		}
	}
}

func TestHermesSentenceChunkerBoundsUnpunctuatedOutput(t *testing.T) {
	chunker := newHermesSentenceChunker()
	text := strings.Repeat("x", 281)
	chunks := chunker.push(text)
	chunks = append(chunks, chunker.finish()...)
	if got, want := len(chunks), 2; got != want {
		t.Fatalf("unexpected chunk count: got %d, want %d", got, want)
	}
	if got, want := len([]rune(chunks[0])), 280; got != want {
		t.Fatalf("first chunk length: got %d, want %d", got, want)
	}
	if got, want := chunks[1], "x"; got != want {
		t.Fatalf("second chunk: got %q, want %q", got, want)
	}
}

func TestConversationTargetRejectsWeakKeyAndNonHTTPURL(t *testing.T) {
	if _, err := conversationTargetsFromEnv(`{"ESN-A":{"url":"file:///tmp/no","key":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","model":"x"}}`); err == nil {
		t.Fatal("accepted a non-HTTP conversation endpoint")
	}
	if _, err := conversationTargetsFromEnv(`{"ESN-A":{"url":"http://localhost","key":"short","model":"x"}}`); err == nil {
		t.Fatal("accepted a weak conversation key")
	}
}

func TestDailySessionIDFollowsFourAMResetBoundary(t *testing.T) {
	zone := time.FixedZone("test", -7*60*60)
	beforeReset := time.Date(2026, 9, 6, 3, 59, 0, 0, zone)
	atReset := time.Date(2026, 9, 6, 4, 0, 0, 0, zone)
	if got, want := dailySessionID("esn-a", beforeReset), "wirepod-esn-a-2026-09-05"; got != want {
		t.Fatalf("before reset: got %q, want %q", got, want)
	}
	if got, want := dailySessionID("esn-a", atReset), "wirepod-esn-a-2026-09-06"; got != want {
		t.Fatalf("at reset: got %q, want %q", got, want)
	}
}
