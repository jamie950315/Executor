//go:build darwin

package desktop

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestDarwinBackend_UsesExpectedCommands(t *testing.T) {
	runner := &fakeRunner{
		outputs: map[string][]byte{
			"osascript|-l|JavaScript|-e|ObjC.import('Foundation'); var se = Application('System Events'); var apps = se.applicationProcesses.whose({backgroundOnly: false})(); JSON.stringify(apps.map(function(app) { try { var appName = ''; try { appName = app.name(); } catch (error) {} var wins = []; try { wins = app.windows().map(function(win) { var title = ''; var id = 0; try { title = win.name() || ''; } catch (error) {} try { id = win.id() || 0; } catch (error) {} return {title: title, id: id}; }); } catch (error) { wins = []; } return {app: appName, windows: wins}; } catch (error) { return {app: '', windows: []}; } }));": []byte(`[{"app":"Finder","windows":[{"title":"Desktop","id":1}]}]`),
			"osascript|-l|JavaScript|-e|ObjC.import('Foundation'); var se = Application('System Events'); var app = se.applicationProcesses.whose({frontmost: true})[0]; JSON.stringify({application: app ? app.name() : '', windows: app ? app.windows().map(function(win) { return {title: win.name() || '', role: 'window'}; }) : []});":                                                                                                                                                                                                                                                                                                              []byte(`{"application":"Finder","windows":[{"title":"Desktop","role":"window"}]}`),
		},
	}
	events := &fakeEventPoster{}
	backend := newDarwinBackend(runner, events)

	if err := backend.Screenshot(context.Background(), "/tmp/executor-shot.png"); err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	windows, err := backend.Windows(context.Background())
	if err != nil {
		t.Fatalf("windows: %v", err)
	}
	tree, err := backend.Accessibility(context.Background())
	if err != nil {
		t.Fatalf("accessibility: %v", err)
	}
	if err := backend.Mouse(context.Background(), MouseAction{Type: MouseActionClick, X: 10, Y: 20, Button: MouseButtonLeft}); err != nil {
		t.Fatalf("mouse: %v", err)
	}
	if err := backend.Keyboard(context.Background(), KeyboardAction{Text: "hello"}); err != nil {
		t.Fatalf("keyboard: %v", err)
	}
	if err := backend.App(context.Background(), AppAction{Type: AppActionActivate, Name: "Finder"}); err != nil {
		t.Fatalf("app: %v", err)
	}

	if len(windows) != 1 || windows[0].App != "Finder" || windows[0].Title != "Desktop" {
		t.Fatalf("unexpected windows: %#v", windows)
	}
	if tree.Application != "Finder" || len(tree.Windows) != 1 {
		t.Fatalf("unexpected accessibility tree: %#v", tree)
	}

	wantCommands := []string{
		"screencapture|-x|/tmp/executor-shot.png",
		"osascript|-l|JavaScript|-e|ObjC.import('Foundation'); var se = Application('System Events'); var apps = se.applicationProcesses.whose({backgroundOnly: false})(); JSON.stringify(apps.map(function(app) { try { var appName = ''; try { appName = app.name(); } catch (error) {} var wins = []; try { wins = app.windows().map(function(win) { var title = ''; var id = 0; try { title = win.name() || ''; } catch (error) {} try { id = win.id() || 0; } catch (error) {} return {title: title, id: id}; }); } catch (error) { wins = []; } return {app: appName, windows: wins}; } catch (error) { return {app: '', windows: []}; } }));",
		"osascript|-l|JavaScript|-e|ObjC.import('Foundation'); var se = Application('System Events'); var app = se.applicationProcesses.whose({frontmost: true})[0]; JSON.stringify({application: app ? app.name() : '', windows: app ? app.windows().map(function(win) { return {title: win.name() || '', role: 'window'}; }) : []});",
		"osascript|-e|tell application \"System Events\" to keystroke \"hello\"",
		"osascript|-e|tell application \"Finder\" to activate",
	}
	if !reflect.DeepEqual(runner.calls, wantCommands) {
		t.Fatalf("unexpected command sequence: %#v", runner.calls)
	}

	wantEvents := []mouseEvent{{Type: MouseActionClick, X: 10, Y: 20, Button: MouseButtonLeft}}
	if !reflect.DeepEqual(events.mouseEvents, wantEvents) {
		t.Fatalf("unexpected mouse events: %#v", events.mouseEvents)
	}
}

type fakeRunner struct {
	calls   []string
	outputs map[string][]byte
}

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	key := commandKey(name, args...)
	f.calls = append(f.calls, key)
	if output, ok := f.outputs[key]; ok {
		return output, nil
	}
	return nil, nil
}

type fakeEventPoster struct {
	mouseEvents []mouseEvent
}

func (f *fakeEventPoster) PostMouse(action MouseAction) error {
	f.mouseEvents = append(f.mouseEvents, mouseEvent{
		Type:   action.Type,
		X:      action.X,
		Y:      action.Y,
		Button: action.Button,
	})
	return nil
}

func (f *fakeEventPoster) PostKeyboard(action KeyboardAction) error {
	return nil
}

type mouseEvent struct {
	Type   MouseActionType
	X      int
	Y      int
	Button MouseButton
}

func decodeJSON[T any](t *testing.T, data []byte) T {
	t.Helper()
	var decoded T
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	return decoded
}
