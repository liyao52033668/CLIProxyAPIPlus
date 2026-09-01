package codearts

// DPoP (RFC 9449) proof JWT: ES256 + P-256, used for the snap-manager / STS
// oauth2/tokens endpoints. Aligned with the huaweicloud.authentication extension.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"time"
)

// dpopKeyPair holds a DPoP key pair.
type dpopKeyPair struct {
	PrivateKey *ecdsa.PrivateKey
	PublicJWK  map[string]string
}

func newDpopKeyPair() (*dpopKeyPair, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		return nil, err
	}
	jwk := map[string]string{
		"kty": "EC",
		"crv": "P-256",
		"x":   base64.RawURLEncoding.EncodeToString(padded(key.PublicKey.X, 32)),
		"y":   base64.RawURLEncoding.EncodeToString(padded(key.PublicKey.Y, 32)),
	}
	return &dpopKeyPair{PrivateKey: key, PublicJWK: jwk}, nil
}

// signDpopProof generates a DPoP JWT for the given token endpoint (htm=POST).
func signDpopProof(kp *dpopKeyPair, htu string) (string, error) {
	header := map[string]any{
		"alg": "ES256",
		"typ": "dpop+jwt",
		"jwk": kp.PublicJWK,
	}
	headerJSON, _ := json.Marshal(header)
	payload := map[string]any{
		"htm": "POST",
		"htu": htu,
		"iat": time.Now().Unix(),
		"jti": dpopRandomHex(16),
	}
	payloadJSON, _ := json.Marshal(payload)
	input := b64(headerJSON) + "." + b64(payloadJSON)
	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(crand.Reader, kp.PrivateKey, digest[:])
	if err != nil {
		return "", err
	}
	// Low-S normalization for better upstream compatibility.
	n := elliptic.P256().Params().N
	halfN := new(big.Int).Rsh(n, 1)
	if s.Cmp(halfN) > 0 {
		s.Sub(n, s)
	}
	sig := append(padded(r, 32), padded(s, 32)...)
	return input + "." + b64(sig), nil
}

func padded(b *big.Int, size int) []byte {
	out := make([]byte, size)
	raw := b.Bytes()
	copy(out[size-len(raw):], raw)
	return out
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func dpopRandomHex(n int) string {
	b := make([]byte, n)
	if _, err := crand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", b)
}
