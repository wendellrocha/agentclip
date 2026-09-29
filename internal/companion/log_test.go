package companion

import (
	"errors"
	"os"
	"path/filepath"
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
