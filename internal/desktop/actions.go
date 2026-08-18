package desktop

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type ActionKind string

const (
	ActionClick       ActionKind = "click"
	ActionDoubleClick ActionKind = "double_click"
	ActionMove        ActionKind = "move"
	ActionDrag        ActionKind = "drag"
	ActionScroll      ActionKind = "scroll"
	ActionType        ActionKind = "type"
	ActionKeypress    ActionKind = "keypress"
	ActionWait        ActionKind = "wait"
	ActionScreenshot  ActionKind = "screenshot"
)

type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type Action struct {
	Type    ActionKind  `json:"type"`
	Button  MouseButton `json:"button,omitempty"`
	X       int         `json:"x,omitempty"`
	Y       int         `json:"y,omitempty"`
	ScrollX int         `json:"scroll_x,omitempty"`
	ScrollY int         `json:"scroll_y,omitempty"`
	Text    string      `json:"text,omitempty"`
	Keys    []string    `json:"keys,omitempty"`
	Path    []Point     `json:"path,omitempty"`
}

type keyName string

const (
	keyControl    keyName = "CTRL"
	keyAlt        keyName = "ALT"
	keyShift      keyName = "SHIFT"
	keyMeta       keyName = "META"
	keyArrowUp    keyName = "ARROWUP"
	keyArrowDown  keyName = "ARROWDOWN"
	keyArrowLeft  keyName = "ARROWLEFT"
	keyArrowRight keyName = "ARROWRIGHT"
	keyEnter      keyName = "ENTER"
	keyTab        keyName = "TAB"
	keyEscape     keyName = "ESC"
	keyBackspace  keyName = "BACKSPACE"
	keyDelete     keyName = "DELETE"
	keyHome       keyName = "HOME"
	keyEnd        keyName = "END"
	keyPageUp     keyName = "PAGEUP"
	keyPageDown   keyName = "PAGEDOWN"
	keySpace      keyName = "SPACE"
	keyF1         keyName = "F1"
	keyF2         keyName = "F2"
	keyF3         keyName = "F3"
	keyF4         keyName = "F4"
	keyF5         keyName = "F5"
	keyF6         keyName = "F6"
	keyF7         keyName = "F7"
	keyF8         keyName = "F8"
	keyF9         keyName = "F9"
	keyF10        keyName = "F10"
	keyF11        keyName = "F11"
	keyF12        keyName = "F12"
)

func normalizeKeys(keys []string) ([]keyName, error) {
	normalized := make([]keyName, 0, len(keys))
	for _, key := range keys {
		upper := strings.ToUpper(strings.TrimSpace(key))
		switch upper {
		case "CONTROL", "CTRL":
			upper = string(keyControl)
		case "OPTION", "ALT":
			upper = string(keyAlt)
		case "COMMAND", "CMD", "META":
			upper = string(keyMeta)
		case "ESCAPE", "ESC":
			upper = string(keyEscape)
		case "RETURN", "ENTER":
			upper = string(keyEnter)
		case "DEL", "DELETE":
			upper = string(keyDelete)
		case "UP", "ARROWUP":
			upper = string(keyArrowUp)
		case "DOWN", "ARROWDOWN":
			upper = string(keyArrowDown)
		case "LEFT", "ARROWLEFT":
			upper = string(keyArrowLeft)
		case "RIGHT", "ARROWRIGHT":
			upper = string(keyArrowRight)
		}
		if !isSupportedKey(keyName(upper)) {
			return nil, fmt.Errorf("unsupported key %q", key)
		}
		normalized = append(normalized, keyName(upper))
	}
	return normalized, nil
}

func splitKeyChord(keys []string) (keyName, []keyName, error) {
	normalized, err := normalizeKeys(keys)
	if err != nil {
		return "", nil, err
	}
	if len(normalized) == 0 {
		return "", nil, fmt.Errorf("keypress action requires keys")
	}
	for _, key := range normalized[:len(normalized)-1] {
		if !isModifierKey(key) {
			return "", nil, fmt.Errorf("keypress chord requires modifier keys before the primary key")
		}
	}
	return normalized[len(normalized)-1], append([]keyName(nil), normalized[:len(normalized)-1]...), nil
}

func isModifierKey(key keyName) bool {
	switch key {
	case keyControl, keyAlt, keyShift, keyMeta:
		return true
	default:
		return false
	}
}

func normalizeModifiers(keys []string) ([]keyName, error) {
	normalized, err := normalizeKeys(keys)
	if err != nil {
		return nil, err
	}
	for _, key := range normalized {
		switch key {
		case keyControl, keyAlt, keyShift, keyMeta:
		default:
			return nil, fmt.Errorf("mouse action key %q is not a modifier", key)
		}
	}
	return normalized, nil
}

func isSupportedKey(key keyName) bool {
	if len(key) == 1 {
		value := key[0]
		return value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
	}
	switch key {
	case keyControl, keyAlt, keyShift, keyMeta,
		keyArrowUp, keyArrowDown, keyArrowLeft, keyArrowRight,
		keyEnter, keyTab, keyEscape, keyBackspace, keyDelete,
		keyHome, keyEnd, keyPageUp, keyPageDown, keySpace:
		return true
	default:
		return strings.HasPrefix(string(key), "F") && validFunctionKey(string(key))
	}
}

func validFunctionKey(key string) bool {
	switch key {
	case "F1", "F2", "F3", "F4", "F5", "F6", "F7", "F8", "F9", "F10", "F11", "F12":
		return true
	default:
		return false
	}
}

type mouseStepType string

const (
	mouseStepMove   mouseStepType = "move"
	mouseStepDown   mouseStepType = "down"
	mouseStepUp     mouseStepType = "up"
	mouseStepDrag   mouseStepType = "drag"
	mouseStepScroll mouseStepType = "scroll"
)

type mouseStep struct {
	Type    mouseStepType
	X       int
	Y       int
	ScrollX int
	ScrollY int
	Click   int
}

func expandMouseAction(action MouseAction) ([]mouseStep, error) {
	point := mouseStep{X: action.X, Y: action.Y}
	switch action.Type {
	case MouseActionMove:
		point.Type = mouseStepMove
		return []mouseStep{point}, nil
	case MouseActionDown:
		point.Type = mouseStepDown
		return []mouseStep{point}, nil
	case MouseActionUp:
		point.Type = mouseStepUp
		return []mouseStep{point}, nil
	case MouseActionClick:
		return clickSteps(action.X, action.Y, 1), nil
	case MouseActionDoubleClick:
		return clickSteps(action.X, action.Y, 2), nil
	case MouseActionScroll:
		return []mouseStep{
			{Type: mouseStepMove, X: action.X, Y: action.Y},
			{Type: mouseStepScroll, X: action.X, Y: action.Y, ScrollX: action.ScrollX, ScrollY: action.ScrollY},
		}, nil
	case MouseActionDrag:
		if len(action.Path) < 2 {
			return nil, fmt.Errorf("drag action requires at least two path points")
		}
		steps := make([]mouseStep, 0, len(action.Path)+2)
		start := action.Path[0]
		steps = append(steps,
			mouseStep{Type: mouseStepMove, X: start.X, Y: start.Y},
			mouseStep{Type: mouseStepDown, X: start.X, Y: start.Y},
		)
		for _, item := range action.Path[1:] {
			steps = append(steps, mouseStep{Type: mouseStepDrag, X: item.X, Y: item.Y})
		}
		end := action.Path[len(action.Path)-1]
		steps = append(steps, mouseStep{Type: mouseStepUp, X: end.X, Y: end.Y})
		return steps, nil
	default:
		return nil, fmt.Errorf("unsupported mouse action %q", action.Type)
	}
}

func clickSteps(x, y, count int) []mouseStep {
	steps := []mouseStep{{Type: mouseStepMove, X: x, Y: y}}
	for click := 1; click <= count; click++ {
		steps = append(steps,
			mouseStep{Type: mouseStepDown, X: x, Y: y, Click: click},
			mouseStep{Type: mouseStepUp, X: x, Y: y, Click: click},
		)
	}
	return steps
}

func nativeWheelDeltas(scrollX, scrollY int) (int, int) {
	return scrollX, -scrollY
}

func validateAction(action Action) error {
	switch action.Type {
	case ActionClick, ActionDoubleClick, ActionMove, ActionScroll:
		if _, err := mouseButtonCode(action.Button); err != nil {
			return err
		}
		_, err := normalizeModifiers(action.Keys)
		return err
	case ActionDrag:
		if len(action.Path) < 2 {
			return fmt.Errorf("drag action requires at least two path points")
		}
		if _, err := mouseButtonCode(action.Button); err != nil {
			return err
		}
		_, err := normalizeModifiers(action.Keys)
		return err
	case ActionType:
		if action.Text == "" {
			return fmt.Errorf("type action requires text")
		}
		return nil
	case ActionKeypress:
		if len(action.Keys) == 0 {
			return fmt.Errorf("keypress action requires keys")
		}
		_, _, err := splitKeyChord(action.Keys)
		return err
	case ActionWait, ActionScreenshot:
		return nil
	default:
		return fmt.Errorf("unsupported computer action %q", action.Type)
	}
}

func ValidateActions(actions []Action) error {
	for index, action := range actions {
		if err := validateAction(action); err != nil {
			return fmt.Errorf("action %d: %w", index, err)
		}
	}
	return nil
}

func defaultActionWait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
