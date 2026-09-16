package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jamie950315/executor/internal/relay"
)

type RuntimeConfig struct {
	Version          uint16 `json:"version"`
	HubID            string `json:"hub_id"`
	DashboardURL     string `json:"dashboard_url"`
	MachineTokenFile string `json:"machine_token_file"`
}

// LoadRouter requires a dedicated, protected Hub state directory. The signing
// key belongs to that state's secret store, not to model-supplied arguments.
func LoadRouter(stateDir string, identity *relay.DeviceIdentity) (*Router, error) {
	root, err := os.OpenRoot(stateDir)
	if err != nil {
		return nil, errors.New("Hub state unavailable")
	}
	defer root.Close()
	data, err := readProtectedHubFile(root, "hub.json", 65536)
	if err != nil {
		return nil, err
	}
	var cfg RuntimeConfig
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cfg) != nil || cfg.Version != 1 {
		return nil, errors.New("invalid Hub configuration")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, errors.New("invalid Hub configuration")
	}
	name := cfg.MachineTokenFile
	if !validStateReference(name) {
		return nil, errors.New("Hub token reference must remain inside Hub state")
	}
	token, err := readProtectedHubFile(root, name, 4096)
	if err != nil {
		return nil, err
	}
	transport, err := NewDashboardRelay(cfg.DashboardURL, strings.TrimSpace(string(token)), nil)
	if err != nil {
		return nil, err
	}
	signed, err := NewSignedRelay(transport, cfg.HubID, identity, transport.Delegation, nil)
	if err != nil {
		return nil, err
	}
	return New(signed), nil
}

func validStateReference(name string) bool {
	return name != "" && !filepath.IsAbs(name) && filepath.VolumeName(name) == "" && filepath.Clean(name) == name && name != "." && name != ".." && !strings.HasPrefix(name, ".."+string(filepath.Separator))
}

func readProtectedHubFile(root *os.Root, name string, maximum int) ([]byte, error) {
	fail := errors.New("Hub configuration or credential file unavailable or unprotected")
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() > int64(maximum) || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return nil, fail
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, fail
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fail
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(maximum+1)))
	if err != nil || len(data) > maximum {
		return nil, fail
	}
	return data, nil
}
