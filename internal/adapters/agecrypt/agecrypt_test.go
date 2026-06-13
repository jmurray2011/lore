package agecrypt_test

import (
	"bytes"
	"errors"
	"testing"

	"filippo.io/age"

	"github.com/jmurray2011/lore/internal/adapters/agecrypt"
)

var plaintext = []byte("LORECORP\x00\x00\x00\x01the whole serialized artifact, vectors and all")

func TestPassphraseRoundTrip(t *testing.T) {
	ct, err := agecrypt.EncryptPassphrase(plaintext, "correct horse battery staple")
	if err != nil {
		t.Fatalf("EncryptPassphrase: %v", err)
	}
	if bytes.Contains(ct, []byte("vectors")) {
		t.Error("ciphertext leaks plaintext")
	}
	if !agecrypt.IsEncrypted(ct) {
		t.Error("IsEncrypted should be true for an age stream")
	}
	got, err := agecrypt.DecryptPassphrase(ct, "correct horse battery staple")
	if err != nil {
		t.Fatalf("DecryptPassphrase: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("round-trip mismatch:\n got %q\nwant %q", got, plaintext)
	}
}

func TestWrongPassphrase(t *testing.T) {
	ct, err := agecrypt.EncryptPassphrase(plaintext, "right")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agecrypt.DecryptPassphrase(ct, "wrong"); !errors.Is(err, agecrypt.ErrDecrypt) {
		t.Errorf("want ErrDecrypt, got %v", err)
	}
}

func TestTamperedCiphertext(t *testing.T) {
	ct, err := agecrypt.EncryptPassphrase(plaintext, "pw")
	if err != nil {
		t.Fatal(err)
	}
	// Flip a byte near the end (in the payload, past the header) — AEAD must reject.
	ct[len(ct)-1] ^= 0xff
	if _, err := agecrypt.DecryptPassphrase(ct, "pw"); !errors.Is(err, agecrypt.ErrDecrypt) {
		t.Errorf("tampered ciphertext should fail to decrypt, got %v", err)
	}
}

func TestRecipientIdentityRoundTrip(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	ct, err := agecrypt.EncryptRecipients(plaintext, []string{id.Recipient().String()})
	if err != nil {
		t.Fatalf("EncryptRecipients: %v", err)
	}
	got, err := agecrypt.DecryptIdentities(ct, []byte(id.String()))
	if err != nil {
		t.Fatalf("DecryptIdentities: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Error("recipient/identity round-trip mismatch")
	}

	// A different identity must not decrypt it.
	other, _ := age.GenerateX25519Identity()
	if _, err := agecrypt.DecryptIdentities(ct, []byte(other.String())); !errors.Is(err, agecrypt.ErrDecrypt) {
		t.Errorf("wrong identity: want ErrDecrypt, got %v", err)
	}
}

func TestIsEncryptedFalseForPlaintext(t *testing.T) {
	if agecrypt.IsEncrypted([]byte("LORECORP\x00\x00\x00\x01...")) {
		t.Error("a plaintext lore artifact must not look encrypted")
	}
	if agecrypt.IsEncrypted(nil) {
		t.Error("empty input is not encrypted")
	}
}
