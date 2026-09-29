package main

import (
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
