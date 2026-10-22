// Package secrets encrypts small values (API keys) at rest with AES-256-GCM. The key lives in a
// 0600 file (or OA_SECRET_KEY) outside the database, so a database dump alone does not reveal secrets.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Box struct{ aead cipher.AEAD }

var ErrCorrupt = errors.New("secret cannot be decrypted (different key or damaged data)")

func newBox(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, errors.New("secret key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: g}, nil
}

// Open takes the key from envKey (64 hex chars) if set, otherwise from keyFile, creating a random key file
// (mode 0600) when it does not exist.
func Open(envKey, keyFile string) (*Box, error) {
	if envKey = strings.TrimSpace(envKey); envKey != "" {
		k, err := hex.DecodeString(envKey)
		if err != nil || len(k) != 32 {
			return nil, errors.New("OA_SECRET_KEY must be 64 hex characters")
		}
		return newBox(k)
	}
	raw, err := os.ReadFile(keyFile)
	if errors.Is(err, os.ErrNotExist) {
		k := make([]byte, 32)
		if _, err := rand.Read(k); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(keyFile), 0o700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(keyFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		_, werr := f.WriteString(hex.EncodeToString(k) + "\n")
		if cerr := f.Close(); werr != nil || cerr != nil {
			return nil, fmt.Errorf("write secret key file: %v %v", werr, cerr)
		}
		return newBox(k)
	}
	if err != nil {
		return nil, err
	}
	k, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(k) != 32 {
		return nil, fmt.Errorf("secret key file %s is damaged", keyFile)
	}
	return newBox(k)
}

// Seal returns nonce||ciphertext.
func (b *Box) Seal(plain []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plain, nil), nil
}

func (b *Box) Unseal(sealed []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n+b.aead.Overhead() {
		return nil, ErrCorrupt
	}
	out, err := b.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return nil, ErrCorrupt
	}
	return out, nil
}
