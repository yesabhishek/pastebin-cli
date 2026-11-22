package vault

import (
	"bytes"
	"errors"
	"testing"
)

func TestCreateUnlockRoundTrip(t *testing.T) {
	file, keys, err := Create([]byte("correct horse battery"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	data, err := file.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(data, []byte("correct horse")) {
		t.Fatalf("vault file leaks passphrase")
	}
	parsed, err := Parse(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	unlocked, err := parsed.Unlock([]byte("correct horse battery"))
	if err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if unlocked.Export() != keys.Export() {
		t.Fatalf("unlocked key differs from created key")
	}
	if _, err := parsed.Unlock([]byte("wrong passphrase!!")); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("expected wrong passphrase error, got %v", err)
	}
}

func TestWeakPassphraseRejected(t *testing.T) {
	if _, _, err := Create([]byte("short")); !errors.Is(err, ErrWeakPassphrase) {
		t.Fatalf("expected weak passphrase error, got %v", err)
	}
}

func TestSealOpenBindsKindAndID(t *testing.T) {
	_, keys, err := Create([]byte("correct horse battery"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := keys.ObjectID("notes/today.md")
	blob, err := keys.Seal("obj", id, []byte("secret note"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Contains(blob, []byte("secret note")) {
		t.Fatalf("ciphertext contains plaintext")
	}
	plain, err := keys.Open("obj", id, blob)
	if err != nil || string(plain) != "secret note" {
		t.Fatalf("open: %q %v", plain, err)
	}
	if _, err := keys.Open("obj", keys.ObjectID("notes/other.md"), blob); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("expected swapped-object detection, got %v", err)
	}
	if _, err := keys.Open("index", id, blob); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("expected kind binding, got %v", err)
	}
	blob[len(blob)-1] ^= 1
	if _, err := keys.Open("obj", id, blob); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("expected tamper detection, got %v", err)
	}
}

func TestObjectIDIsStableAndOpaque(t *testing.T) {
	_, keys, _ := Create([]byte("correct horse battery"))
	a := keys.ObjectID("notes/today.md")
	if a != keys.ObjectID("notes/today.md") {
		t.Fatalf("object id not stable")
	}
	if a == keys.ObjectID("notes/today2.md") {
		t.Fatalf("object ids collide")
	}
	_, other, _ := Create([]byte("correct horse battery"))
	if a == other.ObjectID("notes/today.md") {
		t.Fatalf("object id must depend on the vault key")
	}
}

func TestChangePassphraseKeepsDataKey(t *testing.T) {
	file, keys, _ := Create([]byte("correct horse battery"))
	if err := file.ChangePassphrase(keys, []byte("new passphrase here")); err != nil {
		t.Fatalf("change: %v", err)
	}
	if _, err := file.Unlock([]byte("correct horse battery")); err == nil {
		t.Fatalf("old passphrase still works")
	}
	unlocked, err := file.Unlock([]byte("new passphrase here"))
	if err != nil || unlocked.Export() != keys.Export() {
		t.Fatalf("new passphrase failed: %v", err)
	}
}

func TestParseRejectsHostileKDF(t *testing.T) {
	data := []byte(`{"version":1,"id":"x","wrapped_key":"AA==","kdf":{"name":"argon2id","time":1,"memory_kib":4194304,"threads":1,"salt":"AA=="}}`)
	if _, err := Parse(data); err == nil {
		t.Fatalf("expected out-of-range KDF to be rejected")
	}
}

func TestImportExport(t *testing.T) {
	_, keys, _ := Create([]byte("correct horse battery"))
	back, err := Import(keys.Export())
	if err != nil || back.ObjectID("a") != keys.ObjectID("a") {
		t.Fatalf("import/export mismatch: %v", err)
	}
}
