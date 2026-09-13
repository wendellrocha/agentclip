package companion

import (
	"context"
	"errors"
	"time"

	"github.com/wendellrocha/agentclip/internal/clipboard"
)

// Watcher snapshots only new clipboard images. It keeps pixels local until
// its Arm callback stores them in the local bridge.
type Watcher struct {
	Reader   clipboard.Reader
	Interval time.Duration
	Arm      func(context.Context, clipboard.Image) error
	Log      *Logger
}

func (w Watcher) Run(ctx context.Context) error {
	if w.Reader == nil {
		return errors.New("companion clipboard reader is required")
	}
	if w.Arm == nil {
		return errors.New("companion arm callback is required")
	}
	interval := w.Interval
	if interval <= 0 {
		interval = 750 * time.Millisecond
	}
	lastHash := ""
	// Arm the initial snapshot too: a Companion that starts after the user has
	// copied an image should make that image available without a second copy.
	if image, err := clipboard.Capture(ctx, w.Reader); err == nil {
		if err := w.Arm(ctx, image); err == nil {
			w.Log.Debug("clipboard snapshot armed: kind=image size=%d", image.Size)
			lastHash = image.SHA256
		}
	} else {
		w.Log.Debug("clipboard poll skipped: %v", err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			image, err := clipboard.Capture(ctx, w.Reader)
			if err != nil || image.SHA256 == lastHash {
				continue
			}
			if err := w.Arm(ctx, image); err != nil {
				w.Log.Error("clipboard snapshot could not be armed: %v", err)
				continue
			}
			w.Log.Debug("clipboard snapshot armed: kind=image size=%d", image.Size)
			lastHash = image.SHA256
		}
	}
}
