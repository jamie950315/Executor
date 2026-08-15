package desktop

import (
	"fmt"
	"strconv"
	"strings"
)

type availableTools map[string]bool

func chooseWaylandScreenshotCommand(tools availableTools, path string) ([]string, error) {
	switch {
	case tools["grim"]:
		return []string{"grim", path}, nil
	case tools["gnome-screenshot"]:
		return []string{"gnome-screenshot", "-f", path}, nil
	default:
		return nil, &UnavailableError{Reason: "wayland screenshot unavailable: no supported screenshot tool found"}
	}
}

func chooseWaylandKeyboardCommand(tools availableTools, action KeyboardAction) ([]string, error) {
	if action.Text != "" {
		switch {
		case tools["wtype"]:
			return []string{"wtype", action.Text}, nil
		case tools["ydotool"]:
			return []string{"ydotool", "type", "--key-delay", "1", action.Text}, nil
		default:
			return nil, &UnavailableError{Reason: "wayland keyboard input unavailable: install wtype or ydotool"}
		}
	}
	if action.KeyCode == 0 {
		return nil, &UnavailableError{Reason: "wayland keyboard input unavailable: no text or key code provided"}
	}
	if tools["ydotool"] {
		return []string{"ydotool", "key", strconv.Itoa(action.KeyCode)}, nil
	}
	return nil, &UnavailableError{Reason: "wayland keyboard input unavailable: install ydotool for key events"}
}

func chooseWaylandMouseCommand(tools availableTools, action MouseAction) ([]string, error) {
	if !tools["ydotool"] {
		return nil, &UnavailableError{Reason: "wayland mouse input unavailable: install ydotool"}
	}
	button, err := mouseButtonCode(action.Button)
	if err != nil {
		return nil, err
	}
	args := []string{"ydotool", "mousemove", "--absolute", strconv.Itoa(action.X), strconv.Itoa(action.Y)}
	switch action.Type {
	case MouseActionMove:
		return args, nil
	case MouseActionClick:
		return append(args, "click", button), nil
	case MouseActionDown:
		return append(args, "mousedown", button), nil
	case MouseActionUp:
		return append(args, "mouseup", button), nil
	default:
		return nil, fmt.Errorf("unsupported mouse action %q", action.Type)
	}
}

func mouseButtonCode(button MouseButton) (string, error) {
	switch button {
	case "", MouseButtonLeft:
		return "1", nil
	case MouseButtonCenter:
		return "2", nil
	case MouseButtonRight:
		return "3", nil
	default:
		return "", fmt.Errorf("unsupported mouse button %q", button)
	}
}

func buildWindowsScreenshotScript(path string) string {
	return "$ErrorActionPreference='Stop'; Add-Type -AssemblyName System.Drawing; Add-Type -AssemblyName System.Windows.Forms; $bounds=[System.Windows.Forms.SystemInformation]::VirtualScreen; $bitmap=New-Object System.Drawing.Bitmap $bounds.Width,$bounds.Height; $graphics=[System.Drawing.Graphics]::FromImage($bitmap); $graphics.CopyFromScreen($bounds.X,$bounds.Y,0,0,$bitmap.Size); $bitmap.Save('" + psSingleQuote(path) + "',[System.Drawing.Imaging.ImageFormat]::Png); $graphics.Dispose(); $bitmap.Dispose()"
}

func buildWindowsEnumWindowsScript() string {
	return "$ErrorActionPreference='Stop'; Add-Type @'\nusing System;\nusing System.Text;\nusing System.Runtime.InteropServices;\npublic static class ExecutorWin32 {\n  public delegate bool EnumWindowsProc(IntPtr hWnd, IntPtr lParam);\n  [DllImport(\"user32.dll\")] public static extern bool EnumWindows(EnumWindowsProc lpEnumFunc, IntPtr lParam);\n  [DllImport(\"user32.dll\")] public static extern bool IsWindowVisible(IntPtr hWnd);\n  [DllImport(\"user32.dll\")] public static extern int GetWindowTextLength(IntPtr hWnd);\n  [DllImport(\"user32.dll\")] public static extern int GetWindowText(IntPtr hWnd, StringBuilder text, int count);\n  [DllImport(\"user32.dll\")] public static extern uint GetWindowThreadProcessId(IntPtr hWnd, out uint processId);\n}\n'@; $items=New-Object System.Collections.Generic.List[object]; $callback=[ExecutorWin32+EnumWindowsProc]{ param($hWnd,$lParam) if(-not [ExecutorWin32]::IsWindowVisible($hWnd)){ return $true } $len=[ExecutorWin32]::GetWindowTextLength($hWnd); if($len -le 0){ return $true } $sb=New-Object System.Text.StringBuilder ($len+1); [void][ExecutorWin32]::GetWindowText($hWnd,$sb,$sb.Capacity); $pid=0; [void][ExecutorWin32]::GetWindowThreadProcessId($hWnd,[ref]$pid); $procName=''; try { $procName=(Get-Process -Id $pid -ErrorAction Stop).ProcessName } catch {} $items.Add([pscustomobject]@{ app=$procName; title=$sb.ToString(); id=[int]$hWnd }) | Out-Null; return $true }; [ExecutorWin32]::EnumWindows($callback,[IntPtr]::Zero) | Out-Null; $items | ConvertTo-Json -Compress"
}

func buildWindowsAccessibilityScript() string {
	return "$ErrorActionPreference='Stop'; Add-Type @'\nusing System;\nusing System.Text;\nusing System.Runtime.InteropServices;\npublic static class ExecutorForeground {\n  [DllImport(\"user32.dll\")] public static extern IntPtr GetForegroundWindow();\n  [DllImport(\"user32.dll\")] public static extern int GetWindowTextLength(IntPtr hWnd);\n  [DllImport(\"user32.dll\")] public static extern int GetWindowText(IntPtr hWnd, StringBuilder text, int count);\n}\n'@; $h=[ExecutorForeground]::GetForegroundWindow(); $title=''; if($h -ne [IntPtr]::Zero){ $len=[ExecutorForeground]::GetWindowTextLength($h); if($len -gt 0){ $sb=New-Object System.Text.StringBuilder ($len+1); [void][ExecutorForeground]::GetWindowText($h,$sb,$sb.Capacity); $title=$sb.ToString() } }; [pscustomobject]@{ application='foreground'; windows=@([pscustomobject]@{ title=$title; role='window' }) } | ConvertTo-Json -Compress"
}

func buildWindowsMouseScript(action MouseAction) string {
	buttonMask := "0"
	switch action.Button {
	case "", MouseButtonLeft:
		buttonMask = "2"
	case MouseButtonRight:
		buttonMask = "8"
	case MouseButtonCenter:
		buttonMask = "32"
	}
	eventScript := ""
	switch action.Type {
	case MouseActionMove:
		eventScript = ""
	case MouseActionDown:
		eventScript = "[ExecutorMouse]::mouse_event(" + buttonMask + ",0,0,0,0)"
	case MouseActionUp:
		eventScript = "[ExecutorMouse]::mouse_event(" + strconv.Itoa(maskUp(buttonMask)) + ",0,0,0,0)"
	case MouseActionClick:
		eventScript = "[ExecutorMouse]::mouse_event(" + buttonMask + ",0,0,0,0); [ExecutorMouse]::mouse_event(" + strconv.Itoa(maskUp(buttonMask)) + ",0,0,0,0)"
	}
	return "$ErrorActionPreference='Stop'; Add-Type @'\nusing System;\nusing System.Runtime.InteropServices;\npublic static class ExecutorMouse {\n  [DllImport(\"user32.dll\")] public static extern bool SetCursorPos(int X, int Y);\n  [DllImport(\"user32.dll\")] public static extern void mouse_event(uint dwFlags, uint dx, uint dy, uint dwData, UIntPtr dwExtraInfo);\n}\n'@; [ExecutorMouse]::SetCursorPos(" + strconv.Itoa(action.X) + "," + strconv.Itoa(action.Y) + ") | Out-Null; " + eventScript
}

func maskUp(downMask string) int {
	switch downMask {
	case "2":
		return 4
	case "8":
		return 16
	case "32":
		return 64
	default:
		return 0
	}
}

func buildWindowsKeyboardScript(action KeyboardAction) string {
	if action.Text != "" {
		return "$ErrorActionPreference='Stop'; Add-Type -AssemblyName System.Windows.Forms; [System.Windows.Forms.SendKeys]::SendWait('" + psSingleQuote(sendKeysEscape(action.Text)) + "')"
	}
	modifierScript := ""
	for _, modifier := range action.Modifiers {
		switch strings.ToLower(modifier) {
		case "shift":
			modifierScript += "[ExecutorKeyboard]::keybd_event(0x10,0,0,0); "
		case "control", "ctrl":
			modifierScript += "[ExecutorKeyboard]::keybd_event(0x11,0,0,0); "
		case "alt", "option":
			modifierScript += "[ExecutorKeyboard]::keybd_event(0x12,0,0,0); "
		}
	}
	releaseScript := ""
	for index := len(action.Modifiers) - 1; index >= 0; index-- {
		switch strings.ToLower(action.Modifiers[index]) {
		case "shift":
			releaseScript += "[ExecutorKeyboard]::keybd_event(0x10,0,2,0); "
		case "control", "ctrl":
			releaseScript += "[ExecutorKeyboard]::keybd_event(0x11,0,2,0); "
		case "alt", "option":
			releaseScript += "[ExecutorKeyboard]::keybd_event(0x12,0,2,0); "
		}
	}
	return "$ErrorActionPreference='Stop'; Add-Type @'\nusing System;\nusing System.Runtime.InteropServices;\npublic static class ExecutorKeyboard {\n  [DllImport(\"user32.dll\")] public static extern void keybd_event(byte bVk, byte bScan, uint dwFlags, UIntPtr dwExtraInfo);\n}\n'@; " + modifierScript + "[ExecutorKeyboard]::keybd_event(" + strconv.Itoa(action.KeyCode) + ",0,0,0); [ExecutorKeyboard]::keybd_event(" + strconv.Itoa(action.KeyCode) + ",0,2,0); " + releaseScript
}

func buildWindowsAppScript(action AppAction) string {
	switch action.Type {
	case AppActionActivate:
		return "$ErrorActionPreference='Stop'; $p=Get-Process -Name '" + psSingleQuote(action.Name) + "' -ErrorAction Stop | Select-Object -First 1; $ws=New-Object -ComObject WScript.Shell; [void]$ws.AppActivate($p.Id)"
	case AppActionLaunch:
		return "$ErrorActionPreference='Stop'; Start-Process -FilePath '" + psSingleQuote(action.Name) + "'"
	case AppActionQuit:
		return "$ErrorActionPreference='Stop'; Stop-Process -Name '" + psSingleQuote(action.Name) + "' -Force"
	default:
		return "$ErrorActionPreference='Stop'; throw 'unsupported app action'"
	}
}

func psSingleQuote(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

func sendKeysEscape(value string) string {
	replacer := strings.NewReplacer(
		"{", "{{}",
		"}", "{}}",
		"+", "{+}",
		"^", "{^}",
		"%", "{%}",
		"~", "{~}",
		"(", "{(}",
		")", "{)}",
		"[", "{[}",
		"]", "{]}",
	)
	return replacer.Replace(value)
}
