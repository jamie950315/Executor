package secrets

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
)

const secretsFile = "secrets.json"

type Values struct {
	RecoveryKey     string          `json:"-"`
	URLSecret       string          `json:"-"`
	RecoveryKeyHash string          `json:"recovery_key_hash"`
	URLSecretHash   string          `json:"url_secret_hash"`
	BrokerIPCKey    string          `json:"broker_ipc_key"`
	DesktopIPCKey   string          `json:"desktop_ipc_key"`
	OAuthKey        string          `json:"oauth_signing_key"`
	DashboardKey    string          `json:"dashboard_key"`
	RelayPrivateJWK json.RawMessage `json:"relay_private_jwk"`
	Generation      uint64          `json:"generation"`
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
	values, err := generate(1, nil)
	if err != nil {
		return Values{}, err
	}
	return values, save(dir, values)
}

func Load(dir string) (Values, error) {
	release, err := acquireStoreLock(dir)
	if err != nil {
		return Values{}, err
	}
	defer release()
	return loadUnlocked(dir, true)
}

func loadUnlocked(dir string, migrate bool) (Values, error) {
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
	if len(values.RelayPrivateJWK) == 0 && migrate {
		identity, err := generateRelayPrivateJWK()
		if err != nil {
			return Values{}, err
		}
		values.RelayPrivateJWK = identity
		if err := save(dir, values); err != nil {
			return Values{}, err
		}
	}
	if !validRelayPrivateJWK(values.RelayPrivateJWK) {
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

	current, err := loadUnlocked(dir, true)
	if err != nil {
		return Values{}, err
	}
	values, err := generate(current.Generation+1, current.RelayPrivateJWK)
	if err != nil {
		return Values{}, err
	}
	return values, save(dir, values)
}

func generate(generation uint64, relayPrivateJWK json.RawMessage) (Values, error) {
	items := make([]string, 6)
	for i := range items {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return Values{}, err
		}
		items[i] = base64.RawURLEncoding.EncodeToString(buf)
	}
	if len(relayPrivateJWK) == 0 {
		var err error
		relayPrivateJWK, err = generateRelayPrivateJWK()
		if err != nil {
			return Values{}, err
		}
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
		RelayPrivateJWK: append(json.RawMessage(nil), relayPrivateJWK...),
		Generation:      generation,
	}, nil
}

type relayPrivateJWK struct {
	KeyType string `json:"kty"`
	Curve   string `json:"crv"`
	X       string `json:"x"`
	Y       string `json:"y"`
	D       string `json:"d"`
}

func generateRelayPrivateJWK() (json.RawMessage, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, errors.New("generate relay identity")
	}
	encode := func(value *big.Int) string {
		field := make([]byte, 32)
		value.FillBytes(field)
		return base64.RawURLEncoding.EncodeToString(field)
	}
	encoded, err := json.Marshal(relayPrivateJWK{
		KeyType: "EC", Curve: "P-256", X: encode(key.X), Y: encode(key.Y), D: encode(key.D),
	})
	if err != nil {
		return nil, errors.New("generate relay identity")
	}
	return encoded, nil
}

func validRelayPrivateJWK(data json.RawMessage) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var jwk relayPrivateJWK
	if err := decoder.Decode(&jwk); err != nil || decoder.More() || jwk.KeyType != "EC" || jwk.Curve != "P-256" {
		return false
	}
	decode := func(value string) ([]byte, bool) {
		field, err := base64.RawURLEncoding.Strict().DecodeString(value)
		return field, err == nil && len(field) == 32
	}
	xBytes, xOK := decode(jwk.X)
	yBytes, yOK := decode(jwk.Y)
	dBytes, dOK := decode(jwk.D)
	if !xOK || !yOK || !dOK {
		return false
	}
	x, y, d := new(big.Int).SetBytes(xBytes), new(big.Int).SetBytes(yBytes), new(big.Int).SetBytes(dBytes)
	if d.Sign() <= 0 || d.Cmp(elliptic.P256().Params().N) >= 0 || !elliptic.P256().IsOnCurve(x, y) {
		return false
	}
	derivedX, derivedY := elliptic.P256().ScalarBaseMult(dBytes)
	return derivedX.Cmp(x) == 0 && derivedY.Cmp(y) == 0
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
