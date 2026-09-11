package bridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kercre123/wire-pod/chipper/pkg/wirepod/sdkapp"
)

func isolatedActivityJournal(t *testing.T) *activityJournal {
	t.Helper()
	original := hermesActivity
	journal := &activityJournal{}
	hermesActivity = journal
	t.Cleanup(func() { hermesActivity = original })
	return journal
}

func TestActivityJournalReturnsBoundedNewestFirst(t *testing.T) {
	journal := &activityJournal{}
	journal.record("wirepod_to_hermes", "ESN-A", "voice_turn", "metadata only", "requested", 0)
	journal.record("hermes_to_wirepod", "ESN-A", "voice_reply", "metadata only", "received", 125*time.Millisecond)
	entries := journal.snapshot(1)
	if len(entries) != 1 || entries[0].Kind != "voice_reply" || entries[0].DurationMS != 125 {
		t.Fatalf("unexpected newest activity entry: %#v", entries)
	}
}

func TestActivityFeedIsNoStoreAndRejectsWrites(t *testing.T) {
	journal := isolatedActivityJournal(t)
	journal.record("wirepod_to_hermes", "ESN-A", "voice_turn", "transcript forwarded (content withheld)", "requested", 0)
	request := httptest.NewRequest(http.MethodGet, "/api-sdk/hermes_activity?limit=1", nil)
	response := httptest.NewRecorder()
	handleActivityFeed(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("unexpected activity response: %d %#v", response.Code, response.Header())
	}
	var body struct {
		Entries []ActivityEntry `json:"entries"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || len(body.Entries) != 1 || body.Entries[0].ESN != "esn-a" {
		t.Fatalf("invalid activity JSON: %v %q", err, response.Body.String())
	}
	response = httptest.NewRecorder()
	handleActivityFeed(response, httptest.NewRequest(http.MethodPost, "/api-sdk/hermes_activity", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("activity feed accepted a write: %d", response.Code)
	}
}

func TestBridgeSpeechActivityWithholdsSpeechContent(t *testing.T) {
	journal := isolatedActivityJournal(t)
	server := &Server{control: func(esn string, command sdkapp.HermesCommand) (sdkapp.HermesCommandResult, error) {
		return sdkapp.HermesCommandResult{Action: command.Action}, nil
	}}
	if _, err := server.command("ESN-A", sdkapp.HermesCommand{Action: "say", Text: "secret private sentence"}); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(journal.snapshot(10))
	if err != nil || strings.Contains(string(encoded), "secret private sentence") || !strings.Contains(string(encoded), "content withheld") {
		t.Fatalf("speech content escaped activity journal: %v %s", err, encoded)
	}
}
