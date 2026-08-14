// Package secrets seals the few things guard stores that are not telemetry.
//
// Telemetry is public by nature — it is what a service chose to say about
// itself. An SSH password is the opposite: it is a login to somebody's machine,
// sitting in a SQLite file that gets copied to laptops, backed up to object
// storage and attached to bug reports. Encrypting it there does not stop
// somebody who already runs this process, and is not meant to: it stops the
// database file itself from being a list of credentials.
//
// One key, AES-256-GCM, nonce in front of every ciphertext. No key rotation and
// no per-record keys — both are real features and neither is worth having in a
// dashboard that stores at most one password per machine.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Keeper seals and opens secrets with one key.
type Keeper struct {
	aead cipher.AEAD
	// Ephemeral says the key was generated for this process and stored
	// nowhere. Anything sealed with it is unreadable after a restart, which is
	// a thing the UI has to be able to say out loud rather than reporting the
	// password as corrupt.
	Ephemeral bool
}

// version prefixes every ciphertext. It costs one byte and it is the difference
// between changing the scheme later and never being able to.
const version byte = 1

// ErrUnreadable is what Open returns when the bytes do not decrypt: the key
// changed, or the row came from a different instance. Distinguished from a
// storage error because the answer is different — this one is "set it again",
// not "something is broken".
var ErrUnreadable = errors.New("this secret was sealed with a different key")

// New builds a keeper from raw key material of any length. The bytes are hashed
// rather than used directly so a short passphrase is still a 32-byte key.
func New(material []byte) (*Keeper, error) {
	key := sha256.Sum256(material)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Keeper{aead: aead}, nil
}

// Open finds the key for a database, in the order that keeps the common case
// working and the careful case possible.
//
//  1. GUARD_SECRET_KEY, when set. A deployment that already has a secret
//     manager should keep the key there and not on the disk it is protecting.
//  2. A key file beside the database, created on first use with mode 0600. This
//     is what happens if you do nothing, and it is honest about what it buys:
//     a stolen database file is useless without the file next to it, a stolen
//     directory is not.
//  3. A key held in memory only — for ":memory:" databases and tests. Sealed
//     values do not survive the process, which is why Keeper says so.
func Open(dbPath string) *Keeper {
	if material := strings.TrimSpace(os.Getenv("GUARD_SECRET_KEY")); material != "" {
		keeper, err := New(decodeKey(material))
		if err == nil {
			return keeper
		}
		slog.Error("GUARD_SECRET_KEY could not be used", slog.Any("err", err))
	}
	if path := keyFile(dbPath); path != "" {
		keeper, err := fromFile(path)
		if err == nil {
			return keeper
		}
		slog.Warn("no key file for stored secrets — SSH passwords will not survive a restart",
			slog.String("path", path), slog.Any("err", err))
	}
	return Ephemeral()
}

// decodeKey accepts a base64 or hex 32-byte key as itself, and anything else as
// a passphrase. Guessing wrong is harmless — every path ends in a SHA-256 — but
// a key generated with `openssl rand -base64 32` should be that key rather than
// the hash of its printable form.
func decodeKey(material string) []byte {
	if raw, err := base64.StdEncoding.DecodeString(material); err == nil && len(raw) == 32 {
		return raw
	}
	if raw, err := hex.DecodeString(material); err == nil && len(raw) == 32 {
		return raw
	}
	return []byte(material)
}

func keyFile(dbPath string) string {
	dbPath = strings.TrimSpace(dbPath)
	if dbPath == "" || strings.HasPrefix(dbPath, ":") || strings.HasPrefix(dbPath, "file::memory:") {
		return ""
	}
	return dbPath + ".key"
}

func fromFile(path string) (*Keeper, error) {
	material, err := os.ReadFile(path)
	if err == nil && len(material) > 0 {
		return New(decodeKey(strings.TrimSpace(string(material))))
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// 0600, and written with O_EXCL: two processes starting together must not
	// each generate a key and leave one of them holding secrets it can no
	// longer read.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return fromFile(path)
	}
	if err != nil {
		return nil, err
	}
	if _, err := file.WriteString(encoded); err != nil {
		file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	slog.Info("generated a key for stored secrets", slog.String("path", path))
	return New(key)
}

// Ephemeral is a keeper with a key that exists only in this process — for an
// in-memory database, and for tests. Anything it seals is unreadable after the
// process ends, which is the correct behaviour for a store that ends with it.
func Ephemeral() *Keeper {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		// A process that cannot read random bytes cannot serve TLS either.
		panic(fmt.Sprintf("secrets: %v", err))
	}
	keeper, err := New(key)
	if err != nil {
		panic(fmt.Sprintf("secrets: %v", err))
	}
	keeper.Ephemeral = true
	return keeper
}

// Seal encrypts. An empty string seals to no bytes at all, so "no password" is
// stored as an empty column rather than as an encrypted empty string that
// nothing can tell apart from one.
func (k *Keeper) Seal(plain string) ([]byte, error) {
	if plain == "" {
		return nil, nil
	}
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, 1+len(nonce)+len(plain)+k.aead.Overhead())
	out = append(out, version)
	out = append(out, nonce...)
	return k.aead.Seal(out, nonce, []byte(plain), nil), nil
}

// Open decrypts. ErrUnreadable means the key is not the one this was sealed
// with — the only failure a person can act on, and the only one worth showing.
func (k *Keeper) Open(sealed []byte) (string, error) {
	if len(sealed) == 0 {
		return "", nil
	}
	if sealed[0] != version {
		return "", ErrUnreadable
	}
	nonceSize := k.aead.NonceSize()
	if len(sealed) < 1+nonceSize {
		return "", ErrUnreadable
	}
	plain, err := k.aead.Open(nil, sealed[1:1+nonceSize], sealed[1+nonceSize:], nil)
	if err != nil {
		return "", ErrUnreadable
	}
	return string(plain), nil
}
