package tokenserver

import (
	"sync"
	"testing"
)

func TestTakePrimaryTokensAreAtomicAndDrainRetries(t *testing.T) {
	temporaryStoreMu.Lock()
	original := TokenHashStore
	TokenHashStore = nil
	temporaryStoreMu.Unlock()
	defer func() {
		temporaryStoreMu.Lock()
		TokenHashStore = original
		temporaryStoreMu.Unlock()
	}()
	AddPrimaryToken([3]string{"192.0.2.10", "guid-1", "hash-1"})
	AddPrimaryToken([3]string{"192.0.2.10", "guid-2", "hash-2"})
	AddPrimaryToken([3]string{"192.0.2.11", "other-guid", "other-hash"})

	var claimed [][3]string
	var claimsMu sync.Mutex
	var workers sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if tokens := TakePrimaryTokens("192.0.2.10"); len(tokens) > 0 {
				claimsMu.Lock()
				claimed = append(claimed, tokens...)
				claimsMu.Unlock()
			}
		}()
	}
	workers.Wait()
	if len(claimed) != 2 || claimed[0][1] != "guid-1" || claimed[1][1] != "guid-2" {
		t.Fatalf("got claimed primary tokens %#v, want both ordered retries", claimed)
	}
	if other := TakePrimaryTokens("192.0.2.11"); len(other) != 1 || other[0][1] != "other-guid" {
		t.Fatalf("nonmatching primary token was lost: %#v", other)
	}
}
