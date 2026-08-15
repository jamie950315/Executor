//go:build windows

package terminal

func defaultShell() string {
	return "powershell.exe"
}
