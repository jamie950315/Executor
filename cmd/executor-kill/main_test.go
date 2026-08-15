package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jamie950315/executor/internal/control"
)

type fakeKiller struct {
	result control.Result
	err    error
	called bool
}

func (k *fakeKiller) Kill(context.Context) (control.Result, error) {
	k.called = true
	return k.result, k.err
}

func TestRunShowsReplacementMaterialForPartialKillOnly(t *testing.T) {
	fake := &fakeKiller{result: control.Result{RecoveryKey: "recovery-once", URLSecret: "url-once"}, err: errors.New("broker unavailable")}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--config", "/secure/executor.json"}, &stdout, &stderr, func(string) (killRunner, error) { return fake, nil })
	if code != 1 || !fake.called {
		t.Fatalf("code=%d called=%t stderr=%q", code, fake.called, stderr.String())
	}
	for _, want := range []string{"recovery-once", "url-once", "shown once"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout %q missing %q", stdout.String(), want)
		}
	}
}

func TestRunDoesNotPrintMaterialWhenRotationFailed(t *testing.T) {
	fake := &fakeKiller{err: errors.New("rotation failed")}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--config", "/secure/executor.json"}, &stdout, &stderr, func(string) (killRunner, error) { return fake, nil })
	if code != 1 || stdout.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
