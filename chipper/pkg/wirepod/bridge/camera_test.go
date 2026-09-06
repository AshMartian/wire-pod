package bridge

import (
	"bytes"
	"testing"
	"time"
)

func TestCameraSnapshotVaultExpiresAndBoundsFrames(t *testing.T) {
	now := time.Unix(1_000, 0)
	vault := newCameraSnapshotVault()
	vault.clock = func() time.Time { return now }
	image := []byte{0xff, 0xd8, 0xff, 0xe0, 0x01, 0xff, 0xd9}
	reference, ok := vault.add("esn-a", image)
	if !ok || reference.ID == "" || reference.ExpiresAt <= reference.Captured {
		t.Fatalf("invalid snapshot reference: %+v", reference)
	}
	fetched, ok := vault.get("esn-a", reference.ID)
	if !ok || !bytes.Equal(fetched, image) {
		t.Fatal("snapshot did not round trip")
	}
	if _, ok := vault.get("esn-b", reference.ID); ok {
		t.Fatal("snapshot crossed Vector scope")
	}
	now = now.Add(cameraSnapshotTTL)
	if _, ok := vault.get("esn-a", reference.ID); ok {
		t.Fatal("expired snapshot remained available")
	}

	now = time.Unix(2_000, 0)
	var first cameraSnapshotReference
	for i := 0; i < maxCameraSnapshots+1; i++ {
		reference, ok := vault.add("esn-a", image)
		if !ok {
			t.Fatal("snapshot was rejected")
		}
		if i == 0 {
			first = reference
		}
	}
	if _, ok := vault.get("esn-a", first.ID); ok {
		t.Fatal("vault retained more than its bounded frame count")
	}
}
