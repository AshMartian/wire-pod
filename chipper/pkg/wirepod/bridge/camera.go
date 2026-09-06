package bridge

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/wirepod/sdkapp"
)

const (
	cameraSnapshotTTL   = time.Minute
	maxCameraSnapshots  = 4
	maxCameraImageBytes = 8 * 1024 * 1024
)

type cameraSnapshot struct {
	id        string
	image     []byte
	captured  time.Time
	expiresAt time.Time
}

type cameraSnapshotReference struct {
	ID        string `json:"snapshot_id"`
	Captured  int64  `json:"captured_at_unix_ms"`
	ExpiresAt int64  `json:"expires_at_unix_ms"`
}

// cameraSnapshotVault is intentionally process-local. Camera frames are never
// persisted by WirePod; the small TTL gives the signed event webhook enough
// time to ask its own profile-scoped tool for the exact captured frame.
type cameraSnapshotVault struct {
	mu    sync.Mutex
	byESN map[string][]cameraSnapshot
	clock func() time.Time
}

func newCameraSnapshotVault() *cameraSnapshotVault {
	return &cameraSnapshotVault{byESN: make(map[string][]cameraSnapshot), clock: time.Now}
}

func (v *cameraSnapshotVault) add(esn string, image []byte) (cameraSnapshotReference, bool) {
	if len(image) < 4 || len(image) > maxCameraImageBytes || image[0] != 0xff || image[1] != 0xd8 || image[2] != 0xff {
		return cameraSnapshotReference{}, false
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return cameraSnapshotReference{}, false
	}
	now := v.clock()
	snapshot := cameraSnapshot{id: hex.EncodeToString(idBytes), image: append([]byte(nil), image...), captured: now, expiresAt: now.Add(cameraSnapshotTTL)}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.pruneLocked(esn, now)
	entries := append(v.byESN[esn], snapshot)
	if len(entries) > maxCameraSnapshots {
		entries = entries[len(entries)-maxCameraSnapshots:]
	}
	v.byESN[esn] = entries
	return cameraSnapshotReference{ID: snapshot.id, Captured: snapshot.captured.UnixMilli(), ExpiresAt: snapshot.expiresAt.UnixMilli()}, true
}

func (v *cameraSnapshotVault) get(esn, id string) ([]byte, bool) {
	if len(id) != 32 {
		return nil, false
	}
	now := v.clock()
	v.mu.Lock()
	defer v.mu.Unlock()
	v.pruneLocked(esn, now)
	for _, snapshot := range v.byESN[esn] {
		if snapshot.id == id {
			return append([]byte(nil), snapshot.image...), true
		}
	}
	return nil, false
}

func (v *cameraSnapshotVault) pruneLocked(esn string, now time.Time) {
	entries := v.byESN[esn]
	kept := entries[:0]
	for _, snapshot := range entries {
		if now.Before(snapshot.expiresAt) {
			kept = append(kept, snapshot)
		}
	}
	if len(kept) == 0 {
		delete(v.byESN, esn)
		return
	}
	v.byESN[esn] = kept
}

func (s *Server) captureAndStore(esn string) (cameraSnapshotReference, bool) {
	snapshot, err := s.captureSnapshot(esn)
	if err != nil {
		logger.Println("Hermes camera capture failed:", err)
		return cameraSnapshotReference{}, false
	}
	reference, ok := s.cameraVault().add(esn, snapshot.JPEG)
	if !ok {
		logger.Println("Hermes camera capture rejected an invalid image")
	}
	return reference, ok
}

func (s *Server) cameraVault() *cameraSnapshotVault {
	if s.snapshots == nil {
		s.snapshots = newCameraSnapshotVault()
	}
	return s.snapshots
}

func (s *Server) attachEventSnapshot(event sdkapp.HermesRobotEvent) sdkapp.HermesRobotEvent {
	if event.Type != "wirepod.face_observed" && event.Type != "wirepod.face_recognized" && event.Type != "wirepod.edge_detected" {
		return event
	}
	reference, ok := s.captureAndStore(event.ESN)
	if !ok {
		return event
	}
	event.SnapshotID = reference.ID
	event.CapturedAt = reference.Captured
	event.SnapshotUntil = reference.ExpiresAt
	return event
}
