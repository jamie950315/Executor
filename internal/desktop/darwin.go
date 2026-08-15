//go:build darwin

package desktop

import (
	"context"
	"encoding/json"
	"fmt"
)

const darwinWindowsScript = "ObjC.import('Foundation'); var se = Application('System Events'); var apps = se.applicationProcesses.whose({backgroundOnly: false})(); JSON.stringify(apps.map(function(app) { try { var appName = ''; try { appName = app.name(); } catch (error) {} var wins = []; try { wins = app.windows().map(function(win) { var title = ''; var id = 0; try { title = win.name() || ''; } catch (error) {} try { id = win.id() || 0; } catch (error) {} return {title: title, id: id}; }); } catch (error) { wins = []; } return {app: appName, windows: wins}; } catch (error) { return {app: '', windows: []}; } }));"
const darwinAccessibilityScript = "ObjC.import('Foundation'); var se = Application('System Events'); var app = se.applicationProcesses.whose({frontmost: true})[0]; JSON.stringify({application: app ? app.name() : '', windows: app ? app.windows().map(function(win) { return {title: win.name() || '', role: 'window'}; }) : []});"

type eventPoster interface {
	PostMouse(action MouseAction) error
	PostKeyboard(action KeyboardAction) error
}

type darwinBackend struct {
	runner commandRunner
	events eventPoster
}

func newDarwinBackend(runner commandRunner, events eventPoster) darwinBackend {
	return darwinBackend{runner: runner, events: events}
}

func (b darwinBackend) Screenshot(ctx context.Context, path string) error {
	if _, err := b.runner.Run(ctx, "screencapture", "-x", path); err != nil {
		return wrapDesktopError("screenshot unavailable", err)
	}
	return nil
}

func (b darwinBackend) Windows(ctx context.Context) ([]Window, error) {
	data, err := b.runner.Run(ctx, "osascript", "-l", "JavaScript", "-e", darwinWindowsScript)
	if err != nil {
		return nil, wrapDesktopError("window enumeration unavailable", err)
	}

	var payload []struct {
		App     string `json:"app"`
		Windows []struct {
			Title string `json:"title"`
			ID    int    `json:"id"`
		} `json:"windows"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("decode windows payload: %w", err)
	}

	windows := make([]Window, 0)
	for _, item := range payload {
		for _, window := range item.Windows {
			windows = append(windows, Window{
				App:   item.App,
				Title: window.Title,
				ID:    window.ID,
			})
		}
	}
	return windows, nil
}

func (b darwinBackend) Accessibility(ctx context.Context) (AccessibilityTree, error) {
	data, err := b.runner.Run(ctx, "osascript", "-l", "JavaScript", "-e", darwinAccessibilityScript)
	if err != nil {
		return AccessibilityTree{}, wrapDesktopError("accessibility unavailable", err)
	}

	var tree AccessibilityTree
	if err := json.Unmarshal(data, &tree); err != nil {
		return AccessibilityTree{}, fmt.Errorf("decode accessibility payload: %w", err)
	}
	return tree, nil
}

func (b darwinBackend) Mouse(ctx context.Context, action MouseAction) error {
	if err := b.events.PostMouse(action); err != nil {
		return wrapDesktopError("mouse input unavailable", err)
	}
	return nil
}

func (b darwinBackend) Keyboard(ctx context.Context, action KeyboardAction) error {
	if action.Text != "" {
		if _, err := b.runner.Run(ctx, "osascript", "-e", fmt.Sprintf("tell application \"System Events\" to keystroke %q", action.Text)); err != nil {
			return wrapDesktopError("keyboard input unavailable", err)
		}
		return nil
	}
	if err := b.events.PostKeyboard(action); err != nil {
		return wrapDesktopError("keyboard input unavailable", err)
	}
	return nil
}

func (b darwinBackend) App(ctx context.Context, action AppAction) error {
	var script string
	switch action.Type {
	case AppActionActivate:
		script = fmt.Sprintf("tell application %q to activate", action.Name)
	case AppActionLaunch:
		script = fmt.Sprintf("tell application %q to launch", action.Name)
	case AppActionQuit:
		script = fmt.Sprintf("tell application %q to quit", action.Name)
	default:
		return fmt.Errorf("unsupported app action %q", action.Type)
	}
	if _, err := b.runner.Run(ctx, "osascript", "-e", script); err != nil {
		return wrapDesktopError("application action unavailable", err)
	}
	return nil
}
