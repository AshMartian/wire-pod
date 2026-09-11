package bridge

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const activityCapacity = 160

// ActivityEntry is a privacy-preserving audit item for the operator-facing
// Hermes Fleet console. It intentionally has no transcript, speech text, face
// name/ID, camera data, URL, credential, or webhook payload fields.
type ActivityEntry struct {
	ID         uint64    `json:"id"`
	ObservedAt time.Time `json:"observed_at"`
	Direction  string    `json:"direction"`
	ESN        string    `json:"esn"`
	Kind       string    `json:"kind"`
	Detail     string    `json:"detail"`
	Outcome    string    `json:"outcome"`
	DurationMS int64     `json:"duration_ms,omitempty"`
}

type activityJournal struct {
	sync.Mutex
	next    uint64
	entries []ActivityEntry
}

var hermesActivity = &activityJournal{}

func (journal *activityJournal) record(direction, esn, kind, detail, outcome string, duration time.Duration) {
	entry := ActivityEntry{
		ObservedAt: time.Now().UTC(),
		Direction:  strings.TrimSpace(direction),
		ESN:        strings.ToLower(strings.TrimSpace(esn)),
		Kind:       strings.TrimSpace(kind),
		Detail:     strings.TrimSpace(detail),
		Outcome:    strings.TrimSpace(outcome),
	}
	if duration > 0 {
		entry.DurationMS = duration.Milliseconds()
	}
	journal.Lock()
	journal.next++
	entry.ID = journal.next
	journal.entries = append(journal.entries, entry)
	if len(journal.entries) > activityCapacity {
		journal.entries = append([]ActivityEntry(nil), journal.entries[len(journal.entries)-activityCapacity:]...)
	}
	journal.Unlock()
}

func (journal *activityJournal) snapshot(limit int) []ActivityEntry {
	if limit < 1 {
		limit = 1
	}
	if limit > activityCapacity {
		limit = activityCapacity
	}
	journal.Lock()
	defer journal.Unlock()
	if len(journal.entries) == 0 {
		return []ActivityEntry{}
	}
	start := len(journal.entries) - limit
	if start < 0 {
		start = 0
	}
	entries := append([]ActivityEntry(nil), journal.entries[start:]...)
	// Present newest first, which makes a short operator glance useful.
	for left, right := 0, len(entries)-1; left < right; left, right = left+1, right-1 {
		entries[left], entries[right] = entries[right], entries[left]
	}
	return entries
}

func recordActivity(direction, esn, kind, detail, outcome string, duration time.Duration) {
	hermesActivity.record(direction, esn, kind, detail, outcome, duration)
}

// RegisterActivityFeed installs the local operator console's metadata-only
// timeline. It intentionally works even when no profile bridge token has been
// configured yet, so setup and connection failures remain visible.
func RegisterActivityFeed(mux *http.ServeMux) {
	mux.HandleFunc("/api-sdk/hermes_activity", handleActivityFeed)
}

func handleActivityFeed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	limit := 60
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > activityCapacity {
			http.Error(w, "invalid activity limit", http.StatusBadRequest)
			return
		}
		limit = parsed
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Entries []ActivityEntry `json:"entries"`
	}{Entries: hermesActivity.snapshot(limit)})
}
