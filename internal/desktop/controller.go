package desktop

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type MouseActionType string

const (
	MouseActionMove        MouseActionType = "move"
	MouseActionDown        MouseActionType = "down"
	MouseActionUp          MouseActionType = "up"
	MouseActionClick       MouseActionType = "click"
	MouseActionDoubleClick MouseActionType = "double_click"
	MouseActionDrag        MouseActionType = "drag"
	MouseActionScroll      MouseActionType = "scroll"
)

type MouseButton string

const (
	MouseButtonLeft   MouseButton = "left"
	MouseButtonRight  MouseButton = "right"
	MouseButtonCenter MouseButton = "center"
	MouseButtonWheel  MouseButton = "wheel"
)

type AppActionType string

const (
	AppActionActivate AppActionType = "activate"
	AppActionLaunch   AppActionType = "launch"
	AppActionQuit     AppActionType = "quit"
)

type MouseAction struct {
	Type    MouseActionType `json:"type"`
	X       int             `json:"x,omitempty"`
	Y       int             `json:"y,omitempty"`
	Button  MouseButton     `json:"button,omitempty"`
	Keys    []string        `json:"keys,omitempty"`
	Path    []Point         `json:"path,omitempty"`
	ScrollX int             `json:"scroll_x,omitempty"`
	ScrollY int             `json:"scroll_y,omitempty"`
}

type KeyboardAction struct {
	Text      string   `json:"text,omitempty"`
	KeyCode   int      `json:"key_code,omitempty"`
	Modifiers []string `json:"modifiers,omitempty"`
	Keys      []string `json:"keys,omitempty"`
}

type AppAction struct {
	Type AppActionType
	Name string
}

type Window struct {
	App   string `json:"app"`
	Title string `json:"title"`
	ID    int    `json:"id,omitempty"`
	PID   int    `json:"pid,omitempty"`
}

type AccessibilityWindow struct {
	Title string `json:"title"`
	Role  string `json:"role"`
}

type AccessibilityTree struct {
	Application string                `json:"application"`
	Windows     []AccessibilityWindow `json:"windows"`
}

type UnavailableError struct {
	Reason string
	Cause  error
}

func (e *UnavailableError) Error() string {
	if e == nil {
		return "desktop unavailable"
	}
	if e.Cause != nil {
		return fmt.Sprintf("desktop unavailable: %s: %v", e.Reason, e.Cause)
	}
	return fmt.Sprintf("desktop unavailable: %s", e.Reason)
}

func (e *UnavailableError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func IsUnavailable(err error) bool {
	var unavailable *UnavailableError
	return errors.As(err, &unavailable)
}

type backend interface {
	Screenshot(ctx context.Context, path string) error
	Windows(ctx context.Context) ([]Window, error)
	Accessibility(ctx context.Context) (AccessibilityTree, error)
	Mouse(ctx context.Context, action MouseAction) error
	Keyboard(ctx context.Context, action KeyboardAction) error
	App(ctx context.Context, action AppAction) error
	Available(ctx context.Context) bool
}

type Controller struct {
	backend backend
	wait    func(context.Context, time.Duration) error
}

func NewController() *Controller {
	return &Controller{backend: defaultBackend(), wait: defaultActionWait}
}

func (c *Controller) Screenshot(ctx context.Context, path string) error {
	return c.backend.Screenshot(ctx, path)
}

func (c *Controller) Windows(ctx context.Context) ([]Window, error) {
	return c.backend.Windows(ctx)
}

func (c *Controller) Accessibility(ctx context.Context) (AccessibilityTree, error) {
	return c.backend.Accessibility(ctx)
}

func (c *Controller) Mouse(ctx context.Context, action MouseAction) error {
	return c.backend.Mouse(ctx, action)
}

func (c *Controller) Keyboard(ctx context.Context, action KeyboardAction) error {
	return c.backend.Keyboard(ctx, action)
}

func (c *Controller) App(ctx context.Context, action AppAction) error {
	return c.backend.App(ctx, action)
}

func (c *Controller) Available(ctx context.Context) bool {
	return c.backend.Available(ctx)
}

func (c *Controller) Actions(ctx context.Context, actions []Action) error {
	if err := ValidateActions(actions); err != nil {
		return err
	}
	if preflight, ok := c.backend.(interface{ PreflightActions([]Action) error }); ok {
		if err := preflight.PreflightActions(actions); err != nil {
			return err
		}
	}
	for index, action := range actions {
		if err := ctx.Err(); err != nil {
			return err
		}
		var err error
		switch action.Type {
		case ActionClick, ActionDoubleClick, ActionMove, ActionDrag, ActionScroll:
			types := map[ActionKind]MouseActionType{
				ActionClick:       MouseActionClick,
				ActionDoubleClick: MouseActionDoubleClick,
				ActionMove:        MouseActionMove,
				ActionDrag:        MouseActionDrag,
				ActionScroll:      MouseActionScroll,
			}
			err = c.backend.Mouse(ctx, MouseAction{
				Type: types[action.Type], X: action.X, Y: action.Y, Button: action.Button,
				Keys: append([]string(nil), action.Keys...), Path: append([]Point(nil), action.Path...),
				ScrollX: action.ScrollX, ScrollY: action.ScrollY,
			})
		case ActionType:
			err = c.backend.Keyboard(ctx, KeyboardAction{Text: action.Text})
		case ActionKeypress:
			err = c.backend.Keyboard(ctx, KeyboardAction{Keys: append([]string(nil), action.Keys...)})
		case ActionWait:
			wait := c.wait
			if wait == nil {
				wait = defaultActionWait
			}
			err = wait(ctx, 2*time.Second)
		case ActionScreenshot:
			// The caller captures and returns the screen at this batch boundary.
		}
		if err != nil {
			return fmt.Errorf("action %d (%s): %w", index, action.Type, err)
		}
	}
	return nil
}

type staticUnavailableBackend struct {
	reason string
}

func (b staticUnavailableBackend) Screenshot(ctx context.Context, path string) error {
	return &UnavailableError{Reason: b.reason}
}

func (b staticUnavailableBackend) Windows(ctx context.Context) ([]Window, error) {
	return nil, &UnavailableError{Reason: b.reason}
}

func (b staticUnavailableBackend) Accessibility(ctx context.Context) (AccessibilityTree, error) {
	return AccessibilityTree{}, &UnavailableError{Reason: b.reason}
}

func (b staticUnavailableBackend) Mouse(ctx context.Context, action MouseAction) error {
	return &UnavailableError{Reason: b.reason}
}

func (b staticUnavailableBackend) Keyboard(ctx context.Context, action KeyboardAction) error {
	return &UnavailableError{Reason: b.reason}
}

func (b staticUnavailableBackend) App(ctx context.Context, action AppAction) error {
	return &UnavailableError{Reason: b.reason}
}

func (b staticUnavailableBackend) Available(ctx context.Context) bool {
	return false
}
