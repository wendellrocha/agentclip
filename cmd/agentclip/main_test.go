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
