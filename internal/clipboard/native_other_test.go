//go:build !darwin && !linux && !windows

package clipboard

import "testing"

func nativeSetText(t *testing.T, _ string) {
	t.Helper()
	t.Skip("native clipboard tests are not implemented on this platform")
}

func nativeSetImage(t *testing.T, _ string) {
	t.Helper()
	t.Skip("native clipboard tests are not implemented on this platform")
}

func nativeSetFiles(t *testing.T, _ ...string) {
	t.Helper()
	t.Skip("native clipboard tests are not implemented on this platform")
}
