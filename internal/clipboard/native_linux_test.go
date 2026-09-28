package clipboard

import (
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// xclip forks into the background to own the selection, so its output must not
// be piped back to the test or Wait would block until it exits.
func xclipCopy(t *testing.T, mime string, stdin *strings.Reader, file string) {
	t.Helper()
	if os.Getenv("DISPLAY") == "" {
		t.Skip("DISPLAY is not set; run under an X server, for example xvfb-run -a")
	}
	if _, err := exec.LookPath("xclip"); err != nil {
		t.Skip("xclip is not installed")
	}
	arguments := []string{"-selection", "clipboard", "-t", mime, "-i"}
	if file != "" {
		arguments = append(arguments, file)
	}
	command := exec.Command("xclip", arguments...)
	if stdin != nil {
		command.Stdin = stdin
	}
	if err := command.Run(); err != nil {
		t.Fatalf("xclip %s: %v", mime, err)
	}
}

func nativeSetText(t *testing.T, text string) {
	t.Helper()
	xclipCopy(t, "text/plain;charset=utf-8", strings.NewReader(text), "")
}

func nativeSetImage(t *testing.T, pngPath string) {
	t.Helper()
	xclipCopy(t, "image/png", nil, pngPath)
}

// nativeSetFiles publishes a text/uri-list, the format file managers use.
func nativeSetFiles(t *testing.T, paths ...string) {
	t.Helper()
	var list strings.Builder
	for _, path := range paths {
		list.WriteString((&url.URL{Scheme: "file", Path: path}).String() + "\r\n")
	}
	xclipCopy(t, "text/uri-list", strings.NewReader(list.String()), "")
}
