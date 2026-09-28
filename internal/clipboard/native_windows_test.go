package clipboard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// powershell runs a script in a single-threaded apartment, which the Windows
// clipboard APIs require.
func powershell(t *testing.T, script string, arguments ...string) {
	t.Helper()
	command := exec.Command("powershell.exe", append([]string{"-NoProfile", "-STA", "-NonInteractive", "-Command", script}, arguments...)...)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("powershell: %v: %s", err, out)
	}
}

func nativeSetText(t *testing.T, text string) {
	t.Helper()
	// Go through a UTF-8 file so multibyte text survives the command line.
	path := filepath.Join(t.TempDir(), "text.txt")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	powershell(t, `Set-Clipboard -Value ([System.IO.File]::ReadAllText('`+path+`', [System.Text.Encoding]::UTF8))`)
}

func nativeSetImage(t *testing.T, pngPath string) {
	t.Helper()
	powershell(t, `Add-Type -AssemblyName System.Windows.Forms, System.Drawing; `+
		`[System.Windows.Forms.Clipboard]::SetImage([System.Drawing.Image]::FromFile('`+pngPath+`'))`)
}

func nativeSetFiles(t *testing.T, paths ...string) {
	t.Helper()
	quoted := make([]string, len(paths))
	for i, path := range paths {
		quoted[i] = "'" + strings.ReplaceAll(path, "'", "''") + "'"
	}
	powershell(t, `Set-Clipboard -Path `+strings.Join(quoted, ","))
}
