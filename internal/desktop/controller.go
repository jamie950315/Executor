package desktop

import (
	"context"
	"errors"
	"fmt"
)

type MouseActionType string

const (
	MouseActionMove  MouseActionType = "move"
	MouseActionDown  MouseActionType = "down"
	MouseActionUp    MouseActionType = "up"
	MouseActionClick MouseActionType = "click"
)

type MouseButton string

const (
	MouseButtonLeft   MouseButton = "left"
	MouseButtonRight  MouseButton = "right"
	MouseButtonCenter MouseButton = "center"
)

type AppActionType string

const (
	AppActionActivate AppActionType = "activate"
	AppActionLaunch   AppActionType = "launch"
	AppActionQuit     AppActionType = "quit"
)

type MouseAction struct {
	Type   MouseActionType
	X      int
	Y      int
	Button MouseButton
}

type KeyboardAction struct {
	Text      string
	KeyCode   int
	Modifiers []string
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
}

func NewController() *Controller {
	return &Controller{backend: defaultBackend()}
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
