package secrets

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const secretsFile = "secrets.json"

type Values struct {
	RecoveryKey     string `json:"-"`
	URLSecret       string `json:"-"`
	RecoveryKeyHash string `json:"recovery_key_hash"`
	URLSecretHash   string `json:"url_secret_hash"`
	IPCKey          string `json:"ipc_key"`
	OAuthKey        string `json:"oauth_signing_key"`
	DashboardKey    string `json:"dashboard_key"`
	Generation      uint64 `json:"generation"`
}

func Create(dir string) (Values, error) {
	if _, err := os.Stat(filepath.Join(dir, secretsFile)); err == nil {
		return Values{}, fmt.Errorf("secrets already exist")
	} else if !os.IsNotExist(err) {
		return Values{}, err
	}
	values, err := generate(1)
	if err != nil {
		return Values{}, err
	}
	return values, save(dir, values)
}

func Load(dir string) (Values, error) {
	data, err := os.ReadFile(filepath.Join(dir, secretsFile))
	if err != nil {
		return Values{}, err
	}
	var values Values
	if err := json.Unmarshal(data, &values); err != nil {
		return Values{}, err
	}
	if values.Generation == 0 || values.RecoveryKeyHash == "" || values.URLSecretHash == "" || values.IPCKey == "" || values.OAuthKey == "" || values.DashboardKey == "" {
		return Values{}, fmt.Errorf("incomplete secret store")
	}
	return values, nil
}

func Rotate(dir string) (Values, error) {
	current, err := Load(dir)
	if err != nil {
		return Values{}, err
	}
	values, err := generate(current.Generation + 1)
	if err != nil {
		return Values{}, err
	}
	return values, save(dir, values)
}

func generate(generation uint64) (Values, error) {
	items := make([]string, 5)
	for i := range items {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return Values{}, err
		}
		items[i] = base64.RawURLEncoding.EncodeToString(buf)
	}
	return Values{
		RecoveryKey:     items[0],
		URLSecret:       items[1],
		RecoveryKeyHash: secretHash(items[0]),
		URLSecretHash:   secretHash(items[1]),
		IPCKey:          items[2],
		OAuthKey:        items[3],
		DashboardKey:    items[4],
		Generation:      generation,
	}, nil
}

func (v Values) VerifyRecoveryKey(candidate string) bool {
	return verifyHash(v.RecoveryKeyHash, candidate)
}

func (v Values) VerifyURLSecret(candidate string) bool {
	return verifyHash(v.URLSecretHash, candidate)
}

func secretHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func verifyHash(want, candidate string) bool {
	got := secretHash(candidate)
	return len(want) == len(got) && subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}

func save(dir string, values Values) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, secretsFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
