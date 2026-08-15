package main

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"testing"
)

func TestRunRuntimeCommandRoutesEveryDaemonSubcommand(t *testing.T) {
	t.Parallel()
	var calls []string
	commands := runtimeCommands{
		agent:     func(context.Context, string) error { calls = append(calls, "agent"); return nil },
		broker:    func(context.Context, string) error { calls = append(calls, "broker"); return nil },
		desktop:   func(context.Context, string) error { calls = append(calls, "desktop"); return nil },
		dashboard: func(context.Context, string) error { calls = append(calls, "dashboard"); return nil },
		stdio:     func(context.Context, string, io.Reader, io.Writer) error { calls = append(calls, "stdio"); return nil },
	}
	for _, name := range []string{"agent", "broker", "desktop", "dashboard", "stdio"} {
		var stdout, stderr bytes.Buffer
		handled, code := runRuntimeCommand(context.Background(), []string{name, "--config", "/tmp/config.json"}, bytes.NewBuffer(nil), &stdout, &stderr, commands)
		if !handled || code != 0 {
			t.Fatalf("%s handled=%v code=%d stderr=%q", name, handled, code, stderr.String())
		}
	}
	if !reflect.DeepEqual(calls, []string{"agent", "broker", "desktop", "dashboard", "stdio"}) {
		t.Fatalf("runtime calls = %#v", calls)
	}
}

func TestRunRuntimeCommandLeavesCLICommandsUnhandled(t *testing.T) {
	t.Parallel()
	handled, _ := runRuntimeCommand(context.Background(), []string{"setup", "--domain", "example.com"}, bytes.NewBuffer(nil), io.Discard, io.Discard, runtimeCommands{})
	if handled {
		t.Fatal("setup was intercepted as a runtime command")
	}
}
