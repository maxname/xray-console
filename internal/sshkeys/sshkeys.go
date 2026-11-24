// Package sshkeys keeps the private keys the panel signs in to servers with.
package sshkeys

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"xray-console/internal/secrets"
)

// PanelKeyName is the key the panel creates for itself and installs on every
// server it is given a password for.
const PanelKeyName = "panel-ed25519"

// Key is one stored key. The private half never leaves this package except as
// a Signer.
type Key struct {
	ID        int64
	Name      string
	Algorithm string
	// PublicKey is a complete authorized_keys line.
	PublicKey   string
	Fingerprint string
	CreatedAt   time.Time

	signer ssh.Signer
}

// Signer signs SSH handshakes with this key.
func (k *Key) Signer() ssh.Signer { return k.signer }

// AuthorizedKey is the public key without its comment — the part to compare
// when looking for it in authorized_keys.
func (k *Key) AuthorizedKey() string {
	fields := strings.Fields(k.PublicKey)
	if len(fields) < 2 {
		return k.PublicKey
	}
	return fields[0] + " " + fields[1]
}

type Store struct {
	db  *sql.DB
	box *secrets.Box
	// mu makes "create the panel key if it is missing" happen once even when
	// two requests ask for it at the same moment.
	mu sync.Mutex
}

func NewStore(db *sql.DB, box *secrets.Box) *Store {
	return &Store{db: db, box: box}
}

// PanelKey returns the panel's own key, generating it the first time.
func (s *Store) PanelKey(ctx context.Context) (*Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key, err := s.byName(ctx, PanelKeyName)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	sshPublic, err := ssh.NewPublicKey(public)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(private, PanelKeyName)
	if err != nil {
		return nil, err
	}
	sealed, err := s.box.Seal(pem.EncodeToMemory(block))
	if err != nil {
		return nil, err
	}
	// The comment is how the line is recognised in authorized_keys by a person
	// reading the file.
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPublic))) + " xray-console " + PanelKeyName

	if _, err := s.db.ExecContext(ctx, `INSERT INTO ssh_keys
		(name, algorithm, public_key, fingerprint, private_key, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		PanelKeyName, "ed25519", line, ssh.FingerprintSHA256(sshPublic), sealed, time.Now().Unix()); err != nil {
		return nil, err
	}
	return s.byName(ctx, PanelKeyName)
}

func (s *Store) byName(ctx context.Context, name string) (*Key, error) {
	var key Key
	var sealed []byte
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT id, name, algorithm, public_key, fingerprint, private_key, created_at
		FROM ssh_keys WHERE name = ?`, name,
	).Scan(&key.ID, &key.Name, &key.Algorithm, &key.PublicKey, &key.Fingerprint, &sealed, &created)
	if err != nil {
		return nil, err
	}
	key.CreatedAt = time.Unix(created, 0)

	pemBytes, err := s.box.Open(sealed)
	if err != nil {
		return nil, fmt.Errorf("SSH key %q: %w", name, err)
	}
	if key.signer, err = ssh.ParsePrivateKey(pemBytes); err != nil {
		return nil, fmt.Errorf("SSH key %q: %w", name, err)
	}
	return &key, nil
}
