package keys

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const keyFile = "actor.pem"

// LoadOrCreate returns the actor's RSA keypair from dataDir, generating and
// persisting one on first run.
func LoadOrCreate(dataDir string) (*rsa.PrivateKey, error) {
	path := filepath.Join(dataDir, keyFile)
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		key, err := ParsePrivatePEM(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return key, nil
	case errors.Is(err, os.ErrNotExist):
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, err
		}
		b := pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(key),
		})
		if err := os.WriteFile(path, b, 0600); err != nil {
			return nil, err
		}
		return key, nil
	default:
		return nil, err
	}
}

func ParsePrivatePEM(b []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "RSA PRIVATE KEY" {
		return nil, errors.New("not an RSA private key")
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

func Fingerprint(key *rsa.PrivateKey) string {
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// ParsePublicPEM parses a remote actor's public key. Accepts both SPKI
// ("PUBLIC KEY") and PKCS#1 ("RSA PUBLIC KEY") encodings.
func ParsePublicPEM(pemStr string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("no PEM block in public key")
	}
	if key, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
		return key, nil
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is %T, not RSA", pub)
	}
	return rsaPub, nil
}

// PublicPEM renders the public half in the SPKI PEM form ActivityPub expects.
func PublicPEM(key *rsa.PrivateKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}
