package processreqs

import (
	"strings"
	"testing"
	"time"
)

func TestSpeakHermesIntentReplyBoundsSpeech(t *testing.T) {
	longReply := strings.Repeat("a", 281)
	got := boundedHermesSpeech(longReply)
	if gotLen := len([]rune(got)); gotLen != 280 {
		t.Fatalf("bounded reply length = %d, want 280", gotLen)
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("bounded reply %q does not mark truncation", got)
	}
}

func TestHermesIntentFallbackSpeechSuppressesStaleTurns(t *testing.T) {
	started := time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC)
	if !shouldSpeakHermesIntentFallback(started, started.Add(hermesIntentFallbackSpeechMaxDelay)) {
		t.Fatal("fallback should be spoken at the bounded delay")
	}
	if shouldSpeakHermesIntentFallback(started, started.Add(hermesIntentFallbackSpeechMaxDelay+time.Nanosecond)) {
		t.Fatal("fallback should be silent once a voice turn is stale")
	}
	if shouldSpeakHermesIntentFallback(time.Time{}, started) {
		t.Fatal("zero request start must not produce a fallback")
	}
}
