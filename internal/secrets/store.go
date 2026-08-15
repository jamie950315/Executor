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
	BrokerIPCKey    string `json:"broker_ipc_key"`
	DesktopIPCKey   string `json:"desktop_ipc_key"`
	OAuthKey        string `json:"oauth_signing_key"`
	DashboardKey    string `json:"dashboard_key"`
	Generation      uint64 `json:"generation"`
}

func Create(dir string) (Values, error) {
	release, err := acquireStoreLock(dir)
	if err != nil {
		return Values{}, err
	}
	defer release()

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
	if values.Generation == 0 || values.RecoveryKeyHash == "" || values.URLSecretHash == "" || values.BrokerIPCKey == "" || values.DesktopIPCKey == "" || values.OAuthKey == "" || values.DashboardKey == "" {
		return Values{}, fmt.Errorf("incomplete secret store")
	}
	return values, nil
}

func Rotate(dir string) (Values, error) {
	release, err := acquireStoreLock(dir)
	if err != nil {
		return Values{}, err
	}
	defer release()

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
	items := make([]string, 6)
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
		BrokerIPCKey:    items[2],
		DesktopIPCKey:   items[3],
		OAuthKey:        items[4],
		DashboardKey:    items[5],
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
	existing, statErr := os.Stat(path)
	if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}
	tmpFile, err := os.CreateTemp(dir, "."+secretsFile+".*.tmp")
	if err != nil {
		return err
	}
	tmp := tmpFile.Name()
	closed := false
	defer func() {
		if !closed {
			_ = tmpFile.Close()
		}
		_ = os.Remove(tmp)
	}()
	if _, err := tmpFile.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := tmpFile.Chmod(0o600); err != nil {
		return err
	}
	if existing != nil {
		if err := preserveFileOwnership(tmpFile, existing); err != nil {
			return err
		}
	}
	if err := tmpFile.Sync(); err != nil {
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	closed = true
	if err := replaceFileDurable(tmp, path); err != nil {
		return err
	}
	return nil
}
