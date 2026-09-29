package clipboard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func nativeSetText(t *testing.T, text string) {
	t.Helper()
	command := exec.Command("pbcopy")
	command.Stdin = strings.NewReader(text)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("pbcopy: %v: %s", err, out)
	}
}

func nativeSetImage(t *testing.T, pngPath string) {
	t.Helper()
	// The path is an argument, not part of the script, so any character in a
	// temporary directory name is safe.
	command := exec.Command("osascript",
		"-e", "on run argv",
		"-e", "set the clipboard to (read (POSIX file (item 1 of argv)) as «class PNGf»)",
		"-e", "end run",
		pngPath)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("set image: %v: %s", err, out)
	}
}

// nativeSetFiles stores the paths under NSFilenamesPboardType, the file-manager
// format the reader consumes. Writing only modern file-URL items from a
// short-lived process is unreliable: other processes may see just the first
// item once the writer exits, so it is not a faithful stand-in for Finder.
func nativeSetFiles(t *testing.T, paths ...string) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "set-files.js")
	source := `ObjC.import('AppKit');
function run(argv) {
  var pasteboard = $.NSPasteboard.generalPasteboard;
  pasteboard.declareTypesOwner($(['NSFilenamesPboardType']), $());
  if (!pasteboard.setPropertyListForType($(argv), 'NSFilenamesPboardType')) throw new Error("setPropertyListForType failed");
}
`
	if err := os.WriteFile(script, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	arguments := append([]string{"-l", "JavaScript", script}, paths...)
	if out, err := exec.Command("osascript", arguments...).CombinedOutput(); err != nil {
		t.Fatalf("set files: %v: %s", err, out)
	}
}

// nativeSetTextExclusive: this platform's clipboard only offers the formats
// that were written, so plain text is already exclusive.
func nativeSetTextExclusive(t *testing.T, text string) { nativeSetText(t, text) }
