// Package testenv keeps tests away from the developer's real files.
package testenv

import "testing"

// IsolateUserDirs points every per-user directory at a temporary one for the
// duration of the test. os.UserCacheDir and os.UserConfigDir read different
// variables on each platform (XDG_* on Linux, HOME on macOS, LocalAppData and
// AppData on Windows), so setting only XDG_CACHE_HOME leaves macOS and Windows
// writing into the real cache, where AgentClip keeps its bridge state,
// profiles, logs and received files.
func IsolateUserDirs(t testing.TB) string {
	t.Helper()
	home := t.TempDir()
	for _, name := range []string{"HOME", "USERPROFILE", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "LocalAppData", "AppData"} {
		t.Setenv(name, home)
	}
	return home
}
