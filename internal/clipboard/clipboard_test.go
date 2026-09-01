package clipboard

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"testing"
)

type fakeReader struct {
	data []byte
	err  error
}

func (f fakeReader) ReadImage(context.Context) ([]byte, error) { return f.data, f.err }

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestCaptureMetadataAndCopy(t *testing.T) {
	b := pngBytes(t, 3, 2)
	got, err := Capture(context.Background(), fakeReader{data: b})
	if err != nil {
		t.Fatal(err)
	}
	if got.Width != 3 || got.Height != 2 || got.Size != len(b) || got.SHA256 == "" {
		t.Fatalf("bad metadata: %+v", got)
	}
	b[0] = 0
	if got.PNG[0] == 0 {
		t.Fatal("result aliases reader buffer")
	}
}

func TestValidateErrors(t *testing.T) {
	if !errors.Is(mustErr(func() (Image, error) { return Validate(nil) }), ErrNoImage) {
		t.Error("empty image error")
	}
	if !errors.Is(mustErr(func() (Image, error) { return Validate([]byte("bad")) }), ErrInvalidPNG) {
		t.Error("invalid PNG error")
	}
	if !errors.Is(mustErr(func() (Image, error) { return Validate(pngBytes(t, MaxDimension+1, 1)) }), ErrImageTooWide) {
		t.Error("dimension error")
	}
	if !errors.Is(mustErr(func() (Image, error) { return Validate(bytes.Repeat([]byte{1}, MaxBytes+1)) }), ErrImageTooLarge) {
		t.Error("size error")
	}
}

func mustErr(fn func() (Image, error)) error { _, err := fn(); return err }

func TestCapturePropagatesReaderError(t *testing.T) {
	want := errors.New("unavailable")
	_, err := Capture(context.Background(), fakeReader{err: want})
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
}
