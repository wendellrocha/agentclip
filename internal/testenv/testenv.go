// Package testenv keeps tests away from the developer's real files.
package testenv

import (
	"os"
	"testing"
)

// IsolateUserDirs points every per-user directory at a temporary one for the
// duration of the test. os.UserCacheDir and os.UserConfigDir read different
// variables on each platform (XDG_* on Linux, HOME on macOS, LocalAppData and
// AppData on Windows), so setting only XDG_CACHE_HOME leaves macOS and Windows
// writing into the real cache, where AgentClip keeps its bridge state,
// profiles, logs and received files.
func IsolateUserDirs(t testing.TB) string {
	t.Helper()
	home := t.TempDir()
	for _, name := range userDirVariables {
		t.Setenv(name, home)
	}
	return home
}

var userDirVariables = []string{"HOME", "USERPROFILE", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "LocalAppData", "AppData"}

// Main runs a package's tests with every per-user directory redirected to a
// temporary one, so no test can reach the real files even if it forgets to call
// IsolateUserDirs. Use it from TestMain:
//
//	func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }
func Main(m *testing.M) int {
	home, err := os.MkdirTemp("", "agentclip-test-home-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(home)
	for _, name := range userDirVariables {
		os.Setenv(name, home)
	}
	return m.Run()
}
