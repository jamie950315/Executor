//go:build windows

package terminal

import "testing"

func TestDefaultShellUsesPowerShellOnWindows(t *testing.T) {
	if got := defaultShell(); got != "powershell.exe" {
		t.Fatalf("defaultShell() = %q, want powershell.exe", got)
	}
}
