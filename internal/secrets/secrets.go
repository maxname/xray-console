// Package secrets encrypts what the panel must store but must not leak with a
// copy of its database: the 2FA secret now, SSH and REALITY private keys later.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const keyFileName = "master.key"
const keySize = 32

// Box seals and opens values with the panel's master key (AES-256-GCM).
type Box struct {
	aead cipher.AEAD
}

// LoadOrCreate reads the master key from dataDir, generating it on first run.
//
// The key lives in a file next to the database rather than being derived from
// the administrator's password: the panel has to reach its servers on its own
// (availability checks, certificate renewals) while nobody is signed in.
func LoadOrCreate(dataDir string) (*Box, error) {
	path := filepath.Join(dataDir, keyFileName)

	key, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		key = make([]byte, keySize)
		if _, err := io.ReadFull(rand.Reader, key); err != nil {
			return nil, fmt.Errorf("generate master key: %w", err)
		}
		// O_EXCL: never overwrite a key that appeared in the meantime —
		// everything already sealed with it would become unreadable.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, fmt.Errorf("create master key: %w", err)
		}
		if _, err := f.Write(key); err != nil {
			f.Close()
			return nil, fmt.Errorf("write master key: %w", err)
		}
		if err := f.Close(); err != nil {
			return nil, fmt.Errorf("write master key: %w", err)
		}
	case err != nil:
		return nil, fmt.Errorf("read master key: %w", err)
	default:
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("read master key: %w", err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("%s is readable by other users (mode %04o); run: chmod 600 %s",
				path, info.Mode().Perm(), path)
		}
	}

	return New(key)
}

// New builds a Box from a raw 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("master key must be %d bytes, got %d", keySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plaintext. The result is nonce || ciphertext.
func (b *Box) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open decrypts a value produced by Seal.
func (b *Box) Open(sealed []byte) ([]byte, error) {
	size := b.aead.NonceSize()
	if len(sealed) < size {
		return nil, errors.New("sealed value is too short")
	}
	plaintext, err := b.aead.Open(nil, sealed[:size], sealed[size:], nil)
	if err != nil {
		return nil, errors.New("sealed value does not match the master key")
	}
	return plaintext, nil
}
