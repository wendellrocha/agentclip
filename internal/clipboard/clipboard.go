// Package clipboard provides the image-capture boundary used by AgentClip.
package clipboard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image/png"

	native "golang.design/x/clipboard"
)

const (
	// MaxBytes is the maximum encoded image size accepted by the MVP.
	MaxBytes = 12 * 1024 * 1024
	// MaxDimension is the maximum width or height accepted by the MVP.
	MaxDimension = 4096
)

var (
	ErrNoImage       = errors.New("clipboard contains no image")
	ErrNoFiles       = errors.New("clipboard contains no file references")
	ErrNoText        = errors.New("clipboard contains no text")
	ErrImageTooLarge = errors.New("clipboard image exceeds size limit")
	ErrImageTooWide  = errors.New("clipboard image exceeds dimension limit")
	ErrInvalidPNG    = errors.New("clipboard image is not valid PNG")
)

// Reader abstracts the native clipboard, allowing protocol code and tests to
// remain independent of a graphical desktop session.
type Reader interface {
	ReadImage(context.Context) ([]byte, error)
}

// NativeReader reads the image format exposed by the host clipboard.
type NativeReader struct{}

func (NativeReader) ReadImage(ctx context.Context) ([]byte, error) {
	if err := native.Init(); err != nil {
		return nil, fmt.Errorf("initialize clipboard: %w", err)
	}
	b, err := native.Read(ctx, native.FmtImage)
	if err != nil {
		return nil, fmt.Errorf("read clipboard image: %w", err)
	}
	if len(b) == 0 {
		return nil, ErrNoImage
	}
	return b, nil
}

// FilePaths reads file-manager references from the native clipboard. The
// underlying library maps Finder, Explorer, and URI-list formats to paths.
func FilePaths(ctx context.Context) ([]string, error) {
	if err := native.Init(); err != nil {
		return nil, fmt.Errorf("initialize clipboard: %w", err)
	}
	paths, err := native.ReadFiles(ctx)
	if errors.Is(err, native.ErrNoData) || len(paths) == 0 {
		return nil, ErrNoFiles
	}
	if err != nil {
		return nil, fmt.Errorf("read clipboard files: %w", err)
	}
	return paths, nil
}

// Text reads plain UTF-8 text from the native clipboard. Size policy belongs
// to the bridge so callers can use the same source for generic snapshots.
func Text(ctx context.Context) ([]byte, error) {
	if err := native.Init(); err != nil {
		return nil, fmt.Errorf("initialize clipboard: %w", err)
	}
	data, err := native.Read(ctx, native.FmtText)
	if errors.Is(err, native.ErrNoData) || len(data) == 0 {
		return nil, ErrNoText
	}
	if err != nil {
		return nil, fmt.Errorf("read clipboard text: %w", err)
	}
	return append([]byte(nil), data...), nil
}

// Image contains the validated PNG and metadata used by the bridge.
type Image struct {
	PNG    []byte
	Width  int
	Height int
	Size   int
	SHA256 string
}

// Capture reads and validates one image from r. The returned bytes are not
// retained by the Reader and are safe for the caller to own.
func Capture(ctx context.Context, r Reader) (Image, error) {
	if r == nil {
		return Image{}, errors.New("clipboard reader is nil")
	}
	b, err := r.ReadImage(ctx)
	if err != nil {
		return Image{}, err
	}
	return Validate(b)
}

// Validate checks encoded size, PNG syntax, and dimensions, then computes a
// stable SHA-256 hex digest for diagnostics and deduplication.
func Validate(b []byte) (Image, error) {
	if len(b) == 0 {
		return Image{}, ErrNoImage
	}
	if len(b) > MaxBytes {
		return Image{}, fmt.Errorf("%w: %d bytes (maximum %d)", ErrImageTooLarge, len(b), MaxBytes)
	}
	config, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return Image{}, fmt.Errorf("%w: %v", ErrInvalidPNG, err)
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > MaxDimension || config.Height > MaxDimension {
		return Image{}, fmt.Errorf("%w: %dx%d (maximum %d)", ErrImageTooWide, config.Width, config.Height, MaxDimension)
	}
	hash := sha256.Sum256(b)
	return Image{PNG: append([]byte(nil), b...), Width: config.Width, Height: config.Height, Size: len(b), SHA256: hex.EncodeToString(hash[:])}, nil
}
