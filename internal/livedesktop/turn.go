package livedesktop

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/pion/stun/v3"
)

// TURNConfig is loaded only from protected host state, never public config or UI.
type TURNConfig struct {
	URLs   []string `json:"urls"`
	Secret string   `json:"-"`
}

func (c TURNConfig) String() string {
	return "TURNConfig{servers:" + strconv.Itoa(len(c.URLs)) + ",secret:redacted}"
}

type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

type Connectivity struct {
	ICEServers []ICEServer `json:"iceServers"`
	ExpiresAt  int64       `json:"expiresAt,omitempty"`
}

// Connectivity is invoked only through the authenticated owner RPC. Status
// never issues credentials; each explicit start obtains a fresh short-lived set.
func (m *Manager) Connectivity(ctx context.Context) (Connectivity, error) {
	status, err := m.Status(ctx)
	if err != nil || !status.Available {
		return Connectivity{}, errors.New("desktop unavailable for relay authorization")
	}
	result := Connectivity{ICEServers: []ICEServer{}}
	for _, url := range m.cfg.ICE {
		result.ICEServers = append(result.ICEServers, ICEServer{URLs: []string{url}})
	}
	if m.cfg.TURN != nil {
		now := time.Now()
		credential, err := m.cfg.TURN.credentials(now)
		if err != nil {
			return Connectivity{}, err
		}
		result.ICEServers = append(result.ICEServers, credential)
		result.ExpiresAt = now.Add(time.Hour).Unix()
	}
	return result, nil
}

func (c TURNConfig) validate() error {
	if len(c.Secret) < 32 || len(c.Secret) > 512 || strings.ContainsAny(c.Secret, "\r\n\x00") || len(c.URLs) == 0 || len(c.URLs) > 4 {
		return errors.New("invalid protected TURN configuration")
	}
	for _, raw := range c.URLs {
		u, err := stun.ParseURI(raw)
		if err != nil || (!strings.HasPrefix(raw, "turn:") && !strings.HasPrefix(raw, "turns:")) || strings.ContainsAny(raw, "@\r\n\t ") || u.Host == "" || u.Username != "" || u.Password != "" {
			return errors.New("invalid TURN server URL")
		}
	}
	return nil
}

func LoadTURNConfig(path string) (*TURNConfig, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("cannot inspect protected TURN configuration")
	}
	// Windows privacy is provided by the installed state-directory ACL. Unix
	// additionally rejects a group/world-readable credential file.
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return nil, errors.New("TURN configuration must be a private regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot read protected TURN configuration")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) || (runtime.GOOS != "windows" && opened.Mode().Perm()&0077 != 0) {
		return nil, errors.New("TURN configuration changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, 8193))
	if err != nil || len(data) > 8192 {
		return nil, errors.New("invalid TURN configuration size")
	}
	var disk struct {
		URLs   []string `json:"urls"`
		Secret string   `json:"secret"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err = d.Decode(&disk); err != nil {
		return nil, errors.New("invalid protected TURN configuration")
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid protected TURN configuration")
	}
	c := TURNConfig{URLs: disk.URLs, Secret: disk.Secret}
	if err = c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c TURNConfig) credentials(now time.Time) (ICEServer, error) {
	if err := c.validate(); err != nil {
		return ICEServer{}, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return ICEServer{}, errors.New("cannot issue TURN credentials")
	}
	username := strconv.FormatInt(now.Add(time.Hour).Unix(), 10) + ":" + base64.RawURLEncoding.EncodeToString(nonce[:])
	mac := hmac.New(sha1.New, []byte(c.Secret))
	_, _ = mac.Write([]byte(username))
	return ICEServer{URLs: append([]string(nil), c.URLs...), Username: username, Credential: base64.StdEncoding.EncodeToString(mac.Sum(nil))}, nil
}
