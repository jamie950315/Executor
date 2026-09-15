package relayclient

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jamie950315/executor/internal/relay"
)

func (a *Adapter) handleHubOwner(ctx context.Context, method string, call verifiedCall, actor string) (HandleResult, error) {
	var input struct {
		HubID     string             `json:"hub_id"`
		PublicKey relay.PublicKeyJWK `json:"public_key"`
		Expires   int64              `json:"expires_in_seconds"`
	}
	if method == "hub.revoke" {
		var revoke struct {
			HubID string `json:"hub_id"`
		}
		if decodeStrictJSON(call.input, &revoke) != nil {
			return HandleResult{}, ErrRequestInvalid
		}
		input.HubID = revoke.HubID
	} else if decodeStrictJSON(call.input, &input) != nil {
		return HandleResult{}, ErrRequestInvalid
	}
	if strings.TrimSpace(input.HubID) == "" || len(input.HubID) > 256 {
		return HandleResult{}, ErrRequestInvalid
	}
	if input.Expires == 0 {
		input.Expires = int64(relay.MaxGrantLifetime / time.Second)
	}
	if input.Expires < 60 || input.Expires > int64(relay.MaxGrantLifetime/time.Second) {
		return HandleResult{}, ErrRequestInvalid
	}
	keyID := ""
	if method == "hub.delegate" {
		var err error
		keyID, err = relay.HubKeyID(input.PublicKey)
		if err != nil {
			return HandleResult{}, ErrRequestInvalid
		}
	}
	var result HandleResult
	err := relay.WithHubStateLock(call.config.StateDir, func() error {
		fresh, err := a.revalidateCall(call)
		if err != nil || fresh.config.StateDir != call.config.StateDir {
			return ErrRequestUnauthorized
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if disabled(filepath.Join(fresh.config.StateDir, "disabled")) {
			return ErrExecutorDisabled
		}
		approvals, err := readHubApprovals(fresh.config.StateDir, fresh.config.UnifiedDashboard.DeviceID, true)
		if err != nil {
			return err
		}
		index := -1
		for i := range approvals.Hubs {
			if approvals.Hubs[i].HubID == input.HubID {
				index = i
				break
			}
		}
		if index < 0 {
			if method == "hub.revoke" || len(approvals.Hubs) >= 64 {
				return ErrRequestInvalid
			}
			approvals.Hubs = append(approvals.Hubs, HubApproval{HubID: input.HubID})
			index = len(approvals.Hubs) - 1
		}
		entry := &approvals.Hubs[index]
		if entry.DelegationVersion >= 9007199254740991 {
			return ErrRequestInvalid
		}
		entry.DelegationVersion++
		entry.Enabled = method == "hub.delegate"
		output := map[string]any{"hub_id": input.HubID, "device_id": fresh.config.UnifiedDashboard.DeviceID, "generation": fresh.values.Generation, "delegation_version": entry.DelegationVersion, "enabled": entry.Enabled}
		if entry.Enabled {
			entry.PublicKey = input.PublicKey
			identity, err := relay.ParseDeviceIdentity(fresh.values.RelayPrivateJWK)
			if err != nil {
				return ErrRequestUnauthorized
			}
			var nonce [24]byte
			if _, err := rand.Read(nonce[:]); err != nil {
				return err
			}
			now := a.now()
			expires := now.Add(time.Duration(input.Expires) * time.Second)
			grant, err := relay.SignHubGrant(identity, relay.HubGrantClaims{Version: relay.ProtocolVersion, DeviceID: fresh.config.UnifiedDashboard.DeviceID, HubID: input.HubID, HubKeyID: keyID, Generation: fresh.values.Generation, DelegationVersion: entry.DelegationVersion, IssuedAt: now.Unix(), ExpiresAt: expires.Unix(), JTI: hex.EncodeToString(nonce[:])})
			if err != nil {
				return err
			}
			output["grant"] = grant
			output["expires_at"] = expires.Unix()
		}
		if err := saveHubApprovals(fresh.config.StateDir, approvals); err != nil {
			return err
		}
		result.Payload, err = json.Marshal(output)
		return err
	})
	if err != nil {
		a.appendAudit(call.config, actor, method, "failed")
		return HandleResult{}, errors.New("Hub owner action failed or unconfirmed")
	}
	a.appendAudit(call.config, actor, method, "succeeded")
	return result, nil
}

func saveHubApprovals(directory string, approvals HubApprovalFile) error {
	data, err := json.Marshal(approvals)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	existing, err := root.Lstat("hub-delegations.json")
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if existing != nil && !existing.Mode().IsRegular() {
		return ErrRequestUnauthorized
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	name := ".hub-approvals-" + hex.EncodeToString(nonce[:]) + ".tmp"
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_SYNC, 0600)
	if err != nil {
		return err
	}
	defer func() { file.Close(); _ = root.Remove(name) }()
	if existing != nil {
		if err := preserveHubApprovalOwner(file, existing); err != nil {
			return err
		}
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return finishHubApprovalRename(directory, name)
}
