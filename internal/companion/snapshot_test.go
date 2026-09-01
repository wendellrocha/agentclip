package companion

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/wendellrocha/agentclip/internal/bridge"
)

func TestSnapshotWatcherArmsOnlyWhenFingerprintChanges(t *testing.T) {
	source := &fakeSnapshotSource{fingerprints: []string{"one", "one", "two"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var armed int
	watcher := SnapshotWatcher{
		Source:   source,
		Interval: time.Millisecond,
		Arm: func(_ context.Context, _ []bridge.Item) error {
			mu.Lock()
			armed++
			stop := armed == 2
			mu.Unlock()
			if stop {
				cancel()
			}
			return nil
		},
	}
	if err := watcher.Run(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if armed != 2 || source.reads != 2 {
		t.Fatalf("armed=%d snapshot reads=%d", armed, source.reads)
	}
}

type fakeSnapshotSource struct {
	fingerprints []string
	index        int
	reads        int
}

func (s *fakeSnapshotSource) Fingerprint(context.Context) (string, error) {
	if s.index >= len(s.fingerprints) {
		return s.fingerprints[len(s.fingerprints)-1], nil
	}
	value := s.fingerprints[s.index]
	s.index++
	return value, nil
}

func (s *fakeSnapshotSource) ReadSnapshot(context.Context) ([]bridge.Item, error) {
	s.reads++
	return []bridge.Item{{Kind: bridge.ItemText, Data: []byte("fixture")}}, nil
}
