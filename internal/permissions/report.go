package permissions

import (
	"errors"
	"fmt"
	"strings"
)

type State string

const (
	StateGranted     State = "granted"
	StatePending     State = "pending"
	StateDenied      State = "denied"
	StateManual      State = "manual"
	StateUnavailable State = "unavailable"
	StateNotRequired State = "not_required"
)

type Item struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	State       State  `json:"state"`
	Required    bool   `json:"required"`
	Detail      string `json:"detail,omitempty"`
	SettingsURL string `json:"settings_url,omitempty"`
}

type Report struct {
	Platform        string `json:"platform"`
	Requested       bool   `json:"requested"`
	Ready           bool   `json:"ready"`
	RestartRequired bool   `json:"restart_required,omitempty"`
	Items           []Item `json:"permissions"`
}

func NewReport(platform string, requested bool, items []Item) Report {
	ready := true
	for _, item := range items {
		if item.Required && item.State != StateGranted && item.State != StateNotRequired {
			ready = false
		}
	}
	return Report{
		Platform:  strings.TrimSpace(platform),
		Requested: requested,
		Ready:     ready,
		Items:     append([]Item(nil), items...),
	}
}

func ValidateReport(report Report) (Report, error) {
	if report.Platform == "" {
		return Report{}, errors.New("permission report platform is required")
	}
	validStates := map[State]bool{
		StateGranted: true, StatePending: true, StateDenied: true,
		StateManual: true, StateUnavailable: true, StateNotRequired: true,
	}
	seen := make(map[string]struct{}, len(report.Items))
	for index, item := range report.Items {
		if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Label) == "" {
			return Report{}, fmt.Errorf("permission item %d requires id and label", index)
		}
		if !validStates[item.State] {
			return Report{}, fmt.Errorf("permission item %q has invalid state %q", item.ID, item.State)
		}
		if _, ok := seen[item.ID]; ok {
			return Report{}, fmt.Errorf("duplicate permission item %q", item.ID)
		}
		seen[item.ID] = struct{}{}
	}
	return report, nil
}
