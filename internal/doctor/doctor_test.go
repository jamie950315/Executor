package doctor

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type checkFunc struct {
	name   string
	detail string
	err    error
	called *[]string
}

func (c checkFunc) Name() string { return c.name }
func (c checkFunc) Run(context.Context) (string, error) {
	*c.called = append(*c.called, c.name)
	return c.detail, c.err
}

func TestRunExecutesEveryCheckAndReportsUnhealthy(t *testing.T) {
	var called []string
	result := Run(context.Background(), []Checker{
		checkFunc{name: "agent", detail: "running", called: &called},
		checkFunc{name: "broker", detail: "unavailable", err: errors.New("connection refused"), called: &called},
		checkFunc{name: "tunnel", detail: "connected", called: &called},
	})
	if result.Healthy {
		t.Fatal("result marked healthy despite a failed check")
	}
	if !reflect.DeepEqual(called, []string{"agent", "broker", "tunnel"}) {
		t.Fatalf("checks called = %#v", called)
	}
	if len(result.Checks) != 3 || result.Checks[1].OK || result.Checks[1].Error != "connection refused" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestRunWithNoChecksIsNotHealthy(t *testing.T) {
	if result := Run(context.Background(), nil); result.Healthy {
		t.Fatal("empty doctor run marked healthy")
	}
}
