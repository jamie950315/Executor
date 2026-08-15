package lifecycle

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type recorder struct {
	steps  []string
	failAt string
}

func (r *recorder) step(name string) error {
	r.steps = append(r.steps, name)
	if r.failAt == name {
		return errors.New("failed " + name)
	}
	return nil
}
func (r *recorder) Quiesce(context.Context) error           { return r.step("quiesce") }
func (r *recorder) StopTunnel(context.Context) error        { return r.step("stop-tunnel") }
func (r *recorder) KillSessions(context.Context) error      { return r.step("kill-sessions") }
func (r *recorder) StopDesktop(context.Context) error       { return r.step("stop-desktop") }
func (r *recorder) StopBroker(context.Context) error        { return r.step("stop-broker") }
func (r *recorder) RevokeOAuth(context.Context) error       { return r.step("revoke-oauth") }
func (r *recorder) RotateCredentials(context.Context) error { return r.step("rotate-credentials") }
func (r *recorder) StopAgent(context.Context) error         { return r.step("stop-agent") }
func (r *recorder) StartBroker(context.Context) error       { return r.step("start-broker") }
func (r *recorder) StartDesktop(context.Context) error      { return r.step("start-desktop") }
func (r *recorder) StartAgent(context.Context) error        { return r.step("start-agent") }
func (r *recorder) StartTunnel(context.Context) error       { return r.step("start-tunnel") }

func TestKillRunsEverySafetyStepInOrder(t *testing.T) {
	r := &recorder{}
	manager := New(r)
	if err := manager.Kill(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"quiesce", "stop-tunnel", "kill-sessions", "stop-desktop", "stop-broker", "revoke-oauth", "rotate-credentials", "stop-agent"}
	if !reflect.DeepEqual(r.steps, want) {
		t.Fatalf("steps = %#v, want %#v", r.steps, want)
	}
}

func TestKillContinuesAfterIndividualFailures(t *testing.T) {
	r := &recorder{failAt: "stop-tunnel"}
	err := New(r).Kill(context.Background())
	if err == nil {
		t.Fatal("Kill returned nil after a failed step")
	}
	if len(r.steps) != 8 || r.steps[len(r.steps)-1] != "stop-agent" {
		t.Fatalf("Kill stopped early: %#v", r.steps)
	}
}

func TestResumeStartsLocalComponentsBeforeTunnel(t *testing.T) {
	r := &recorder{}
	if err := New(r).Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"start-broker", "start-desktop", "start-agent", "start-tunnel"}
	if !reflect.DeepEqual(r.steps, want) {
		t.Fatalf("steps = %#v, want %#v", r.steps, want)
	}
}
