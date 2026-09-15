package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jamie950315/executor/internal/relay"
)

const maximumRelayBody = 80 << 20

// DashboardRelay uses the Hub machine API only. It never uses browser cookies
// or device MCP URLs. The server must verify the Hub's current device delegation
// on every call; bearer authentication alone does not authorize host control.
type DashboardRelay struct {
	origin string
	token  string
	client *http.Client
}

func NewDashboardRelay(origin, token string, client *http.Client) (*DashboardRelay, error) {
	u, err := url.Parse(origin)
	if err != nil {
		return nil, errors.New("invalid Dashboard origin")
	}
	loopback := net.ParseIP(u.Hostname())
	secure := u.Scheme == "https" || u.Scheme == "http" && loopback != nil && loopback.IsLoopback()
	if !secure || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(origin, "#") || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("Dashboard must be an HTTPS origin or explicit loopback test origin")
	}
	if token == "" || strings.ContainsAny(token, "\r\n\t ") {
		return nil, errors.New("Hub machine credential is required")
	}
	if client == nil {
		client = &http.Client{Timeout: 125 * time.Second}
	}
	owned := *client
	owned.Jar = nil
	owned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &DashboardRelay{origin: strings.TrimRight(origin, "/"), token: token, client: &owned}, nil
}

func (d *DashboardRelay) Devices(ctx context.Context) ([]Device, error) {
	var response struct {
		Devices []Device `json:"devices"`
	}
	if err := d.request(ctx, http.MethodGet, "/api/hub/devices", nil, &response); err != nil {
		return nil, err
	}
	if response.Devices == nil {
		return nil, errors.New("invalid Hub device directory")
	}
	return response.Devices, nil
}

func (d *DashboardRelay) Submit(ctx context.Context, id string, payload relay.SignedHubRequest) (any, error) {
	if id == "" || strings.ContainsAny(id, "/\\?#\r\n") {
		return nil, errors.New("invalid device identifier")
	}
	if payload.Signature == "" || payload.Request.DeviceID != id {
		return nil, errors.New("signed device-bound Hub request required")
	}
	var result any
	err := d.request(ctx, http.MethodPost, "/api/hub/devices/"+url.PathEscape(id)+"/call", payload, &result)
	return result, err
}

func (d *DashboardRelay) request(ctx context.Context, method, path string, payload, output any) error {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil || len(data) > maximumRelayBody {
			return errors.New("invalid or oversized Hub request")
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, d.origin+path, body)
	if err != nil {
		return errors.New("invalid Hub request")
	}
	request.Header.Set("Authorization", "Bearer "+d.token)
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := d.client.Do(request)
	if err != nil {
		return errors.New("Hub transport unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Hub response status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maximumRelayBody+1))
	if err != nil || len(data) > maximumRelayBody {
		return errors.New("incomplete or oversized Hub response")
	}
	if err := json.Unmarshal(data, output); err != nil {
		return errors.New("invalid Hub response")
	}
	return nil
}
