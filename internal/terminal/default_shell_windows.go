//go:build windows

package terminal

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const defaultWindowsPowerShell7Path = `C:\Program Files\PowerShell\7\pwsh.exe`

func defaultShell() string {
	return resolveWindowsDefaultShell(exec.LookPath, os.Stat, os.Getenv("ProgramFiles"))
}

func resolveWindowsDefaultShell(
	lookPath func(string) (string, error),
	stat func(string) (os.FileInfo, error),
	programFiles string,
) string {
	if path, err := lookPath("pwsh.exe"); err == nil && strings.TrimSpace(path) != "" {
		return path
	}

	candidates := make([]string, 0, 2)
	if trimmed := strings.TrimSpace(programFiles); trimmed != "" {
		candidates = append(candidates, filepath.Join(trimmed, "PowerShell", "7", "pwsh.exe"))
	}
	if len(candidates) == 0 || !strings.EqualFold(candidates[0], defaultWindowsPowerShell7Path) {
		candidates = append(candidates, defaultWindowsPowerShell7Path)
	}

	for _, candidate := range candidates {
		if info, err := stat(candidate); err == nil && info != nil && !info.IsDir() {
			return candidate
		}
	}
	return "powershell.exe"
}
