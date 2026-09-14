package dispatch

import (
	"encoding/json"
	"errors"
	"strings"

	permissionmodel "github.com/jamie950315/executor/internal/permissions"
)

// Permission reports describe desktop prerequisites. Filesystem access depends
// on each path and selected privilege; terminal availability is checked by its
// own operation. Neither can be inferred from a graphical-session report.
func desktopPermissionResult(result any) (any, error) {
	value, ok := result.(map[string]any)
	if !ok {
		return nil, errors.New("invalid desktop permission report")
	}
	if _, ok := value["ready"].(bool); !ok {
		return nil, errors.New("desktop permission report is missing boolean readiness")
	}
	if _, ok := value["requested"].(bool); !ok {
		return nil, errors.New("desktop permission report is missing request status")
	}
	if _, ok := value["permissions"]; !ok {
		return nil, errors.New("desktop permission report is missing permission checks")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("invalid desktop permission report")
	}
	var report permissionmodel.Report
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, errors.New("invalid desktop permission report")
	}
	if _, err := permissionmodel.ValidateReport(report); err != nil {
		return nil, err
	}
	states := make(map[string]permissionmodel.State, len(report.Items))
	for _, item := range report.Items {
		states[item.ID] = item.State
	}
	captureIDs := []string{"screen_recording"}
	inputIDs := []string{"input_control"}
	switch {
	case report.Platform == "macos" || report.Platform == "darwin":
		inputIDs = append(inputIDs, "accessibility")
	case report.Platform == "windows":
		captureIDs = append(captureIDs, "desktop_session")
		inputIDs = append(inputIDs, "desktop_session")
	case strings.HasPrefix(report.Platform, "linux") || report.Platform == "wsl":
		captureIDs = append(captureIDs, "desktop_session")
		inputIDs = []string{"keyboard_input", "pointer_input", "desktop_session"}
	default:
		// An unknown platform must provide its dependency mapping before these
		// higher-level prerequisites can be reported as satisfied.
		captureIDs, inputIDs = nil, nil
	}
	value["scope"] = "desktop"
	value["capabilities"] = map[string]any{
		"filesystem":     "not_checked",
		"terminal":       "not_checked",
		"desktopCapture": desktopPrerequisiteState(states, captureIDs, report.RestartRequired),
		"desktopInput":   desktopPrerequisiteState(states, inputIDs, report.RestartRequired),
	}
	if value["permissions"] == nil {
		value["permissions"] = []any{}
	}
	return value, nil
}

func desktopPrerequisiteState(states map[string]permissionmodel.State, ids []string, restart bool) string {
	if len(ids) == 0 {
		return "not_checked"
	}
	missing := false
	for _, id := range ids {
		state, exists := states[id]
		if !exists {
			missing = true
			continue
		}
		if state != permissionmodel.StateGranted && state != permissionmodel.StateNotRequired {
			return "permissions_required"
		}
	}
	if missing {
		return "not_checked"
	}
	if restart {
		return "restart_required"
	}
	return "permissions_ready"
}
