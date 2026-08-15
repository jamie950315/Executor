package control

import (
	"errors"
	"reflect"
	"testing"
)

func TestLinuxCommandsUseInstalledCloudflaredUnitAndDesktopRuntime(t *testing.T) {
	env := func(name string) string {
		if name == "EXECUTOR_DESKTOP_USER" {
			return "jamie"
		}
		return ""
	}
	lookup := func(owner string) (string, error) {
		if owner != "jamie" {
			return "", errors.New("unexpected owner")
		}
		return "501", nil
	}

	cloudflared, err := serviceCommands("linux", "stop", Cloudflared, env, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cloudflared, []commandSpec{{name: "systemctl", args: []string{"stop", "executor-cloudflared.service"}}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cloudflared command = %#v, want %#v", got, want)
	}
	desktop, err := serviceCommands("linux", "start", Desktop, env, lookup)
	if err != nil {
		t.Fatal(err)
	}
	wantDesktop := []commandSpec{{name: "runuser", args: []string{"-u", "jamie", "--", "env", "XDG_RUNTIME_DIR=/run/user/501", "systemctl", "--user", "start", "executor-desktop.service"}}}
	if !reflect.DeepEqual(desktop, wantDesktop) {
		t.Fatalf("desktop command = %#v, want %#v", desktop, wantDesktop)
	}
}

func TestLinuxDesktopPrefersConfiguredUID(t *testing.T) {
	env := func(name string) string {
		return map[string]string{"EXECUTOR_DESKTOP_USER": "jamie", "EXECUTOR_DESKTOP_UID": "777"}[name]
	}
	commands, err := serviceCommands("linux", "stop", Desktop, env, func(string) (string, error) {
		return "", errors.New("lookup must not run")
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := commands[0].args[4]; got != "XDG_RUNTIME_DIR=/run/user/777" {
		t.Fatalf("desktop runtime = %q", got)
	}
}

func TestWindowsUsesSCForServicesAndScheduledTaskForDesktop(t *testing.T) {
	env := func(string) string { return "" }
	cloudflared, err := serviceCommands("windows", "start", Cloudflared, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cloudflared, []commandSpec{{name: "sc.exe", args: []string{"start", "ExecutorCloudflared"}}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cloudflared command = %#v, want %#v", got, want)
	}
	desktopStop, err := serviceCommands("windows", "stop", Desktop, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := desktopStop, []commandSpec{{name: "schtasks.exe", args: []string{"/End", "/TN", "ExecutorDesktop"}}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("desktop stop command = %#v, want %#v", got, want)
	}
	desktopStart, err := serviceCommands("windows", "start", Desktop, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := desktopStart, []commandSpec{{name: "schtasks.exe", args: []string{"/Run", "/TN", "ExecutorDesktop"}}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("desktop start command = %#v, want %#v", got, want)
	}
}

func TestDarwinStopAndStartAreReversible(t *testing.T) {
	env := func(name string) string {
		return map[string]string{"SUDO_UID": "501"}[name]
	}
	stop, err := serviceCommands("darwin", "stop", Desktop, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := stop, []commandSpec{{name: "launchctl", args: []string{"bootout", "gui/501/com.executor.desktop"}}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stop = %#v, want %#v", got, want)
	}
	start, err := serviceCommands("darwin", "start", Desktop, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantStart := []commandSpec{
		{name: "launchctl", args: []string{"bootstrap", "gui/501", "/Library/LaunchAgents/com.executor.desktop.plist"}},
		{name: "launchctl", args: []string{"enable", "gui/501/com.executor.desktop"}},
		{name: "launchctl", args: []string{"kickstart", "-k", "gui/501/com.executor.desktop"}},
	}
	if !reflect.DeepEqual(start, wantStart) {
		t.Fatalf("start = %#v, want %#v", start, wantStart)
	}
}

func TestDarwinCloudflaredUsesExecutorOwnedLabel(t *testing.T) {
	stop, err := serviceCommands("darwin", "stop", Cloudflared, func(string) string { return "" }, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []commandSpec{{name: "launchctl", args: []string{"bootout", "system/com.executor.cloudflared"}}}
	if !reflect.DeepEqual(stop, want) {
		t.Fatalf("cloudflared stop = %#v, want %#v", stop, want)
	}
}

func TestDarwinGUIUIDFallbackOrderMatchesBootstrap(t *testing.T) {
	withExplicit := func(name string) string {
		return map[string]string{"EXECUTOR_GUI_UID": "777", "SUDO_UID": "501"}[name]
	}
	commands, err := serviceCommands("darwin", "stop", Desktop, withExplicit, func(string) (string, error) {
		return "", errors.New("console lookup must not run")
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := commands[0].args[1]; got != "gui/777/com.executor.desktop" {
		t.Fatalf("explicit GUI target = %q", got)
	}

	consoleLookup := func(owner string) (string, error) {
		if owner != "" {
			return "", errors.New("console lookup must not receive an owner")
		}
		return "502", nil
	}
	commands, err = serviceCommands("darwin", "stop", Desktop, func(string) string { return "" }, consoleLookup)
	if err != nil {
		t.Fatal(err)
	}
	if got := commands[0].args[1]; got != "gui/502/com.executor.desktop" {
		t.Fatalf("console GUI target = %q", got)
	}
}
