package companion

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wendellrocha/agentclip/internal/testenv"
)

// Asking for the log of a profile that never ran must not create one: the name
// is whatever the user typed, and each attempt used to leave an empty file.
func TestRequireLogNeverCreatesALog(t *testing.T) {
	testenv.IsolateUserDirs(t)
	if err := RequireLog("typo"); !errors.Is(err, ErrNoLog) {
		t.Fatalf("RequireLog of an unused profile = %v, want ErrNoLog", err)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(filepath.Join(cache, "agentclip", "logs")); len(entries) != 0 {
		t.Fatalf("RequireLog created %d file(s)", len(entries))
	}

	logger, err := NewLogger("real")
	if err != nil {
		t.Fatal(err)
	}
	logger.Close()
	if err := RequireLog("real"); err != nil {
		t.Fatalf("RequireLog of a profile with a log = %v", err)
	}
}

func TestRequireLogRefusesNamesThatAreNotProfiles(t *testing.T) {
	testenv.IsolateUserDirs(t)
	for _, name := range []string{"", "../escape", "a/b", `a\b`, ".", ".."} {
		err := RequireLog(name)
		if err == nil || errors.Is(err, ErrNoLog) {
			t.Errorf("RequireLog(%q) = %v, want an invalid-profile error", name, err)
		}
	}
}

// A log that cannot be inspected is reported as such, not as missing, and a
// path that is not a regular file is its own error.
func TestRequireLogDistinguishesMissingFromUnreadableAndIrregular(t *testing.T) {
	testenv.IsolateUserDirs(t)
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	logs := filepath.Join(cache, "agentclip", "logs")
	if err := os.MkdirAll(filepath.Join(logs, "isdir.log"), 0o700); err != nil {
		t.Fatal(err)
	}
	err = RequireLog("isdir")
	if err == nil || errors.Is(err, ErrNoLog) || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("a directory named like a log = %v, want the not-a-regular-file error", err)
	}

	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permissions cannot be withdrawn here")
	}
	if err := os.WriteFile(filepath.Join(logs, "locked.log"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(logs, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(logs, 0o700)
	err = RequireLog("locked")
	if err == nil || errors.Is(err, ErrNoLog) || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("an unreadable log directory = %v, want the permission error preserved", err)
	}
}
