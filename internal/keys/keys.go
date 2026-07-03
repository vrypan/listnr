package keys

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
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
		block, _ := pem.Decode(b)
		if block == nil || block.Type != "RSA PRIVATE KEY" {
			return nil, fmt.Errorf("%s: not an RSA private key", path)
		}
		return x509.ParsePKCS1PrivateKey(block.Bytes)
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

// PublicPEM renders the public half in the SPKI PEM form ActivityPub expects.
func PublicPEM(key *rsa.PrivateKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}
