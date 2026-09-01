package companion

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wendellrocha/agentclip/internal/clipboard"
)

func TestSaveAndLoadProfileUsesPrivateConfigFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	want := Profile{Name: "dev", Destination: "dev.example", RemotePort: 39123, Token: "pair-token", CreatedAt: time.Now().UTC().Round(0)}
	if err := SaveProfile(want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadProfile("dev")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("profile = %#v, want %#v", got, want)
	}
	path, err := profilePath("dev")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("profile permissions = %o, want 600", info.Mode().Perm())
	}
}

func TestProfileRejectsUnsafeName(t *testing.T) {
	profile := Profile{Name: "../other", Destination: "dev", RemotePort: 39123, Token: "token"}
	if err := profile.Validate(); err == nil {
		t.Fatal("expected invalid profile name")
	}
}

func TestTunnelCommandUsesLoopbackReverseForward(t *testing.T) {
	command, err := TunnelCommand(Profile{Name: "dev", Destination: "dev", RemotePort: 39123, Token: "token"}, 45678)
	if err != nil {
		t.Fatal(err)
	}
	arguments := strings.Join(command.Args, " ")
	for _, expected := range []string{"-N", "-R 127.0.0.1:39123:127.0.0.1:45678", "ExitOnForwardFailure=yes", "ServerAliveInterval=30"} {
		if !strings.Contains(arguments, expected) {
			t.Errorf("command %q does not contain %q", arguments, expected)
		}
	}
}

func TestWatcherArmsInitialAndNewImage(t *testing.T) {
	first := pngFixture(t, color.RGBA{R: 1, A: 255})
	second := pngFixture(t, color.RGBA{B: 1, A: 255})
	reader := &fakeReader{responses: [][]byte{first, first, second}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var armed []string
	watcher := Watcher{
		Reader: reader, Interval: time.Millisecond,
		Arm: func(_ context.Context, image clipboard.Image) error {
			mu.Lock()
			armed = append(armed, image.SHA256)
			shouldStop := len(armed) == 2
			mu.Unlock()
			if shouldStop {
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
	if len(armed) != 2 || armed[0] == armed[1] {
		t.Fatalf("armed = %#v", armed)
	}
}

type fakeReader struct {
	responses [][]byte
	index     int
}

func (r *fakeReader) ReadImage(context.Context) ([]byte, error) {
	if r.index >= len(r.responses) {
		return nil, errors.New("no image")
	}
	image := r.responses[r.index]
	r.index++
	return image, nil
}

func pngFixture(t *testing.T, fill color.RGBA) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, 2, 2))
	canvas.SetRGBA(0, 0, fill)
	var output bytes.Buffer
	if err := png.Encode(&output, canvas); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
