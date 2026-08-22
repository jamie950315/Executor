package relay

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
)

var ErrInvalidDeviceKey = errors.New("invalid device key")

type PrivateKeyJWK struct {
	KeyType string `json:"kty"`
	Curve   string `json:"crv"`
	X       string `json:"x"`
	Y       string `json:"y"`
	D       string `json:"d"`
}

type DeviceIdentity struct {
	key *ecdsa.PrivateKey
}

func GenerateDeviceIdentity() (*DeviceIdentity, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, errors.New("device identity generation failed")
	}
	return &DeviceIdentity{key: key}, nil
}

func ParseDeviceIdentity(data []byte) (*DeviceIdentity, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var jwk PrivateKeyJWK
	if err := decoder.Decode(&jwk); err != nil {
		return nil, ErrInvalidDeviceKey
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, ErrInvalidDeviceKey
	}
	publicKey, err := ParsePublicKeyJWK(PublicKeyJWK{
		KeyType: jwk.KeyType,
		Curve:   jwk.Curve,
		X:       jwk.X,
		Y:       jwk.Y,
	})
	if err != nil {
		return nil, ErrInvalidDeviceKey
	}
	privateBytes, err := decodeP256Field(jwk.D)
	if err != nil {
		return nil, ErrInvalidDeviceKey
	}
	privateScalar := new(big.Int).SetBytes(privateBytes)
	if privateScalar.Sign() <= 0 || privateScalar.Cmp(elliptic.P256().Params().N) >= 0 {
		return nil, ErrInvalidDeviceKey
	}
	derivedX, derivedY := elliptic.P256().ScalarBaseMult(privateBytes)
	if subtle.ConstantTimeCompare(paddedP256Field(derivedX), paddedP256Field(publicKey.X)) != 1 ||
		subtle.ConstantTimeCompare(paddedP256Field(derivedY), paddedP256Field(publicKey.Y)) != 1 {
		return nil, ErrInvalidDeviceKey
	}
	return &DeviceIdentity{key: &ecdsa.PrivateKey{
		PublicKey: *publicKey,
		D:         privateScalar,
	}}, nil
}

func (identity *DeviceIdentity) MarshalPrivateJWK() ([]byte, error) {
	if !validPrivateKey(identity) {
		return nil, ErrInvalidDeviceKey
	}
	public := identity.PublicJWK()
	encoded, err := json.Marshal(PrivateKeyJWK{
		KeyType: public.KeyType,
		Curve:   public.Curve,
		X:       public.X,
		Y:       public.Y,
		D:       encodeP256Field(identity.key.D),
	})
	if err != nil {
		return nil, ErrInvalidDeviceKey
	}
	return encoded, nil
}

func (identity *DeviceIdentity) PublicJWK() PublicKeyJWK {
	if !validPrivateKey(identity) {
		return PublicKeyJWK{}
	}
	return PublicKeyJWK{
		KeyType: "EC",
		Curve:   "P-256",
		X:       encodeP256Field(identity.key.X),
		Y:       encodeP256Field(identity.key.Y),
	}
}

func ParsePublicKeyJWK(jwk PublicKeyJWK) (*ecdsa.PublicKey, error) {
	if jwk.KeyType != "EC" || jwk.Curve != "P-256" {
		return nil, ErrInvalidDeviceKey
	}
	xBytes, err := decodeP256Field(jwk.X)
	if err != nil {
		return nil, ErrInvalidDeviceKey
	}
	yBytes, err := decodeP256Field(jwk.Y)
	if err != nil {
		return nil, ErrInvalidDeviceKey
	}
	x := new(big.Int).SetBytes(xBytes)
	y := new(big.Int).SetBytes(yBytes)
	if !elliptic.P256().IsOnCurve(x, y) {
		return nil, ErrInvalidDeviceKey
	}
	return &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, nil
}

func (identity *DeviceIdentity) publicKey() *ecdsa.PublicKey {
	if !validPrivateKey(identity) {
		return nil
	}
	return &identity.key.PublicKey
}

func validPrivateKey(identity *DeviceIdentity) bool {
	return identity != nil && identity.key != nil && identity.key.Curve == elliptic.P256() &&
		identity.key.D != nil && identity.key.D.Sign() > 0 && identity.key.D.Cmp(elliptic.P256().Params().N) < 0 &&
		identity.key.X != nil && identity.key.Y != nil && elliptic.P256().IsOnCurve(identity.key.X, identity.key.Y)
}

func decodeP256Field(value string) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return nil, ErrInvalidDeviceKey
	}
	return decoded, nil
}

func encodeP256Field(value *big.Int) string {
	return base64.RawURLEncoding.EncodeToString(paddedP256Field(value))
}

func paddedP256Field(value *big.Int) []byte {
	field := make([]byte, 32)
	value.FillBytes(field)
	return field
}
