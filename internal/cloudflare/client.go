package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

type Logger interface {
	Printf(format string, args ...any)
}

type Option func(*Client)

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
	logger     Logger
}

type Account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Zone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Tunnel struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Token string `json:"token,omitempty"`
}

type DNSRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
}

type IngressRule struct {
	Hostname string
	Service  string
}

type DeploymentRequest struct {
	AccountID             string
	ZoneID                string
	TunnelName            string
	Hostname              string
	LocalServiceURL       string
	TokenFilePath         string
	CloudflaredBinaryPath string
}

type DeploymentResult struct {
	Tunnel                Tunnel
	DNS                   DNSRecord
	CloudflaredConfig     string
	CloudflaredRunCommand string
	TokenFilePath         string
	CreatedTunnel         bool
	CreatedDNS            bool
}

type TunnelOutcome struct {
	Tunnel  Tunnel
	Created bool
}

type DNSOutcome struct {
	Record   DNSRecord
	Created  bool
	Updated  bool
	Previous *DNSRecord
}

type apiEnvelope[T any] struct {
	Success bool       `json:"success"`
	Result  T          `json:"result"`
	Errors  []apiError `json:"errors"`
}

type apiError struct {
	Message string `json:"message"`
}

func NewClient(baseURL, token string, opts ...Option) *Client {
	client := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		httpClient: http.DefaultClient,
	}
	for _, opt := range opts {
		opt(client)
	}
	return client
}

func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) {
		if httpClient != nil {
			c.httpClient = httpClient
		}
	}
}

func WithLogger(logger Logger) Option {
	return func(c *Client) {
		c.logger = logger
	}
}

func (c *Client) ListAccounts(ctx context.Context) ([]Account, error) {
	var env apiEnvelope[[]Account]
	if err := c.do(ctx, http.MethodGet, "/accounts", nil, &env); err != nil {
		return nil, err
	}
	return env.Result, nil
}

func (c *Client) ListZones(ctx context.Context, accountID string) ([]Zone, error) {
	path := "/zones?account.id=" + url.QueryEscape(accountID)
	var env apiEnvelope[[]Zone]
	if err := c.do(ctx, http.MethodGet, path, nil, &env); err != nil {
		return nil, err
	}
	return env.Result, nil
}

func (c *Client) Apply(ctx context.Context, req DeploymentRequest) (DeploymentResult, error) {
	out := DeploymentResult{}
	created := rollbackState{}

	tunnelOutcome, err := c.EnsureTunnel(ctx, req.AccountID, req.TunnelName)
	if err != nil {
		return out, err
	}
	out.Tunnel = tunnelOutcome.Tunnel
	out.CreatedTunnel = tunnelOutcome.Created
	if tunnelOutcome.Created {
		created.AccountID = req.AccountID
		created.TunnelID = tunnelOutcome.Tunnel.ID
	}

	tunnelToken, err := c.GetTunnelToken(ctx, req.AccountID, tunnelOutcome.Tunnel)
	if err != nil {
		_ = c.rollback(ctx, created)
		return out, err
	}
	if err := WriteTunnelTokenFile(req.TokenFilePath, tunnelToken); err != nil {
		_ = c.rollback(ctx, created)
		return out, err
	}
	created.TokenFilePath = req.TokenFilePath
	out.TokenFilePath = req.TokenFilePath

	if !tunnelOutcome.Created {
		previousConfig, err := c.GetTunnelConfig(ctx, req.AccountID, tunnelOutcome.Tunnel.ID)
		if err != nil {
			_ = c.rollback(ctx, created)
			return out, err
		}
		created.AccountID = req.AccountID
		created.TunnelID = tunnelOutcome.Tunnel.ID
		created.PreviousTunnelConfig = previousConfig
	}

	rules := []IngressRule{{Hostname: req.Hostname, Service: req.LocalServiceURL}}
	if err := c.ConfigureTunnel(ctx, req.AccountID, tunnelOutcome.Tunnel.ID, rules); err != nil {
		_ = c.rollback(ctx, created)
		return out, err
	}

	dnsOutcome, err := c.EnsureDNSRecord(ctx, req.ZoneID, req.Hostname, tunnelOutcome.Tunnel.CNAMETarget())
	if err != nil {
		_ = c.rollback(ctx, created)
		return out, err
	}
	out.DNS = dnsOutcome.Record
	out.CreatedDNS = dnsOutcome.Created
	if dnsOutcome.Created {
		created.ZoneID = req.ZoneID
		created.DNSRecordID = dnsOutcome.Record.ID
	}
	if dnsOutcome.Previous != nil {
		created.ZoneID = req.ZoneID
		created.DNSRecordID = dnsOutcome.Record.ID
		created.PreviousDNSRecord = dnsOutcome.Previous
	}

	runCommand, err := BuildCloudflaredRunCommand(req.CloudflaredBinaryPath, req.TokenFilePath)
	if err != nil {
		_ = c.rollback(ctx, created)
		return out, err
	}
	out.CloudflaredRunCommand = runCommand
	return out, nil
}

func (c *Client) EnsureTunnel(ctx context.Context, accountID, name string) (TunnelOutcome, error) {
	var list apiEnvelope[[]Tunnel]
	path := fmt.Sprintf("/accounts/%s/cfd_tunnel?name=%s", url.PathEscape(accountID), url.QueryEscape(name))
	if err := c.do(ctx, http.MethodGet, path, nil, &list); err != nil {
		return TunnelOutcome{}, err
	}
	for _, tunnel := range list.Result {
		if tunnel.Name == name {
			return TunnelOutcome{Tunnel: tunnel}, nil
		}
	}

	payload := map[string]any{"name": name, "config_src": "cloudflare"}
	var created apiEnvelope[Tunnel]
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("/accounts/%s/cfd_tunnel", url.PathEscape(accountID)), payload, &created); err != nil {
		return TunnelOutcome{}, err
	}
	return TunnelOutcome{Tunnel: created.Result, Created: true}, nil
}

func (c *Client) GetTunnelToken(ctx context.Context, accountID string, tunnel Tunnel) (string, error) {
	if tunnel.Token != "" {
		return tunnel.Token, nil
	}
	var env apiEnvelope[string]
	path := fmt.Sprintf("/accounts/%s/cfd_tunnel/%s/token", url.PathEscape(accountID), url.PathEscape(tunnel.ID))
	if err := c.do(ctx, http.MethodGet, path, nil, &env); err != nil {
		return "", err
	}
	return env.Result, nil
}

func (c *Client) ConfigureTunnel(ctx context.Context, accountID, tunnelID string, rules []IngressRule) error {
	ingress, err := buildIngressPayload(rules)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"config": map[string]any{
			"ingress": ingress,
		},
	}
	var env apiEnvelope[map[string]any]
	path := fmt.Sprintf("/accounts/%s/cfd_tunnel/%s/configurations", url.PathEscape(accountID), url.PathEscape(tunnelID))
	return c.do(ctx, http.MethodPut, path, payload, &env)
}

func (c *Client) GetTunnelConfig(ctx context.Context, accountID, tunnelID string) (map[string]any, error) {
	var env apiEnvelope[map[string]any]
	path := fmt.Sprintf("/accounts/%s/cfd_tunnel/%s/configurations", url.PathEscape(accountID), url.PathEscape(tunnelID))
	if err := c.do(ctx, http.MethodGet, path, nil, &env); err != nil {
		return nil, err
	}
	config, _ := env.Result["config"].(map[string]any)
	return config, nil
}

func (c *Client) EnsureDNSRecord(ctx context.Context, zoneID, hostname, target string) (DNSOutcome, error) {
	var list apiEnvelope[[]DNSRecord]
	path := fmt.Sprintf("/zones/%s/dns_records?type=CNAME&name=%s", url.PathEscape(zoneID), url.QueryEscape(hostname))
	if err := c.do(ctx, http.MethodGet, path, nil, &list); err != nil {
		return DNSOutcome{}, err
	}
	for _, record := range list.Result {
		if record.Name == hostname && strings.EqualFold(record.Type, "CNAME") {
			if record.Content == target && record.Proxied {
				return DNSOutcome{Record: record}, nil
			}
			payload := map[string]any{
				"type":    "CNAME",
				"name":    hostname,
				"content": target,
				"proxied": true,
			}
			var updated apiEnvelope[DNSRecord]
			updatePath := fmt.Sprintf("/zones/%s/dns_records/%s", url.PathEscape(zoneID), url.PathEscape(record.ID))
			if err := c.do(ctx, http.MethodPatch, updatePath, payload, &updated); err != nil {
				return DNSOutcome{}, err
			}
			previous := record
			recordAfter := updated.Result
			if recordAfter.ID == "" {
				recordAfter.ID = record.ID
			}
			if recordAfter.Type == "" {
				recordAfter.Type = "CNAME"
			}
			if recordAfter.Name == "" {
				recordAfter.Name = hostname
			}
			if recordAfter.Content == "" {
				recordAfter.Content = target
			}
			return DNSOutcome{Record: recordAfter, Updated: true, Previous: &previous}, nil
		}
	}

	payload := map[string]any{
		"type":    "CNAME",
		"name":    hostname,
		"content": target,
		"proxied": true,
	}
	var created apiEnvelope[DNSRecord]
	if err := c.do(ctx, http.MethodPost, fmt.Sprintf("/zones/%s/dns_records", url.PathEscape(zoneID)), payload, &created); err != nil {
		return DNSOutcome{}, err
	}
	return DNSOutcome{Record: created.Result, Created: true}, nil
}

func (c *Client) rollback(ctx context.Context, state rollbackState) error {
	var errs []error
	if state.TokenFilePath != "" {
		if err := os.Remove(state.TokenFilePath); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	if state.PreviousDNSRecord != nil {
		payload := map[string]any{
			"type":    state.PreviousDNSRecord.Type,
			"name":    state.PreviousDNSRecord.Name,
			"content": state.PreviousDNSRecord.Content,
			"proxied": state.PreviousDNSRecord.Proxied,
		}
		path := fmt.Sprintf("/zones/%s/dns_records/%s", url.PathEscape(state.ZoneID), url.PathEscape(state.DNSRecordID))
		if err := c.do(ctx, http.MethodPatch, path, payload, nil); err != nil {
			errs = append(errs, err)
		}
	} else if state.DNSRecordID != "" {
		path := fmt.Sprintf("/zones/%s/dns_records/%s", url.PathEscape(state.ZoneID), url.PathEscape(state.DNSRecordID))
		if err := c.do(ctx, http.MethodDelete, path, nil, nil); err != nil {
			errs = append(errs, err)
		}
	}
	if state.PreviousTunnelConfig != nil {
		payload := map[string]any{"config": state.PreviousTunnelConfig}
		path := fmt.Sprintf("/accounts/%s/cfd_tunnel/%s/configurations", url.PathEscape(state.AccountID), url.PathEscape(state.TunnelID))
		if err := c.do(ctx, http.MethodPut, path, payload, nil); err != nil {
			errs = append(errs, err)
		}
	} else if state.TunnelID != "" {
		path := fmt.Sprintf("/accounts/%s/cfd_tunnel/%s", url.PathEscape(state.AccountID), url.PathEscape(state.TunnelID))
		if err := c.do(ctx, http.MethodDelete, path, nil, nil); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (t Tunnel) CNAMETarget() string {
	return t.ID + ".cfargotunnel.com"
}

func (c *Client) do(ctx context.Context, method, path string, payload any, out any) error {
	fullURL := c.baseURL + path
	var body io.Reader
	if payload != nil {
		buf := &bytes.Buffer{}
		if err := json.NewEncoder(buf).Encode(payload); err != nil {
			return err
		}
		body = buf
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.logf("cloudflare %s %s", method, path)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if out == nil {
		if resp.StatusCode >= http.StatusBadRequest {
			return c.decodeError(resp)
		}
		io.Copy(io.Discard, resp.Body)
		return nil
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return decodeEnvelopeError(out, resp.Status)
	}
	return decodeEnvelopeError(out, "")
}

func (c *Client) decodeError(resp *http.Response) error {
	var env apiEnvelope[any]
	if err := json.NewDecoder(resp.Body).Decode(&env); err == nil {
		return decodeAPIError(env.Errors, resp.Status)
	}
	return fmt.Errorf("cloudflare API returned %s", resp.Status)
}

func decodeEnvelopeError(out any, status string) error {
	type errored interface {
		apiErrors() []apiError
		apiSuccess() bool
	}
	if env, ok := out.(errored); ok {
		if !env.apiSuccess() || len(env.apiErrors()) > 0 {
			return decodeAPIError(env.apiErrors(), status)
		}
	}
	switch env := out.(type) {
	case *apiEnvelope[[]Account]:
		if !env.Success || len(env.Errors) > 0 {
			return decodeAPIError(env.Errors, status)
		}
	case *apiEnvelope[[]Zone]:
		if !env.Success || len(env.Errors) > 0 {
			return decodeAPIError(env.Errors, status)
		}
	case *apiEnvelope[[]Tunnel]:
		if !env.Success || len(env.Errors) > 0 {
			return decodeAPIError(env.Errors, status)
		}
	case *apiEnvelope[Tunnel]:
		if !env.Success || len(env.Errors) > 0 {
			return decodeAPIError(env.Errors, status)
		}
	case *apiEnvelope[[]DNSRecord]:
		if !env.Success || len(env.Errors) > 0 {
			return decodeAPIError(env.Errors, status)
		}
	case *apiEnvelope[DNSRecord]:
		if !env.Success || len(env.Errors) > 0 {
			return decodeAPIError(env.Errors, status)
		}
	case *apiEnvelope[map[string]any]:
		if !env.Success || len(env.Errors) > 0 {
			return decodeAPIError(env.Errors, status)
		}
	case *apiEnvelope[string]:
		if !env.Success || len(env.Errors) > 0 {
			return decodeAPIError(env.Errors, status)
		}
	}
	return nil
}

func decodeAPIError(errs []apiError, status string) error {
	if len(errs) > 0 {
		msg := errs[0].Message
		if msg == "" {
			msg = "cloudflare API request failed"
		}
		if status != "" {
			return fmt.Errorf("%s: %s", status, msg)
		}
		return errors.New(msg)
	}
	if status != "" {
		return fmt.Errorf("cloudflare API returned %s", status)
	}
	return nil
}

func (c *Client) logf(format string, args ...any) {
	if c.logger != nil {
		c.logger.Printf(fmt.Sprintf(format, args...))
	}
}

type rollbackState struct {
	AccountID            string
	ZoneID               string
	TunnelID             string
	DNSRecordID          string
	TokenFilePath        string
	PreviousTunnelConfig map[string]any
	PreviousDNSRecord    *DNSRecord
}
