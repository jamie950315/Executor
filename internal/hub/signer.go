package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jamie950315/executor/internal/mcp"
	"github.com/jamie950315/executor/internal/relay"
)

type Delegation struct {
	DeviceKey  relay.PublicKeyJWK
	Grant      string
	Generation uint64
	Version    uint64
}

// DelegationSource must use owner-approved device identities and current local
// grants. It must not trust public keys or grants supplied by model arguments.
type DelegationSource func(context.Context, string) (Delegation, error)
type SignedTransport interface {
	Devices(context.Context) ([]Device, error)
	Submit(context.Context, string, relay.SignedHubRequest) (any, error)
}

type SignedRelay struct {
	transport   SignedTransport
	hubID       string
	identity    *relay.DeviceIdentity
	keyID       string
	delegations DelegationSource
	now         func() time.Time
}

func NewSignedRelay(transport SignedTransport, hubID string, identity *relay.DeviceIdentity, source DelegationSource, now func() time.Time) (*SignedRelay, error) {
	if transport == nil || identity == nil || source == nil || strings.TrimSpace(hubID) == "" || len(hubID) > 256 {
		return nil, errors.New("Hub signing configuration is required")
	}
	keyID, err := relay.HubKeyID(identity.PublicJWK())
	if err != nil {
		return nil, errors.New("invalid Hub signing identity")
	}
	if now == nil {
		now = time.Now
	}
	return &SignedRelay{transport: transport, hubID: hubID, identity: identity, keyID: keyID, delegations: source, now: now}, nil
}

func (s *SignedRelay) Devices(ctx context.Context) ([]Device, error) {
	devices, err := s.transport.Devices(ctx)
	if err != nil {
		return nil, err
	}
	devices = append([]Device{}, devices...)
	for i := range devices {
		if devices[i].Authorized {
			_, err := s.validDelegation(ctx, devices[i].ID, s.now())
			devices[i].Authorized = err == nil
		}
	}
	return devices, nil
}

func (s *SignedRelay) validDelegation(ctx context.Context, deviceID string, now time.Time) (Delegation, error) {
	delegation, err := s.delegations(ctx, deviceID)
	if err != nil {
		return Delegation{}, errors.New("Hub device delegation unavailable")
	}
	expected := relay.HubGrantExpectation{DeviceID: deviceID, HubID: s.hubID, HubKeyID: s.keyID, Generation: delegation.Generation, DelegationVersion: delegation.Version, Now: now}
	if _, err := relay.VerifyHubGrant(delegation.DeviceKey, delegation.Grant, expected); err != nil {
		return Delegation{}, errors.New("Hub device delegation is invalid or expired")
	}
	return delegation, nil
}

func (s *SignedRelay) Call(ctx context.Context, deviceID string, call mcp.ToolCall) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if call.SessionID == "" {
		return nil, errors.New("Hub caller session is required")
	}
	now := s.now()
	delegation, err := s.validDelegation(ctx, deviceID, now)
	if err != nil {
		return nil, errors.New("Hub device delegation unavailable")
	}
	input, err := json.Marshal(call.Arguments)
	if err != nil {
		return nil, errors.New("invalid Hub tool arguments")
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, errors.New("Hub nonce generation failed")
	}
	requestID := hex.EncodeToString(nonce[:16])
	proof, err := relay.SignHubRequest(s.identity, relay.HubRequest{Version: relay.ProtocolVersion, DeviceID: deviceID, HubID: s.hubID, CallerID: call.SessionID, RequestID: requestID, Method: call.Name, Input: input, Grant: delegation.Grant, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Minute).Unix(), Nonce: hex.EncodeToString(nonce[:])})
	if err != nil {
		return nil, errors.New("Hub request signing failed")
	}
	return s.transport.Submit(ctx, deviceID, proof)
}
