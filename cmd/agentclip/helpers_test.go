package main

import (
	"errors"
	"testing"
)

func TestRandomTokenAndPort(t *testing.T) {
	first, err := randomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	second, err := randomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) < 43 {
		t.Fatalf("tokens must be unique and long enough: %q %q", first, second)
	}
	for i := 0; i < 200; i++ {
		port, err := randomPort()
		if err != nil {
			t.Fatal(err)
		}
		if port < remotePortMin || port > remotePortMax {
			t.Fatalf("port %d outside [%d, %d]", port, remotePortMin, remotePortMax)
		}
	}
}

func TestHasRemoteUpgradeFailure(t *testing.T) {
	if hasRemoteUpgradeFailure(nil) {
		t.Fatal("no results means no failure")
	}
	ok := []remoteUpgradeResult{{Profile: "a"}}
	if hasRemoteUpgradeFailure(ok) {
		t.Fatal("successful results must not fail")
	}
	failed := append(ok, remoteUpgradeResult{Profile: "b", Err: errors.New("ssh")})
	if !hasRemoteUpgradeFailure(failed) {
		t.Fatal("a failed remote must be reported")
	}
}
