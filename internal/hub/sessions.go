package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/jamie950315/executor/internal/mcp"
)

type sessionBinding struct{ device, caller, privilege, remote string }

const maximumHubSessions = 2048

func copyRecord(record map[string]any) map[string]any {
	out := make(map[string]any, len(record))
	for k, v := range record {
		out[k] = v
	}
	return out
}

func (r *Router) dispatchTerminal(ctx context.Context, device string, call mcp.ToolCall) (any, error) {
	if call.SessionID == "" {
		return nil, errors.New("Hub caller session is required")
	}
	privilege := "owner"
	if raw, exists := call.Arguments["privilege"]; exists {
		value, ok := raw.(string)
		if !ok || (value != "owner" && value != "admin") {
			return nil, errors.New("invalid privilege")
		}
		privilege = value
	}
	scope := sessionBinding{device: device, caller: call.SessionID, privilege: privilege}
	action, _ := call.Arguments["action"].(string)
	creating := call.Name == "terminal" && action == "create"
	needsID := call.Name == "terminal_output" || call.Name == "terminal" && !creating || call.Name == "terminal_sessions" && action == "inspect"
	hubID := ""
	if creating {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, errors.New("could not allocate Hub session")
		}
		hubID = "hs_" + hex.EncodeToString(random[:])
		r.mu.Lock()
		if len(r.sessions) >= maximumHubSessions {
			r.mu.Unlock()
			return nil, errors.New("Hub session capacity reached")
		}
		r.sessions[hubID] = scope
		r.mu.Unlock()
	} else if needsID {
		hubID, _ = call.Arguments["sessionId"].(string)
		r.mu.Lock()
		binding, ok := r.sessions[hubID]
		r.mu.Unlock()
		if !ok || binding.remote == "" || binding.device != device || binding.caller != scope.caller || binding.privilege != privilege {
			return nil, errors.New("session does not belong to this caller, device and privilege")
		}
		call.Arguments["sessionId"] = binding.remote
	}
	result, err := r.relay.Call(ctx, device, call)
	if err != nil {
		if creating {
			r.mu.Lock()
			delete(r.sessions, hubID)
			r.mu.Unlock()
		}
		return nil, relayCallError(err)
	}
	if creating {
		record, ok := result.(map[string]any)
		remote, _ := record["ID"].(string)
		if !ok || remote == "" {
			r.mu.Lock()
			delete(r.sessions, hubID)
			r.mu.Unlock()
			return nil, ErrUnconfirmed
		}
		scope.remote = remote
		r.mu.Lock()
		r.sessions[hubID] = scope
		r.mu.Unlock()
		out := copyRecord(record)
		out["ID"] = hubID
		return out, nil
	}
	if call.Name == "terminal" && action == "close" {
		record, _ := result.(map[string]any)
		if record["ok"] == true {
			r.mu.Lock()
			delete(r.sessions, hubID)
			r.mu.Unlock()
		}
	}
	if call.Name == "terminal_sessions" && action == "inspect" {
		record, _ := result.(map[string]any)
		session, _ := record["Session"].(map[string]any)
		if session["ID"] != call.Arguments["sessionId"] {
			return nil, errors.New("session response identity mismatch")
		}
		return replaceSessionID(result, hubID)
	}
	if call.Name == "terminal_sessions" && action == "list" {
		entries, ok := result.([]any)
		if !ok {
			return nil, errors.New("invalid session list")
		}
		visible := []any{}
		r.mu.Lock()
		ids := map[string]string{}
		for id, binding := range r.sessions {
			if binding.device == device && binding.caller == scope.caller && binding.privilege == privilege && binding.remote != "" {
				ids[binding.remote] = id
			}
		}
		r.mu.Unlock()
		for _, entry := range entries {
			record, _ := entry.(map[string]any)
			session, _ := record["Session"].(map[string]any)
			remote, _ := session["ID"].(string)
			if id, ok := ids[remote]; ok {
				filtered, err := replaceSessionID(entry, id)
				if err != nil {
					return nil, err
				}
				visible = append(visible, filtered)
			}
		}
		return visible, nil
	}
	return result, nil
}

func replaceSessionID(value any, id string) (any, error) {
	record, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("invalid session result")
	}
	session, ok := record["Session"].(map[string]any)
	if !ok {
		return nil, errors.New("invalid session result")
	}
	out := copyRecord(record)
	nested := copyRecord(session)
	nested["ID"] = id
	out["Session"] = nested
	return out, nil
}
