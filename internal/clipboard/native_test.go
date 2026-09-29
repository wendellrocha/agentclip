package clipboard

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// These tests exercise the real system clipboard. They are opt-in because they
// OVERWRITE whatever is on the clipboard: set AGENTCLIP_NATIVE_CLIPBOARD_TEST=1
// on a machine (or CI job) where that is acceptable. On Linux they need an X
// server, for example `xvfb-run -a go test ./internal/clipboard`, plus xclip.
//
// Each platform provides nativeSetText, nativeSetImage and nativeSetFiles,
// which put content on the clipboard the way a real application would, so the
// production readers are checked against genuine clipboard formats.
// nativeSetTextExclusive additionally guarantees the clipboard offers text and
// nothing else, for tests that assert other formats are absent.
const nativeTestEnv = "AGENTCLIP_NATIVE_CLIPBOARD_TEST"

func requireNativeClipboard(t *testing.T) {
	t.Helper()
	if os.Getenv(nativeTestEnv) != "1" {
		t.Skipf("set %s=1 to run tests against the real clipboard (they overwrite it)", nativeTestEnv)
	}
}

func nativeContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// writeProbePNG writes an 8x6 PNG and returns its path.
func writeProbePNG(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 6))
	for x := 0; x < 8; x++ {
		for y := 0; y < 6; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 30), G: uint8(y * 40), B: 200, A: 255})
		}
	}
	path := filepath.Join(t.TempDir(), "probe.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, img); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNativeTextRoundTripKeepsMultibyteUTF8(t *testing.T) {
	requireNativeClipboard(t)
	const want = "agentclip é 中文 🚀\nsecond line"
	nativeSetText(t, want)
	got, err := Text(nativeContext(t))
	if err != nil || string(got) != want {
		t.Fatalf("Text = %q, %v; want %q", got, err, want)
	}
}

func TestNativeTextOnlyClipboardHasNoImageAndNoFiles(t *testing.T) {
	requireNativeClipboard(t)
	nativeSetTextExclusive(t, "just text")
	img, err := (NativeReader{}).ReadImage(nativeContext(t))
	if !errors.Is(err, ErrNoImage) || len(img) != 0 {
		t.Fatalf("ReadImage = %d bytes, %v; want ErrNoImage and no bytes", len(img), err)
	}
	if _, err := FilePaths(nativeContext(t)); !errors.Is(err, ErrNoFiles) {
		t.Fatalf("FilePaths err = %v, want ErrNoFiles", err)
	}
}

func TestNativePNGImageIsCapturedAndValidated(t *testing.T) {
	requireNativeClipboard(t)
	nativeSetImage(t, writeProbePNG(t))
	captured, err := Capture(nativeContext(t), NativeReader{})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if captured.Width != 8 || captured.Height != 6 || captured.SHA256 == "" {
		t.Fatalf("captured %dx%d sha=%q, want 8x6", captured.Width, captured.Height, captured.SHA256)
	}
}

func TestNativeFileReferencesResolveToPaths(t *testing.T) {
	requireNativeClipboard(t)
	dir := t.TempDir()
	first, second := filepath.Join(dir, "a file.txt"), filepath.Join(dir, "b.csv")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	nativeSetFiles(t, first, second)
	paths, err := FilePaths(nativeContext(t))
	if err != nil {
		t.Fatalf("FilePaths: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("got %d paths %q, want 2", len(paths), paths)
	}
	for i, want := range []string{first, second} {
		got, _ := filepath.EvalSymlinks(paths[i])
		expected, _ := filepath.EvalSymlinks(want)
		if got != expected {
			t.Errorf("path %d = %q, want %q", i, paths[i], want)
		}
	}
}
