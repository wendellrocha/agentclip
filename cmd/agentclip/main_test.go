package main

import (
	"strings"
	"testing"

	"github.com/wendellrocha/agentclip/internal/upgrader"
)

func TestWindowsUpgradeHelperCommandCarriesStagedBinaryAndActiveProfiles(t *testing.T) {
	staged := upgrader.StagedBinary{Path: `C:\\Temp\\agentclip.new`, Target: `C:\\Tools\\agentclip.exe`}
	command := windowsReplacementCommand(`C:\\Temp\\upgrade.ps1`, staged, `["dev","prod"]`, 42)
	arguments := strings.Join(command.Args, " ")
	for _, expected := range []string{"powershell", "-NoProfile", "-ExecutionPolicy Bypass", "-ProcessId 42", staged.Path, staged.Target, `["dev","prod"]`} {
		if !strings.Contains(arguments, expected) {
			t.Fatalf("Windows helper command %q does not contain %q", arguments, expected)
		}
	}
	for _, expected := range []string{"Wait-Process -Id $ProcessId", "Move-Item -Force", "Start-Process -WindowStyle Hidden", "Remove-Item -Force -LiteralPath $PSCommandPath"} {
		if !strings.Contains(windowsReplacementScript(), expected) {
			t.Fatalf("Windows helper script does not contain %q", expected)
		}
	}
}

func TestReleaseTag(t *testing.T) {
	tests := []struct {
		name      string
		requested string
		want      string
		wantError bool
	}{
		{name: "prefixes a stable local version", requested: "0.2.0", want: "v0.2.0"},
		{name: "accepts a tag", requested: "v1.2.3-rc.1", want: "v1.2.3-rc.1"},
		{name: "rejects development builds", requested: "0.2.0-dev", wantError: true},
		{name: "rejects an invalid version", requested: "latest", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := releaseTag(test.requested)
			if test.wantError {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("releaseTag(%q) = %q, want %q", test.requested, got, test.want)
			}
		})
	}
}

func TestDefaultProfileName(t *testing.T) {
	tests := map[string]string{
		"bastion-m2":              "bastion-m2",
		"wendell@bastion.example": "bastion-example",
		"user@[2001:db8::1]":      "2001-db8-1",
	}
	for destination, want := range tests {
		if got := defaultProfileName(destination); got != want {
			t.Errorf("defaultProfileName(%q) = %q, want %q", destination, got, want)
		}
	}
}
