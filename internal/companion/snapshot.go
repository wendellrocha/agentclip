package companion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wendellrocha/agentclip/internal/bridge"
	"github.com/wendellrocha/agentclip/internal/clipboard"
)

// SnapshotSource separates cheap change detection from the potentially costly
// hashing needed to arm a file. This prevents hashing a 50 MiB CSV every poll.
type SnapshotSource interface {
	Fingerprint(context.Context) (string, error)
	ReadSnapshot(context.Context) ([]bridge.Item, error)
}

type HostSnapshotSource struct {
	ImageReader clipboard.Reader
	FileReader  func(context.Context) ([]string, error)
}

func (s HostSnapshotSource) Fingerprint(ctx context.Context) (string, error) {
	paths, err := s.filePaths(ctx)
	if err == nil && len(paths) > 0 {
		return fingerprintPaths(paths)
	}
	if err != nil && !errors.Is(err, clipboard.ErrNoFiles) {
		return "", err
	}
	if image, err := clipboard.Capture(ctx, s.imageReader()); err == nil {
		return "image:" + image.SHA256, nil
	}
	text, err := clipboard.Text(ctx)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(text)
	return "text:" + hex.EncodeToString(sum[:]), nil
}

func (s HostSnapshotSource) ReadSnapshot(ctx context.Context) ([]bridge.Item, error) {
	paths, err := s.filePaths(ctx)
	if err == nil && len(paths) > 0 {
		items := make([]bridge.Item, 0, len(paths))
		for _, path := range paths {
			item, err := bridge.FileItem(path, mime.TypeByExtension(strings.ToLower(filepath.Ext(path))))
			if err != nil {
				return nil, fmt.Errorf("prepare clipboard file %q: %w", filepath.Base(path), err)
			}
			items = append(items, item)
		}
		return items, nil
	}
	if err != nil && !errors.Is(err, clipboard.ErrNoFiles) {
		return nil, err
	}
	if image, err := clipboard.Capture(ctx, s.imageReader()); err == nil {
		return []bridge.Item{{Kind: bridge.ItemImage, MIMEType: "image/png", Data: image.PNG, Width: image.Width, Height: image.Height}}, nil
	}
	text, err := clipboard.Text(ctx)
	if err != nil {
		return nil, err
	}
	return []bridge.Item{{Kind: bridge.ItemText, MIMEType: "text/plain; charset=utf-8", Data: text}}, nil
}

func (s HostSnapshotSource) imageReader() clipboard.Reader {
	if s.ImageReader != nil {
		return s.ImageReader
	}
	return clipboard.NativeReader{}
}

func (s HostSnapshotSource) filePaths(ctx context.Context) ([]string, error) {
	if s.FileReader != nil {
		return s.FileReader(ctx)
	}
	return clipboard.FilePaths(ctx)
}

func fingerprintPaths(paths []string) (string, error) {
	parts := make([]string, 0, len(paths))
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return "", bridge.ErrInvalidFile
		}
		parts = append(parts, filepath.Clean(path)+":"+fmt.Sprint(info.Size())+":"+fmt.Sprint(info.ModTime().UnixNano()))
	}
	sort.Strings(parts)
	return "files:" + strings.Join(parts, "|"), nil
}

type SnapshotWatcher struct {
	Source   SnapshotSource
	Interval time.Duration
	Arm      func(context.Context, []bridge.Item) error
}

func (w SnapshotWatcher) Run(ctx context.Context) error {
	if w.Source == nil {
		return errors.New("companion snapshot source is required")
	}
	if w.Arm == nil {
		return errors.New("companion snapshot arm callback is required")
	}
	interval := w.Interval
	if interval <= 0 {
		interval = 750 * time.Millisecond
	}
	last := ""
	arm := func(fingerprint string) {
		items, err := w.Source.ReadSnapshot(ctx)
		if err != nil || len(items) == 0 {
			return
		}
		if w.Arm(ctx, items) == nil {
			last = fingerprint
		}
	}
	if fingerprint, err := w.Source.Fingerprint(ctx); err == nil {
		arm(fingerprint)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			fingerprint, err := w.Source.Fingerprint(ctx)
			if err != nil || fingerprint == last {
				continue
			}
			arm(fingerprint)
		}
	}
}
