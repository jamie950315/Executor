package desktop

import (
	"fmt"
	"math"
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
		if len(action.Keys) == 0 {
			return nil, &UnavailableError{Reason: "wayland keyboard input unavailable: no text or key provided"}
		}
		if !tools["ydotool"] {
			return nil, &UnavailableError{Reason: "wayland keyboard input unavailable: install ydotool for key events"}
		}
		keys, err := normalizeKeys(action.Keys)
		if err != nil {
			return nil, err
		}
		command := []string{"ydotool", "key"}
		for _, key := range keys {
			code, ok := linuxInputKeyCode(key)
			if !ok {
				return nil, &UnavailableError{Reason: fmt.Sprintf("wayland key %q has no reliable ydotool mapping", key)}
			}
			command = append(command, strconv.Itoa(code)+":1")
		}
		for index := len(keys) - 1; index >= 0; index-- {
			code, _ := linuxInputKeyCode(keys[index])
			command = append(command, strconv.Itoa(code)+":0")
		}
		return command, nil
	}
	if tools["ydotool"] {
		return []string{"ydotool", "key", strconv.Itoa(action.KeyCode)}, nil
	}
	return nil, &UnavailableError{Reason: "wayland keyboard input unavailable: install ydotool for key events"}
}

func linuxInputKeyCode(key keyName) (int, bool) {
	codes := map[keyName]int{
		keyEscape: 1, "1": 2, "2": 3, "3": 4, "4": 5, "5": 6, "6": 7, "7": 8, "8": 9, "9": 10, "0": 11,
		keyBackspace: 14, keyTab: 15, "Q": 16, "W": 17, "E": 18, "R": 19, "T": 20, "Y": 21, "U": 22, "I": 23, "O": 24, "P": 25,
		keyEnter: 28, keyControl: 29, "A": 30, "S": 31, "D": 32, "F": 33, "G": 34, "H": 35, "J": 36, "K": 37, "L": 38,
		keyShift: 42, "Z": 44, "X": 45, "C": 46, "V": 47, "B": 48, "N": 49, "M": 50, keyAlt: 56, keySpace: 57,
		keyF1: 59, keyF2: 60, keyF3: 61, keyF4: 62, keyF5: 63, keyF6: 64, keyF7: 65, keyF8: 66, keyF9: 67, keyF10: 68, keyF11: 87, keyF12: 88,
		keyHome: 102, keyArrowUp: 103, keyPageUp: 104, keyArrowLeft: 105, keyArrowRight: 106,
		keyEnd: 107, keyArrowDown: 108, keyPageDown: 109, keyDelete: 111, keyMeta: 125,
	}
	code, ok := codes[key]
	return code, ok
}

func chooseWaylandMouseCommand(tools availableTools, action MouseAction) ([]string, error) {
	if !tools["ydotool"] {
		return nil, &UnavailableError{Reason: "wayland mouse input unavailable: install ydotool"}
	}
	if len(action.Keys) > 0 {
		return nil, &UnavailableError{Reason: "wayland modifier-assisted mouse input is not reliably supported by ydotool"}
	}
	if action.Type == MouseActionDoubleClick || action.Type == MouseActionDrag || action.Type == MouseActionScroll {
		return nil, &UnavailableError{Reason: fmt.Sprintf("wayland %s mouse input is not reliably supported by ydotool", action.Type)}
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
	case MouseButtonCenter, MouseButtonWheel:
		return "2", nil
	case MouseButtonRight:
		return "3", nil
	default:
		return "", fmt.Errorf("unsupported mouse button %q", button)
	}
}

func buildX11MouseCommands(action MouseAction) ([][]string, error) {
	steps, err := expandMouseAction(action)
	if err != nil {
		return nil, err
	}
	button, err := mouseButtonCode(action.Button)
	if err != nil {
		return nil, err
	}
	modifiers, err := normalizeModifiers(action.Keys)
	if err != nil {
		return nil, err
	}
	commands := make([][]string, 0, len(steps)+len(modifiers)*2)
	for _, modifier := range modifiers {
		commands = append(commands, []string{"keydown", xdotoolKey(modifier)})
	}
	for _, step := range steps {
		switch step.Type {
		case mouseStepMove, mouseStepDrag:
			commands = append(commands, []string{"mousemove", strconv.Itoa(step.X), strconv.Itoa(step.Y)})
		case mouseStepDown:
			commands = append(commands, []string{"mousedown", button})
		case mouseStepUp:
			commands = append(commands, []string{"mouseup", button})
		case mouseStepScroll:
			for _, scrollButton := range xdotoolScrollButtons(step.ScrollX, step.ScrollY) {
				commands = append(commands, []string{"click", scrollButton})
			}
		}
	}
	for index := len(modifiers) - 1; index >= 0; index-- {
		commands = append(commands, []string{"keyup", xdotoolKey(modifiers[index])})
	}
	return commands, nil
}

func xdotoolScrollButtons(scrollX, scrollY int) []string {
	buttons := make([]string, 0)
	appendButtons := func(delta int, negative, positive string) {
		if delta == 0 {
			return
		}
		button := positive
		if delta < 0 {
			button = negative
		}
		clicks := int(math.Abs(math.Round(float64(delta) / 100)))
		if clicks < 1 {
			clicks = 1
		}
		for range clicks {
			buttons = append(buttons, button)
		}
	}
	appendButtons(scrollY, "4", "5")
	appendButtons(scrollX, "6", "7")
	return buttons
}

func xdotoolKey(key keyName) string {
	switch key {
	case keyControl:
		return "ctrl"
	case keyAlt:
		return "alt"
	case keyShift:
		return "shift"
	case keyMeta:
		return "super"
	case keyArrowUp:
		return "Up"
	case keyArrowDown:
		return "Down"
	case keyArrowLeft:
		return "Left"
	case keyArrowRight:
		return "Right"
	case keyEnter:
		return "Return"
	case keyEscape:
		return "Escape"
	case keyBackspace:
		return "BackSpace"
	case keyPageUp:
		return "Page_Up"
	case keyPageDown:
		return "Page_Down"
	case keySpace:
		return "space"
	default:
		return string(key)
	}
}

func buildX11KeypressCommand(keys []string) ([]string, error) {
	normalized, err := normalizeKeys(keys)
	if err != nil {
		return nil, err
	}
	if len(normalized) == 0 {
		return nil, fmt.Errorf("keypress action requires keys")
	}
	names := make([]string, 0, len(normalized))
	for _, key := range normalized {
		names = append(names, xdotoolKey(key))
	}
	return []string{"key", strings.Join(names, "+")}, nil
}

func darwinKeyCode(key keyName) (int, error) {
	codes := map[keyName]int{
		"A": 0, "S": 1, "D": 2, "F": 3, "H": 4, "G": 5, "Z": 6, "X": 7,
		"C": 8, "V": 9, "B": 11, "Q": 12, "W": 13, "E": 14, "R": 15,
		"Y": 16, "T": 17, "1": 18, "2": 19, "3": 20, "4": 21, "6": 22,
		"5": 23, "9": 25, "7": 26, "8": 28, "0": 29, "O": 31, "U": 32,
		"I": 34, "P": 35, "L": 37, "J": 38, "K": 40, "N": 45, "M": 46,
		keyEnter: 36, keyTab: 48, keySpace: 49, keyBackspace: 51, keyEscape: 53,
		keyMeta: 55, keyShift: 56, keyAlt: 58, keyControl: 59,
		keyF1: 122, keyF2: 120, keyF3: 99, keyF4: 118, keyF5: 96, keyF6: 97,
		keyF7: 98, keyF8: 100, keyF9: 101, keyF10: 109, keyF11: 103, keyF12: 111,
		keyHome: 115, keyPageUp: 116, keyDelete: 117, keyEnd: 119, keyPageDown: 121,
		keyArrowLeft: 123, keyArrowRight: 124, keyArrowDown: 125, keyArrowUp: 126,
	}
	code, ok := codes[key]
	if !ok {
		return 0, &UnavailableError{Reason: fmt.Sprintf("macOS key %q has no reliable virtual-key mapping", key)}
	}
	return code, nil
}

func buildWindowsScreenshotScript(path string) string {
	return "$ErrorActionPreference='Stop'; Add-Type -AssemblyName System.Drawing; Add-Type -AssemblyName System.Windows.Forms; $bounds=[System.Windows.Forms.Screen]::PrimaryScreen.Bounds; $bitmap=New-Object System.Drawing.Bitmap $bounds.Width,$bounds.Height; $graphics=[System.Drawing.Graphics]::FromImage($bitmap); $graphics.CopyFromScreen($bounds.X,$bounds.Y,0,0,$bitmap.Size); $bitmap.Save('" + psSingleQuote(path) + "',[System.Drawing.Imaging.ImageFormat]::Png); $graphics.Dispose(); $bitmap.Dispose()"
}

func buildWindowsDesktopAvailabilityScript() string {
	return `$ErrorActionPreference='Stop'; Add-Type @'
using System;
using System.Runtime.InteropServices;
using System.Text;
public static class ExecutorDesktopProbe {
  public const int WTSConnectState = 8;
  public const int WTSActive = 0;
  [DllImport("user32.dll", SetLastError=true)] public static extern IntPtr OpenInputDesktop(uint flags, bool inherit, uint access);
  [DllImport("user32.dll", SetLastError=true)] [return: MarshalAs(UnmanagedType.Bool)] public static extern bool GetUserObjectInformation(IntPtr handle, int index, StringBuilder value, int length, ref int needed);
  [DllImport("user32.dll")] [return: MarshalAs(UnmanagedType.Bool)] public static extern bool CloseDesktop(IntPtr handle);
  [DllImport("wtsapi32.dll", SetLastError=true)] [return: MarshalAs(UnmanagedType.Bool)] static extern bool WTSQuerySessionInformation(IntPtr server, int sessionId, int infoClass, out IntPtr buffer, out int bytes);
  [DllImport("wtsapi32.dll")] static extern void WTSFreeMemory(IntPtr buffer);
  public static bool CurrentSessionActive() {
    IntPtr buffer; int bytes;
    if (!WTSQuerySessionInformation(IntPtr.Zero, -1, WTSConnectState, out buffer, out bytes) || buffer == IntPtr.Zero) return false;
    try { return bytes >= 4 && Marshal.ReadInt32(buffer) == WTSActive; }
    finally { WTSFreeMemory(buffer); }
  }
}
'@; if(-not [ExecutorDesktopProbe]::CurrentSessionActive()){exit 1}; $desktop=[ExecutorDesktopProbe]::OpenInputDesktop(0,$false,1); if($desktop -eq [IntPtr]::Zero){exit 1}; try{$name=New-Object System.Text.StringBuilder 256; $needed=0; if(-not [ExecutorDesktopProbe]::GetUserObjectInformation($desktop,2,$name,$name.Capacity,[ref]$needed)){exit 1}; if($name.ToString() -ne 'Default'){exit 1}} finally{[void][ExecutorDesktopProbe]::CloseDesktop($desktop)}`
}

func buildWindowsEnumWindowsScript() string {
	return "$ErrorActionPreference='Stop'; Add-Type @'\nusing System;\nusing System.Text;\nusing System.Runtime.InteropServices;\npublic static class ExecutorWin32 {\n  public delegate bool EnumWindowsProc(IntPtr hWnd, IntPtr lParam);\n  [DllImport(\"user32.dll\")] public static extern bool EnumWindows(EnumWindowsProc lpEnumFunc, IntPtr lParam);\n  [DllImport(\"user32.dll\")] public static extern bool IsWindowVisible(IntPtr hWnd);\n  [DllImport(\"user32.dll\")] public static extern int GetWindowTextLength(IntPtr hWnd);\n  [DllImport(\"user32.dll\")] public static extern int GetWindowText(IntPtr hWnd, StringBuilder text, int count);\n  [DllImport(\"user32.dll\")] public static extern uint GetWindowThreadProcessId(IntPtr hWnd, out uint processId);\n}\n'@; $items=New-Object System.Collections.Generic.List[object]; $callback=[ExecutorWin32+EnumWindowsProc]{ param($hWnd,$lParam) if(-not [ExecutorWin32]::IsWindowVisible($hWnd)){ return $true } $len=[ExecutorWin32]::GetWindowTextLength($hWnd); if($len -le 0){ return $true } $sb=New-Object System.Text.StringBuilder ($len+1); [void][ExecutorWin32]::GetWindowText($hWnd,$sb,$sb.Capacity); $processId=0; [void][ExecutorWin32]::GetWindowThreadProcessId($hWnd,[ref]$processId); $procName=''; try { $procName=(Get-Process -Id $processId -ErrorAction Stop).ProcessName } catch {} $items.Add([pscustomobject]@{ app=$procName; title=$sb.ToString(); id=[int]$hWnd }) | Out-Null; return $true }; [ExecutorWin32]::EnumWindows($callback,[IntPtr]::Zero) | Out-Null; ConvertTo-Json -InputObject $items.ToArray() -Compress"
}

func buildWindowsAccessibilityScript() string {
	return "$ErrorActionPreference='Stop'; Add-Type @'\nusing System;\nusing System.Text;\nusing System.Runtime.InteropServices;\npublic static class ExecutorForeground {\n  [DllImport(\"user32.dll\")] public static extern IntPtr GetForegroundWindow();\n  [DllImport(\"user32.dll\")] public static extern int GetWindowTextLength(IntPtr hWnd);\n  [DllImport(\"user32.dll\")] public static extern int GetWindowText(IntPtr hWnd, StringBuilder text, int count);\n}\n'@; $h=[ExecutorForeground]::GetForegroundWindow(); $title=''; if($h -ne [IntPtr]::Zero){ $len=[ExecutorForeground]::GetWindowTextLength($h); if($len -gt 0){ $sb=New-Object System.Text.StringBuilder ($len+1); [void][ExecutorForeground]::GetWindowText($h,$sb,$sb.Capacity); $title=$sb.ToString() } }; [pscustomobject]@{ application='foreground'; windows=@([pscustomobject]@{ title=$title; role='window' }) } | ConvertTo-Json -Compress"
}

func buildWindowsMouseScript(action MouseAction) string {
	buttonMask := "2"
	switch action.Button {
	case "", MouseButtonLeft:
		buttonMask = "2"
	case MouseButtonRight:
		buttonMask = "8"
	case MouseButtonCenter, MouseButtonWheel:
		buttonMask = "32"
	}
	steps, _ := expandMouseAction(action)
	modifiers, _ := normalizeModifiers(action.Keys)
	var events strings.Builder
	for _, modifier := range modifiers {
		events.WriteString("[ExecutorMouse]::keybd_event(" + windowsVirtualKey(modifier) + ",0,0,0); ")
	}
	for _, step := range steps {
		switch step.Type {
		case mouseStepMove, mouseStepDrag:
			events.WriteString("[ExecutorMouse]::SetCursorPos(" + strconv.Itoa(step.X) + "," + strconv.Itoa(step.Y) + ") | Out-Null; ")
		case mouseStepDown:
			events.WriteString("[ExecutorMouse]::mouse_event(" + buttonMask + ",0,0,0,0); ")
		case mouseStepUp:
			events.WriteString("[ExecutorMouse]::mouse_event(" + strconv.Itoa(maskUp(buttonMask)) + ",0,0,0,0); ")
		case mouseStepScroll:
			scrollX, scrollY := nativeWheelDeltas(step.ScrollX, step.ScrollY)
			if step.ScrollY != 0 {
				events.WriteString("[ExecutorMouse]::mouse_event(2048,0,0," + strconv.Itoa(scrollY) + ",0); ")
			}
			if step.ScrollX != 0 {
				events.WriteString("[ExecutorMouse]::mouse_event(4096,0,0," + strconv.Itoa(scrollX) + ",0); ")
			}
		}
	}
	for index := len(modifiers) - 1; index >= 0; index-- {
		events.WriteString("[ExecutorMouse]::keybd_event(" + windowsVirtualKey(modifiers[index]) + ",0,2,0); ")
	}
	return "$ErrorActionPreference='Stop'; Add-Type @'\nusing System;\nusing System.Runtime.InteropServices;\npublic static class ExecutorMouse {\n  [DllImport(\"user32.dll\")] public static extern bool SetCursorPos(int X, int Y);\n  [DllImport(\"user32.dll\")] public static extern void mouse_event(uint dwFlags, uint dx, uint dy, int dwData, UIntPtr dwExtraInfo);\n  [DllImport(\"user32.dll\")] public static extern void keybd_event(byte bVk, byte bScan, uint dwFlags, UIntPtr dwExtraInfo);\n}\n'@; " + events.String()
}

func windowsVirtualKey(key keyName) string {
	switch key {
	case keyEnter:
		return "0x0D"
	case keyTab:
		return "0x09"
	case keyEscape:
		return "0x1B"
	case keyBackspace:
		return "0x08"
	case keySpace:
		return "0x20"
	case keyPageUp:
		return "0x21"
	case keyPageDown:
		return "0x22"
	case keyEnd:
		return "0x23"
	case keyHome:
		return "0x24"
	case keyArrowLeft:
		return "0x25"
	case keyArrowUp:
		return "0x26"
	case keyArrowRight:
		return "0x27"
	case keyArrowDown:
		return "0x28"
	case keyDelete:
		return "0x2E"
	case keyShift:
		return "0x10"
	case keyControl:
		return "0x11"
	case keyAlt:
		return "0x12"
	case keyMeta:
		return "0x5B"
	default:
		if len(key) > 1 && key[0] == 'F' {
			value, _ := strconv.Atoi(string(key[1:]))
			return fmt.Sprintf("0x%02X", 0x6F+value)
		}
		return strconv.Itoa(int(key[0]))
	}
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
	if len(action.Keys) > 0 {
		keys, _ := normalizeKeys(action.Keys)
		var events strings.Builder
		for _, key := range keys {
			virtualKey := windowsVirtualKey(key)
			events.WriteString("[ExecutorKeyboard]::keybd_event(" + virtualKey + ",0,0,0); ")
		}
		for index := len(keys) - 1; index >= 0; index-- {
			virtualKey := windowsVirtualKey(keys[index])
			events.WriteString("[ExecutorKeyboard]::keybd_event(" + virtualKey + ",0,2,0); ")
		}
		return "$ErrorActionPreference='Stop'; Add-Type @'\nusing System;\nusing System.Runtime.InteropServices;\npublic static class ExecutorKeyboard {\n  [DllImport(\"user32.dll\")] public static extern void keybd_event(byte bVk, byte bScan, uint dwFlags, UIntPtr dwExtraInfo);\n}\n'@; " + events.String()
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
