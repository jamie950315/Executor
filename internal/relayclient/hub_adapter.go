package relayclient

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jamie950315/executor/internal/mcp"
	"github.com/jamie950315/executor/internal/relay"
)

// Hub approvals contain public keys only. They must be provisioned through
// an owner-authorized local/enrollment path, never from the incoming proof.
type HubApproval struct {
	HubID             string             `json:"hub_id"`
	PublicKey         relay.PublicKeyJWK `json:"public_key"`
	DelegationVersion uint64             `json:"delegation_version"`
	Enabled           bool               `json:"enabled"`
}
type HubApprovalFile struct {
	Version  uint16        `json:"version"`
	DeviceID string        `json:"device_id"`
	Hubs     []HubApproval `json:"hubs"`
}

func readHubApproval(stateDir, deviceID, hubID string) (HubApproval, error) {
	root, err := os.OpenRoot(stateDir)
	if err != nil {
		return HubApproval{}, ErrRequestUnauthorized
	}
	defer root.Close()
	info, err := root.Lstat("hub-delegations.json")
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return HubApproval{}, ErrRequestUnauthorized
	}
	file, err := root.Open("hub-delegations.json")
	if err != nil {
		return HubApproval{}, ErrRequestUnauthorized
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return HubApproval{}, ErrRequestUnauthorized
	}
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return HubApproval{}, ErrRequestUnauthorized
	}
	var approvals HubApprovalFile
	if decodeStrictJSON(data, &approvals) != nil || approvals.Version != relay.ProtocolVersion || approvals.DeviceID != deviceID || len(approvals.Hubs) > 64 {
		return HubApproval{}, ErrRequestUnauthorized
	}
	seen := map[string]bool{}
	var matched *HubApproval
	for i := range approvals.Hubs {
		entry := &approvals.Hubs[i]
		if strings.TrimSpace(entry.HubID) == "" || len(entry.HubID) > 256 || seen[entry.HubID] || entry.DelegationVersion == 0 {
			return HubApproval{}, ErrRequestUnauthorized
		}
		if _, err := relay.HubKeyID(entry.PublicKey); err != nil {
			return HubApproval{}, ErrRequestUnauthorized
		}
		seen[entry.HubID] = true
		if entry.HubID == hubID {
			matched = entry
		}
	}
	if matched == nil || !matched.Enabled {
		return HubApproval{}, ErrRequestUnauthorized
	}
	return *matched, nil
}

func (a *Adapter) handleHubRequest(ctx context.Context, requestID string, raw json.RawMessage) (HandleResult, error) {
	var signed relay.SignedHubRequest
	if len(raw) > 112<<20 || decodeStrictJSON(raw, &signed) != nil || signed.Request.RequestID != requestID {
		return HandleResult{}, ErrRequestInvalid
	}
	cfg, values, identity, err := a.loadCurrent()
	if err != nil {
		return HandleResult{}, ErrRequestUnauthorized
	}
	actor := actorHash("hub:"+signed.Request.HubID, signed.Request.CallerID)
	if disabled(filepath.Join(cfg.StateDir, "disabled")) {
		return HandleResult{}, ErrExecutorDisabled
	}
	approved, err := readHubApproval(cfg.StateDir, cfg.UnifiedDashboard.DeviceID, signed.Request.HubID)
	if err != nil {
		return HandleResult{}, ErrRequestUnauthorized
	}
	keyID, err := relay.HubKeyID(approved.PublicKey)
	if err != nil {
		return HandleResult{}, ErrRequestUnauthorized
	}
	allowed := false
	for _, tool := range mcp.BuiltinTools() {
		if tool.Name == signed.Request.Method {
			allowed = true
			break
		}
	}
	if !allowed {
		return HandleResult{}, ErrUnsupportedMethod
	}
	var input map[string]any
	if decodeStrictJSON(signed.Request.Input, &input) != nil || input == nil {
		return HandleResult{}, ErrRequestInvalid
	}
	replay, err := relay.NewFileReplayStore(filepath.Join(cfg.StateDir, "hub-replay"), a.now)
	if err != nil {
		return HandleResult{}, ErrRequestUnauthorized
	}
	defer replay.Close()
	expected := relay.HubGrantExpectation{DeviceID: cfg.UnifiedDashboard.DeviceID, HubID: approved.HubID, HubKeyID: keyID, Generation: values.Generation, DelegationVersion: approved.DelegationVersion, Now: a.now()}
	if relay.VerifyHubRequest(identity.PublicJWK(), approved.PublicKey, expected, signed, replay) != nil {
		a.appendAudit(cfg, actor, "hub.authorize", "rejected")
		return HandleResult{}, ErrRequestUnauthorized
	}
	// Recheck mutable local authorization after waiting for replay persistence.
	fresh, err := readHubApproval(cfg.StateDir, cfg.UnifiedDashboard.DeviceID, approved.HubID)
	if err != nil || fresh != approved {
		return HandleResult{}, ErrRequestUnauthorized
	}
	currentCfg, currentValues, currentIdentity, err := a.loadCurrent()
	if err != nil || currentValues.Generation != values.Generation || currentCfg.StateDir != cfg.StateDir || currentCfg.UnifiedDashboard.DeviceID != cfg.UnifiedDashboard.DeviceID || currentCfg.BrokerEndpoint != cfg.BrokerEndpoint || currentCfg.DesktopEndpoint != cfg.DesktopEndpoint || currentIdentity.PublicJWK() != identity.PublicJWK() {
		return HandleResult{}, ErrRequestUnauthorized
	}
	if err := ctx.Err(); err != nil {
		return HandleResult{}, err
	}
	if disabled(filepath.Join(cfg.StateDir, "disabled")) {
		return HandleResult{}, ErrExecutorDisabled
	}
	scopeBytes, _ := json.Marshal([]string{"hub-v1", cfg.UnifiedDashboard.DeviceID, approved.HubID, signed.Request.CallerID})
	result, err := a.dispatcherFor(cfg, values).Dispatch(ctx, mcp.ToolCall{SessionID: stableSessionID(string(scopeBytes), "hub"), Name: signed.Request.Method, Arguments: input})
	if err != nil {
		a.appendAudit(cfg, actor, signed.Request.Method, "failed")
		return HandleResult{}, fixedDispatchError(err)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return HandleResult{}, ErrRequestInvalid
	}
	a.appendAudit(cfg, actor, signed.Request.Method, "succeeded")
	return HandleResult{Payload: payload}, nil
}
