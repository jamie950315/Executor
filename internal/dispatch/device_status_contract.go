package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

const diagnosticProbeTimeout = 2 * time.Second

type diagnosticTarget struct {
	name   string
	caller Caller
}

type diagnosticProbe struct {
	name  string
	value any
	state string
}

// Independent, read-only probes retain healthy component evidence when another
// helper is unavailable. State codes deliberately exclude raw helper errors.
func diagnosticComponents(ctx context.Context, method string, targets ...diagnosticTarget) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, diagnosticProbeTimeout)
	defer cancel()
	completed := make(chan diagnosticProbe, len(targets))
	pending := make(map[string]bool, len(targets))
	result := map[string]any{"partial": false}
	components := make(map[string]any, len(targets))
	result["components"] = components
	for _, target := range targets {
		pending[target.name] = true
		go func(target diagnosticTarget) { completed <- runDiagnosticProbe(probeCtx, method, target) }(target)
	}
	accept := func(probe diagnosticProbe) {
		if !pending[probe.name] {
			return
		}
		delete(pending, probe.name)
		result[probe.name] = probe.value
		components[probe.name] = map[string]any{"state": probe.state}
		if probe.state != "ok" {
			result["partial"] = true
		}
	}
	for len(pending) > 0 {
		select {
		case probe := <-completed:
			accept(probe)
		case <-probeCtx.Done():
			// Capture all evidence already returned before assigning timeout to
			// outstanding probes. Late returns fit in the bounded channel.
			draining := true
			for draining {
				select {
				case probe := <-completed:
					accept(probe)
				default:
					draining = false
				}
			}
			for name := range pending {
				accept(diagnosticProbe{name: name, state: diagnosticErrorState(probeCtx.Err())})
			}
		}
	}
	return result, nil
}

func runDiagnosticProbe(ctx context.Context, method string, target diagnosticTarget) diagnosticProbe {
	probe := diagnosticProbe{name: target.name, state: "unavailable"}
	if target.caller == nil {
		return probe
	}
	if err := ctx.Err(); err != nil {
		probe.state = diagnosticErrorState(err)
		return probe
	}
	var raw json.RawMessage
	if err := target.caller.Call(ctx, method, struct{}{}, &raw); err != nil {
		probe.state = diagnosticErrorState(err)
		return probe
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		probe.state = "invalid_response"
		return probe
	}
	valid := false
	if method == "terminal.list" {
		_, valid = value.([]any)
	} else if record, ok := value.(map[string]any); ok && len(record) > 0 {
		valid = true
	}
	if !valid {
		probe.state = "invalid_response"
		return probe
	}
	probe.state, probe.value = "ok", value
	return probe
}

func diagnosticErrorState(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "error"
}
