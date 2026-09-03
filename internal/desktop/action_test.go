package desktop

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestControllerActionsExecutesComputerUseBatchInOrder(t *testing.T) {
	backend := &recordingActionBackend{}
	waits := make([]time.Duration, 0, 1)
	controller := &Controller{
		backend: backend,
		wait: func(ctx context.Context, duration time.Duration) error {
			waits = append(waits, duration)
			return nil
		},
	}

	actions := []Action{
		{Type: ActionClick, X: 10, Y: 20, Button: MouseButtonRight, Keys: []string{"CTRL"}},
		{Type: ActionDoubleClick, X: 30, Y: 40, Keys: []string{"SHIFT"}},
		{Type: ActionMove, X: 50, Y: 60, Keys: []string{"ALT"}},
		{Type: ActionDrag, Path: []Point{{X: 1, Y: 2}, {X: 3, Y: 4}, {X: 5, Y: 6}}, Keys: []string{"META"}},
		{Type: ActionScroll, X: 70, Y: 80, ScrollX: -90, ScrollY: 120, Keys: []string{"CTRL"}},
		{Type: ActionType, Text: "hello"},
		{Type: ActionKeypress, Keys: []string{"CTRL", "L"}},
		{Type: ActionWait},
		{Type: ActionScreenshot},
	}

	if err := controller.Actions(context.Background(), actions); err != nil {
		t.Fatalf("Actions: %v", err)
	}

	want := []recordedAction{
		{kind: "mouse", mouse: MouseAction{Type: MouseActionClick, X: 10, Y: 20, Button: MouseButtonRight, Keys: []string{"CTRL"}}},
		{kind: "mouse", mouse: MouseAction{Type: MouseActionDoubleClick, X: 30, Y: 40, Keys: []string{"SHIFT"}}},
		{kind: "mouse", mouse: MouseAction{Type: MouseActionMove, X: 50, Y: 60, Keys: []string{"ALT"}}},
		{kind: "mouse", mouse: MouseAction{Type: MouseActionDrag, Path: []Point{{X: 1, Y: 2}, {X: 3, Y: 4}, {X: 5, Y: 6}}, Keys: []string{"META"}}},
		{kind: "mouse", mouse: MouseAction{Type: MouseActionScroll, X: 70, Y: 80, ScrollX: -90, ScrollY: 120, Keys: []string{"CTRL"}}},
		{kind: "keyboard", keyboard: KeyboardAction{Text: "hello"}},
		{kind: "keyboard", keyboard: KeyboardAction{Keys: []string{"CTRL", "L"}}},
	}
	if !reflect.DeepEqual(backend.actions, want) {
		t.Fatalf("unexpected action sequence:\n got: %#v\nwant: %#v", backend.actions, want)
	}
	if !reflect.DeepEqual(waits, []time.Duration{2 * time.Second}) {
		t.Fatalf("unexpected waits: %#v", waits)
	}
}

func TestControllerActionsTreatsScreenshotAsDispatcherCaptureBoundary(t *testing.T) {
	backend := &recordingActionBackend{}
	controller := &Controller{backend: backend}
	if err := controller.Actions(context.Background(), []Action{{Type: ActionScreenshot}}); err != nil {
		t.Fatalf("Actions screenshot: %v", err)
	}
	if len(backend.actions) != 0 {
		t.Fatalf("screenshot action unexpectedly emitted input: %#v", backend.actions)
	}
}

func TestControllerActionsRejectsUnknownActionBeforeExecutingBatch(t *testing.T) {
	backend := &recordingActionBackend{}
	controller := &Controller{backend: backend}

	err := controller.Actions(context.Background(), []Action{
		{Type: ActionClick, X: 1, Y: 2},
		{Type: ActionKind("triple_click")},
	})
	if err == nil {
		t.Fatal("expected unknown action error")
	}
	if len(backend.actions) != 0 {
		t.Fatalf("batch partially executed before validation: %#v", backend.actions)
	}
}

func TestControllerActionsPreflightsPlatformBeforeExecutingBatch(t *testing.T) {
	wantErr := &UnavailableError{Reason: "scroll unsupported"}
	backend := &recordingActionBackend{preflightErr: wantErr}
	controller := &Controller{backend: backend}
	err := controller.Actions(context.Background(), []Action{
		{Type: ActionClick, X: 1, Y: 2},
		{Type: ActionScroll, X: 3, Y: 4, ScrollY: 100},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Actions error = %v, want %v", err, wantErr)
	}
	if len(backend.actions) != 0 {
		t.Fatalf("batch partially executed before platform preflight: %#v", backend.actions)
	}
}

func TestValidateActionsRejectsInvalidLaterAction(t *testing.T) {
	err := ValidateActions([]Action{
		{Type: ActionMove, X: 1, Y: 2},
		{Type: ActionDrag, Path: []Point{{X: 3, Y: 4}}},
	})
	if err == nil {
		t.Fatal("expected whole-batch validation error")
	}
}

func TestValidateActionsRejectsUnknownMouseButtonAndNonModifierKeys(t *testing.T) {
	if err := ValidateActions([]Action{{
		Type: ActionClick, Button: MouseButton("back"), X: 1, Y: 2,
	}}); err == nil {
		t.Fatal("expected unsupported mouse button error")
	}
	if err := ValidateActions([]Action{{
		Type: ActionClick, X: 1, Y: 2, Keys: []string{"L"},
	}}); err == nil {
		t.Fatal("expected non-modifier mouse key error")
	}
}

func TestControllerActionsRejectsDragWithFewerThanTwoPoints(t *testing.T) {
	controller := &Controller{backend: &recordingActionBackend{}}
	err := controller.Actions(context.Background(), []Action{{
		Type: ActionDrag,
		Path: []Point{{X: 1, Y: 2}},
	}})
	if err == nil {
		t.Fatal("expected invalid drag path error")
	}
}

func TestExpandMouseActionPreservesDragDownMoveUpOrder(t *testing.T) {
	steps, err := expandMouseAction(MouseAction{
		Type: MouseActionDrag,
		Path: []Point{{X: 10, Y: 20}, {X: 30, Y: 40}, {X: 50, Y: 60}},
	})
	if err != nil {
		t.Fatalf("expandMouseAction: %v", err)
	}
	want := []mouseStep{
		{Type: mouseStepMove, X: 10, Y: 20},
		{Type: mouseStepDown, X: 10, Y: 20},
		{Type: mouseStepDrag, X: 30, Y: 40},
		{Type: mouseStepDrag, X: 50, Y: 60},
		{Type: mouseStepUp, X: 50, Y: 60},
	}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("unexpected drag steps:\n got: %#v\nwant: %#v", steps, want)
	}
}

func TestExpandMouseActionProducesTwoCompleteClicks(t *testing.T) {
	steps, err := expandMouseAction(MouseAction{Type: MouseActionDoubleClick, X: 10, Y: 20})
	if err != nil {
		t.Fatalf("expandMouseAction: %v", err)
	}
	wantTypes := []mouseStepType{mouseStepMove, mouseStepDown, mouseStepUp, mouseStepDown, mouseStepUp}
	gotTypes := make([]mouseStepType, 0, len(steps))
	for _, step := range steps {
		gotTypes = append(gotTypes, step.Type)
	}
	if !reflect.DeepEqual(gotTypes, wantTypes) {
		t.Fatalf("unexpected double-click steps: %#v", gotTypes)
	}
}

func TestExpandMouseActionPreservesBothScrollAxes(t *testing.T) {
	steps, err := expandMouseAction(MouseAction{Type: MouseActionScroll, X: 10, Y: 20, ScrollX: -30, ScrollY: 40})
	if err != nil {
		t.Fatalf("expandMouseAction: %v", err)
	}
	want := []mouseStep{
		{Type: mouseStepMove, X: 10, Y: 20},
		{Type: mouseStepScroll, X: 10, Y: 20, ScrollX: -30, ScrollY: 40},
	}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("unexpected scroll steps: %#v", steps)
	}
}

func TestNormalizeKeysMapsComputerUseNamesAndRejectsUnknown(t *testing.T) {
	got, err := normalizeKeys([]string{"CTRL", "ALT", "SHIFT", "META", "LEFT", "RETURN", "DEL", "a"})
	if err != nil {
		t.Fatalf("normalizeKeys: %v", err)
	}
	want := []keyName{keyControl, keyAlt, keyShift, keyMeta, keyArrowLeft, keyEnter, keyDelete, "A"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeKeys() = %#v, want %#v", got, want)
	}
	if _, err := normalizeKeys([]string{"HYPERSPACE"}); err == nil {
		t.Fatal("expected unsupported key error")
	}
}

func TestValidateActionsRejectsMultiplePrimaryKeysInChord(t *testing.T) {
	err := ValidateActions([]Action{{Type: ActionKeypress, Keys: []string{"A", "B"}}})
	if err == nil {
		t.Fatal("keypress with multiple primary keys succeeded")
	}
}

func TestBuildX11MouseCommandsHoldsModifiersAcrossDrag(t *testing.T) {
	commands, err := buildX11MouseCommands(MouseAction{
		Type: MouseActionDrag,
		Path: []Point{{X: 10, Y: 20}, {X: 30, Y: 40}},
		Keys: []string{"CTRL", "SHIFT"},
	})
	if err != nil {
		t.Fatalf("buildX11MouseCommands: %v", err)
	}
	want := [][]string{
		{"keydown", "ctrl"},
		{"keydown", "shift"},
		{"mousemove", "10", "20"},
		{"mousedown", "1"},
		{"mousemove", "30", "40"},
		{"mouseup", "1"},
		{"keyup", "shift"},
		{"keyup", "ctrl"},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("unexpected xdotool commands:\n got: %#v\nwant: %#v", commands, want)
	}
}

func TestX11FailureCleanupReleasesMouseAndModifiers(t *testing.T) {
	commands := [][]string{
		{"keydown", "ctrl"},
		{"mousemove", "10", "20"},
		{"mousedown", "1"},
		{"mousemove", "30", "40"},
		{"mouseup", "1"},
		{"keyup", "ctrl"},
	}
	want := [][]string{{"mouseup", "1"}, {"keyup", "ctrl"}}
	if got := x11FailureCleanup(commands, 3); !reflect.DeepEqual(got, want) {
		t.Fatalf("x11 failure cleanup = %#v, want %#v", got, want)
	}
	if got := x11FailureCleanup(commands, 4); !reflect.DeepEqual(got, want) {
		t.Fatalf("failed mouseup cleanup = %#v, want %#v", got, want)
	}
	if got := x11FailureCleanup(commands, 2); !reflect.DeepEqual(got, want) {
		t.Fatalf("possibly injected mousedown cleanup = %#v, want %#v", got, want)
	}
	wantBeforeMouseDown := [][]string{{"keyup", "ctrl"}}
	if got := x11FailureCleanup(commands, 1); !reflect.DeepEqual(got, wantBeforeMouseDown) {
		t.Fatalf("pre-mousedown cleanup = %#v, want %#v", got, wantBeforeMouseDown)
	}
}

func TestRunX11FailureCleanupUsesFreshBoundedContexts(t *testing.T) {
	runner := &cleanupContextRunner{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runX11FailureCleanup(ctx, runner, [][]string{
		{"keydown", "ctrl"},
		{"mousedown", "1"},
		{"mouseup", "1"},
		{"keyup", "ctrl"},
	}, 2)
	if len(runner.calls) != 2 {
		t.Fatalf("cleanup calls = %d, want 2", len(runner.calls))
	}
	for _, call := range runner.calls {
		if !call.hasDeadline || call.contextErr != nil {
			t.Fatalf("cleanup context = %#v, want active bounded context", call)
		}
	}
}

func TestX11LegacyKeyboardCommandsHoldModifiersUntilKeyRelease(t *testing.T) {
	commands, err := buildX11LegacyKeyboardCommands(KeyboardAction{
		KeyCode: 65, Modifiers: []string{"CTRL", "SHIFT"},
	})
	if err != nil {
		t.Fatalf("buildX11LegacyKeyboardCommands: %v", err)
	}
	want := [][]string{
		{"keydown", "ctrl"},
		{"keydown", "shift"},
		{"key", "65"},
		{"keyup", "shift"},
		{"keyup", "ctrl"},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("legacy X11 key commands = %#v, want %#v", commands, want)
	}
}

func TestBuildX11MouseCommandsPreservesHorizontalAndVerticalScroll(t *testing.T) {
	commands, err := buildX11MouseCommands(MouseAction{
		Type: MouseActionScroll, X: 10, Y: 20, ScrollX: -90, ScrollY: 220,
	})
	if err != nil {
		t.Fatalf("buildX11MouseCommands: %v", err)
	}
	want := [][]string{
		{"mousemove", "10", "20"},
		{"click", "5"},
		{"click", "5"},
		{"click", "6"},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("unexpected xdotool scroll commands:\n got: %#v\nwant: %#v", commands, want)
	}
}

func TestWaylandRejectsAdvancedMouseActionInsteadOfSilentlySucceeding(t *testing.T) {
	_, err := chooseWaylandMouseCommand(availableTools{"ydotool": true}, MouseAction{
		Type: MouseActionScroll, ScrollY: 100,
	})
	if !IsUnavailable(err) {
		t.Fatalf("expected UnavailableError, got %v", err)
	}
	_, err = chooseWaylandMouseCommand(availableTools{"ydotool": true}, MouseAction{
		Type: MouseActionClick, X: 1, Y: 2, Keys: []string{"CTRL"},
	})
	if !IsUnavailable(err) {
		t.Fatalf("expected modifier UnavailableError, got %v", err)
	}
}

func TestWindowsMouseScriptIncludesModifiersDoubleClickDragAndScroll(t *testing.T) {
	doubleClick := buildWindowsMouseScript(MouseAction{
		Type: MouseActionDoubleClick, X: 10, Y: 20, Keys: []string{"CTRL"},
	})
	if strings.Count(doubleClick, "mouse_event(2,0,0,0,[UIntPtr]::Zero)") != 2 ||
		strings.Count(doubleClick, "mouse_event(4,0,0,0,[UIntPtr]::Zero)") != 2 {
		t.Fatalf("double click does not contain two down/up pairs: %s", doubleClick)
	}
	if strings.Index(doubleClick, "keybd_event(0x11,0,0,[UIntPtr]::Zero)") > strings.Index(doubleClick, "mouse_event(2,0,0,0,[UIntPtr]::Zero)") ||
		strings.Index(doubleClick, "keybd_event(0x11,0,2,[UIntPtr]::Zero)") < strings.LastIndex(doubleClick, "mouse_event(4,0,0,0,[UIntPtr]::Zero)") {
		t.Fatalf("modifier is not held for complete double click: %s", doubleClick)
	}

	drag := buildWindowsMouseScript(MouseAction{
		Type: MouseActionDrag, Path: []Point{{X: 1, Y: 2}, {X: 3, Y: 4}},
	})
	down := strings.Index(drag, "SetCursorPos(1,2)")
	down = strings.Index(drag[down:], "mouse_event(2,0,0,0,[UIntPtr]::Zero)") + down
	move := strings.Index(drag, "SetCursorPos(3,4)")
	up := strings.LastIndex(drag, "mouse_event(4,0,0,0,[UIntPtr]::Zero)")
	if !(down >= 0 && down < move && move < up) {
		t.Fatalf("drag ordering is not down -> move -> up: %s", drag)
	}

	scroll := buildWindowsMouseScript(MouseAction{
		Type: MouseActionScroll, X: 5, Y: 6, ScrollX: -30, ScrollY: 40,
	})
	if !strings.Contains(scroll, "mouse_event(2048,0,0,-40,[UIntPtr]::Zero)") ||
		!strings.Contains(scroll, "mouse_event(4096,0,0,-30,[UIntPtr]::Zero)") {
		t.Fatalf("scroll script lost an axis or inverted Computer Use direction: %s", scroll)
	}
}

func TestNativeWheelDeltasInvertComputerUseVerticalDirection(t *testing.T) {
	x, y := nativeWheelDeltas(-30, 40)
	if x != -30 || y != -40 {
		t.Fatalf("native wheel deltas = (%d,%d), want (-30,-40)", x, y)
	}
}

func TestWindowsScreenshotUsesPrimaryDisplayCoordinateSpace(t *testing.T) {
	script := buildWindowsScreenshotScript(`C:\Temp\shot.png`)
	if !strings.Contains(script, "[System.Windows.Forms.Screen]::PrimaryScreen.Bounds") {
		t.Fatalf("screenshot does not use primary display bounds: %s", script)
	}
	if strings.Contains(script, "SystemInformation]::VirtualScreen") {
		t.Fatalf("screenshot combines displays without mapping virtual-screen origins: %s", script)
	}
}

func TestWindowsKeyboardScriptMapsComputerUseKeyNames(t *testing.T) {
	script := buildWindowsKeyboardScript(KeyboardAction{Keys: []string{"CTRL", "ARROWLEFT", "ENTER"}})
	for _, virtualKey := range []string{"0x11", "0x25", "0x0D"} {
		if !strings.Contains(script, "keybd_event("+virtualKey+",0,0,[UIntPtr]::Zero)") ||
			!strings.Contains(script, "keybd_event("+virtualKey+",0,2,[UIntPtr]::Zero)") {
			t.Fatalf("key %s was not pressed and released: %s", virtualKey, script)
		}
	}
}

func TestWindowsKeyboardScriptHoldsChordUntilPrimaryKeyIsReleased(t *testing.T) {
	script := buildWindowsKeyboardScript(KeyboardAction{Keys: []string{"CTRL", "SHIFT", "L"}})
	downControl := strings.Index(script, "keybd_event(0x11,0,0,[UIntPtr]::Zero)")
	downShift := strings.Index(script, "keybd_event(0x10,0,0,[UIntPtr]::Zero)")
	downL := strings.Index(script, "keybd_event(76,0,0,[UIntPtr]::Zero)")
	upL := strings.Index(script, "keybd_event(76,0,2,[UIntPtr]::Zero)")
	upShift := strings.Index(script, "keybd_event(0x10,0,2,[UIntPtr]::Zero)")
	upControl := strings.Index(script, "keybd_event(0x11,0,2,[UIntPtr]::Zero)")
	if !(downControl >= 0 && downControl < downShift && downShift < downL && downL < upL && upL < upShift && upShift < upControl) {
		t.Fatalf("keypress is not a held chord: %s", script)
	}
}

func TestWindowsLegacyKeyboardScriptUsesExplicitUIntPtrZero(t *testing.T) {
	script := buildWindowsKeyboardScript(KeyboardAction{KeyCode: 65, Modifiers: []string{"CTRL", "META"}})
	for _, event := range []string{
		"keybd_event(0x11,0,0,[UIntPtr]::Zero)",
		"keybd_event(0x5B,0,0,[UIntPtr]::Zero)",
		"keybd_event(65,0,0,[UIntPtr]::Zero)",
		"keybd_event(65,0,2,[UIntPtr]::Zero)",
		"keybd_event(0x5B,0,2,[UIntPtr]::Zero)",
		"keybd_event(0x11,0,2,[UIntPtr]::Zero)",
	} {
		if !strings.Contains(script, event) {
			t.Fatalf("legacy keyboard event %q does not pass an explicit UIntPtr: %s", event, script)
		}
	}
}

func TestValidateWindowsLegacyKeyboardActionRejectsInvalidInput(t *testing.T) {
	for _, action := range []KeyboardAction{
		{KeyCode: -1},
		{KeyCode: 256},
		{KeyCode: 65, Modifiers: []string{"unsupported"}},
	} {
		if err := validateWindowsLegacyKeyboardAction(action); err == nil {
			t.Fatalf("invalid Windows legacy keyboard action was accepted: %#v", action)
		}
	}
	if err := validateWindowsLegacyKeyboardAction(KeyboardAction{KeyCode: 65, Modifiers: []string{"CTRL", "META"}}); err != nil {
		t.Fatalf("valid Windows legacy keyboard action rejected: %v", err)
	}
}

func TestXdotoolKeyChordUsesSingleCombinedKeypress(t *testing.T) {
	got, err := buildX11KeypressCommand([]string{"CTRL", "SHIFT", "L"})
	if err != nil {
		t.Fatalf("buildX11KeypressCommand: %v", err)
	}
	want := []string{"key", "ctrl+shift+L"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("xdotool keypress = %#v, want %#v", got, want)
	}
}

func TestWaylandKeypressHoldsKeysAndReleasesInReverse(t *testing.T) {
	got, err := chooseWaylandKeyboardCommand(availableTools{"ydotool": true}, KeyboardAction{Keys: []string{"CTRL", "L"}})
	if err != nil {
		t.Fatalf("chooseWaylandKeyboardCommand: %v", err)
	}
	want := []string{"ydotool", "key", "29:1", "38:1", "38:0", "29:0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ydotool keypress = %#v, want %#v", got, want)
	}
}

func TestWaylandLegacyKeyboardCommandHoldsModifiersUntilKeyRelease(t *testing.T) {
	got, err := chooseWaylandKeyboardCommand(availableTools{"ydotool": true}, KeyboardAction{
		KeyCode: 30, Modifiers: []string{"CTRL"},
	})
	if err != nil {
		t.Fatalf("chooseWaylandKeyboardCommand: %v", err)
	}
	want := []string{"ydotool", "key", "29:1", "30:1", "30:0", "29:0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy Wayland key command = %#v, want %#v", got, want)
	}
}

func TestControllerActionsStopsAfterBackendFailure(t *testing.T) {
	wantErr := errors.New("input failed")
	backend := &recordingActionBackend{failAt: 1, err: wantErr}
	controller := &Controller{backend: backend}
	err := controller.Actions(context.Background(), []Action{
		{Type: ActionMove, X: 1, Y: 2},
		{Type: ActionClick, X: 3, Y: 4},
		{Type: ActionType, Text: "must not run"},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Actions error = %v, want %v", err, wantErr)
	}
	if len(backend.actions) != 2 {
		t.Fatalf("executed %d actions, want 2", len(backend.actions))
	}
}

func TestControllerActionsDoesNotStartAfterContextCancellation(t *testing.T) {
	backend := &recordingActionBackend{}
	controller := &Controller{backend: backend}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := controller.Actions(ctx, []Action{{Type: ActionClick, X: 1, Y: 2}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Actions error = %v, want context cancellation", err)
	}
	if len(backend.actions) != 0 {
		t.Fatalf("canceled batch executed actions: %#v", backend.actions)
	}
}

type recordedAction struct {
	kind     string
	mouse    MouseAction
	keyboard KeyboardAction
}

type recordingActionBackend struct {
	actions      []recordedAction
	failAt       int
	err          error
	preflightErr error
}

type cleanupContextCall struct {
	hasDeadline bool
	contextErr  error
}

type cleanupContextRunner struct {
	calls []cleanupContextCall
}

func (r *cleanupContextRunner) Run(ctx context.Context, _ string, _ ...string) ([]byte, error) {
	_, hasDeadline := ctx.Deadline()
	r.calls = append(r.calls, cleanupContextCall{hasDeadline: hasDeadline, contextErr: ctx.Err()})
	return nil, nil
}

func (b *recordingActionBackend) PreflightActions([]Action) error { return b.preflightErr }

func (b *recordingActionBackend) Screenshot(context.Context, string) error  { return nil }
func (b *recordingActionBackend) Windows(context.Context) ([]Window, error) { return nil, nil }
func (b *recordingActionBackend) Accessibility(context.Context) (AccessibilityTree, error) {
	return AccessibilityTree{}, nil
}
func (b *recordingActionBackend) Mouse(_ context.Context, action MouseAction) error {
	b.actions = append(b.actions, recordedAction{kind: "mouse", mouse: action})
	if b.err != nil && len(b.actions)-1 == b.failAt {
		return b.err
	}
	return nil
}
func (b *recordingActionBackend) Keyboard(_ context.Context, action KeyboardAction) error {
	b.actions = append(b.actions, recordedAction{kind: "keyboard", keyboard: action})
	if b.err != nil && len(b.actions)-1 == b.failAt {
		return b.err
	}
	return nil
}
func (b *recordingActionBackend) App(context.Context, AppAction) error { return nil }
func (b *recordingActionBackend) Available(context.Context) bool       { return true }
