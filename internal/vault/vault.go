// Package vault implements pb's end-to-end encryption.
//
// Design:
//
//   - A random 256-bit data key (DEK) is generated once per notes repository.
//   - The DEK is wrapped (AES-256-GCM) with a key-encryption key derived from
//     the user's passphrase using Argon2id. The wrapped DEK is stored in the
//     repository as meta/vault.json, so any machine that knows the passphrase
//     can unlock the vault. The passphrase itself never leaves the machine.
//   - Content and name keys are derived from the DEK with HKDF-SHA256.
//   - Every note is sealed with AES-256-GCM under a fresh random nonce, with the
//     object kind and opaque object ID bound as additional authenticated data,
//     so ciphertexts cannot be swapped between objects without detection.
//   - Note paths are never stored in clear on the remote: objects are addressed
//     by HMAC-SHA256(nameKey, path), and the path index is itself encrypted.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	FormatVersion = 1
	keySize       = 32
	nonceSize     = 12
	saltSize      = 16

	// MinPassphraseLength is enforced when creating or changing a passphrase.
	MinPassphraseLength = 10

	// Default Argon2id cost (RFC 9106 "second recommended" profile, 64 MiB).
	defaultTime    = 3
	defaultMemory  = 64 * 1024
	defaultThreads = 4

	// Upper bounds protect against a tampered vault file forcing huge work.
	maxTime    = 16
	maxMemory  = 1024 * 1024 // 1 GiB in KiB
	maxThreads = 16
)

var (
	blobMagic = []byte("PBE1")

	ErrWrongPassphrase = errors.New("wrong passphrase")
	ErrDecrypt         = errors.New("decryption failed: data is corrupted, tampered with, or encrypted with a different key")
	ErrWeakPassphrase  = fmt.Errorf("passphrase must be at least %d characters", MinPassphraseLength)
)

// KDFParams describes how the key-encryption key is derived.
type KDFParams struct {
	Name    string `json:"name"`
	Time    uint32 `json:"time"`
	Memory  uint32 `json:"memory_kib"`
	Threads uint8  `json:"threads"`
	Salt    string `json:"salt"`
}

// File is the JSON document stored at meta/vault.json. It contains no secrets
// that are usable without the passphrase.
type File struct {
	Version   int       `json:"version"`
	ID        string    `json:"id"`
	KDF       KDFParams `json:"kdf"`
	Wrapped   string    `json:"wrapped_key"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Keys holds the unlocked key material for one vault.
type Keys struct {
	VaultID string
	dek     []byte
	content []byte
	names   []byte
}

// Create generates a fresh data key and wraps it with the passphrase.
func Create(passphrase []byte) (*File, *Keys, error) {
	if len(passphrase) < MinPassphraseLength {
		return nil, nil, ErrWeakPassphrase
	}
	dek := make([]byte, keySize)
	if _, err := rand.Read(dek); err != nil {
		return nil, nil, err
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	file := &File{Version: FormatVersion, ID: hex.EncodeToString(idBytes), CreatedAt: now}
	if err := file.wrap(dek, passphrase); err != nil {
		return nil, nil, err
	}
	keys, err := NewKeys(file.ID, dek)
	if err != nil {
		return nil, nil, err
	}
	return file, keys, nil
}

// Parse decodes and validates a vault file.
func Parse(data []byte) (*File, error) {
	var file File
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse vault file: %w", err)
	}
	if file.Version != FormatVersion {
		return nil, fmt.Errorf("unsupported vault version %d; upgrade pb", file.Version)
	}
	if file.ID == "" || file.Wrapped == "" {
		return nil, errors.New("vault file is incomplete")
	}
	if err := file.KDF.validate(); err != nil {
		return nil, err
	}
	return &file, nil
}

// Marshal encodes the vault file for storage.
func (f *File) Marshal() ([]byte, error) {
	return json.MarshalIndent(f, "", "  ")
}

// Unlock derives the key-encryption key and unwraps the data key.
func (f *File) Unlock(passphrase []byte) (*Keys, error) {
	if err := f.KDF.validate(); err != nil {
		return nil, err
	}
	salt, err := base64.StdEncoding.DecodeString(f.KDF.Salt)
	if err != nil {
		return nil, fmt.Errorf("decode vault salt: %w", err)
	}
	wrapped, err := base64.StdEncoding.DecodeString(f.Wrapped)
	if err != nil {
		return nil, fmt.Errorf("decode wrapped key: %w", err)
	}
	kek := argon2.IDKey(passphrase, salt, f.KDF.Time, f.KDF.Memory, f.KDF.Threads, keySize)
	defer zero(kek)
	dek, err := openRaw(kek, wrapped, f.wrapAAD())
	if err != nil {
		return nil, ErrWrongPassphrase
	}
	if len(dek) != keySize {
		return nil, ErrDecrypt
	}
	return NewKeys(f.ID, dek)
}

// ChangePassphrase rewraps the existing data key with a new passphrase. The
// notes themselves do not need to be re-encrypted.
func (f *File) ChangePassphrase(keys *Keys, newPassphrase []byte) error {
	if len(newPassphrase) < MinPassphraseLength {
		return ErrWeakPassphrase
	}
	if keys == nil || keys.VaultID != f.ID {
		return errors.New("keys do not belong to this vault")
	}
	f.UpdatedAt = time.Now().UTC()
	return f.wrap(keys.dek, newPassphrase)
}

func (f *File) wrap(dek, passphrase []byte) error {
	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	f.KDF = KDFParams{
		Name:    "argon2id",
		Time:    defaultTime,
		Memory:  defaultMemory,
		Threads: defaultThreads,
		Salt:    base64.StdEncoding.EncodeToString(salt),
	}
	kek := argon2.IDKey(passphrase, salt, f.KDF.Time, f.KDF.Memory, f.KDF.Threads, keySize)
	defer zero(kek)
	wrapped, err := sealRaw(kek, dek, f.wrapAAD())
	if err != nil {
		return err
	}
	f.Wrapped = base64.StdEncoding.EncodeToString(wrapped)
	return nil
}

func (f *File) wrapAAD() []byte {
	return []byte("pb-vault-v1:" + f.ID)
}

func (p KDFParams) validate() error {
	if p.Name != "argon2id" {
		return fmt.Errorf("unsupported key derivation %q", p.Name)
	}
	if p.Time == 0 || p.Time > maxTime || p.Memory < 8*1024 || p.Memory > maxMemory || p.Threads == 0 || p.Threads > maxThreads {
		return errors.New("vault key-derivation parameters are out of range")
	}
	return nil
}

// NewKeys derives sub-keys from a raw data key.
func NewKeys(vaultID string, dek []byte) (*Keys, error) {
	if len(dek) != keySize {
		return nil, errors.New("invalid data key length")
	}
	content, err := hkdf.Key(sha256.New, dek, nil, "pb content v1", keySize)
	if err != nil {
		return nil, err
	}
	names, err := hkdf.Key(sha256.New, dek, nil, "pb names v1", keySize)
	if err != nil {
		return nil, err
	}
	return &Keys{VaultID: vaultID, dek: append([]byte(nil), dek...), content: content, names: names}, nil
}

// Export returns a compact string for the local OS keychain.
func (k *Keys) Export() string {
	return k.VaultID + ":" + base64.StdEncoding.EncodeToString(k.dek)
}

// Import parses a string produced by Export.
func Import(s string) (*Keys, error) {
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			dek, err := base64.StdEncoding.DecodeString(s[i+1:])
			if err != nil {
				return nil, fmt.Errorf("decode cached key: %w", err)
			}
			return NewKeys(s[:i], dek)
		}
	}
	return nil, errors.New("cached key is malformed")
}

// ObjectID returns the opaque remote identifier for a logical note path.
func (k *Keys) ObjectID(path string) string {
	mac := hmac.New(sha256.New, k.names)
	mac.Write([]byte(path))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

// Seal encrypts plaintext for the given object kind and ID.
func (k *Keys) Seal(kind, id string, plaintext []byte) ([]byte, error) {
	ct, err := sealRaw(k.content, plaintext, blobAAD(kind, id))
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(blobMagic)+len(ct))
	out = append(out, blobMagic...)
	return append(out, ct...), nil
}

// Open decrypts a blob produced by Seal for the same kind and ID.
func (k *Keys) Open(kind, id string, blob []byte) ([]byte, error) {
	if len(blob) < len(blobMagic) || subtle.ConstantTimeCompare(blob[:len(blobMagic)], blobMagic) != 1 {
		return nil, ErrDecrypt
	}
	plain, err := openRaw(k.content, blob[len(blobMagic):], blobAAD(kind, id))
	if err != nil {
		return nil, ErrDecrypt
	}
	return plain, nil
}

func blobAAD(kind, id string) []byte {
	return []byte("pb-blob-v1|" + kind + "|" + id)
}

func sealRaw(key, plaintext, aad []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, aad), nil
}

func openRaw(key, data, aad []byte) ([]byte, error) {
	if len(data) < nonceSize {
		return nil, ErrDecrypt
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, data[:nonceSize], data[nonceSize:], aad)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
