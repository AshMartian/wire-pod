package processreqs

import (
	"strings"
	"testing"
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
