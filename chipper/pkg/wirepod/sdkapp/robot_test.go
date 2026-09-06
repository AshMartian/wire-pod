package sdkapp

import "testing"

func TestRobotIndexLockedTracksESNAfterSliceCompaction(t *testing.T) {
	robotsMu.Lock()
	previous := robots
	robots = []Robot{{ESN: "00603f9b"}, {ESN: "004047ef"}}
	defer func() {
		robots = previous
		robotsMu.Unlock()
	}()

	if got := robotIndexLocked("004047EF"); got != 1 {
		t.Fatalf("robot index = %d, want 1", got)
	}
	robots = robots[:1]
	if got := robotIndexLocked("004047ef"); got != -1 {
		t.Fatalf("removed robot index = %d, want -1", got)
	}
}
