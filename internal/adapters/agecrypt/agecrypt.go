// Package agecrypt is lore's encryption boundary for portable artifacts: it
// wraps a whole serialized artifact byte stream in an age envelope (and reverses
// it), so the exported file is encrypted at rest and in transit. It deliberately
// does nothing clever — age (filippo.io/age) owns the header, nonce, KDF, and
// AEAD framing, and the artifacts it writes are ordinary age files decryptable
// by the standalone `age` tool. The whole stream is encrypted, vectors included
// (vectors are invertible to approximate plaintext, so encrypting text but not
// vectors would leak the corpus).
package agecrypt

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"filippo.io/age"
)

// ErrDecrypt means the artifact could not be decrypted: a wrong passphrase, an
// identity matching no recipient, or a tampered/corrupt ciphertext. age's AEAD
// makes these indistinguishable by design — all mean "you do not have the key,
// or the bytes changed" — so they share one sentinel the CLI maps to exit 1.
var ErrDecrypt = errors.New("could not decrypt")

// age stream headers, for detecting encryption from content (not extension).
const (
	binaryHeader = "age-encryption.org/v1"
	armorHeader  = "-----BEGIN AGE ENCRYPTED FILE-----"
)

// IsEncrypted reports whether data looks like an age stream (binary or armored),
// so import detects encryption from the artifact itself.
func IsEncrypted(data []byte) bool {
	return bytes.HasPrefix(data, []byte(binaryHeader)) || bytes.HasPrefix(data, []byte(armorHeader))
}

// EncryptPassphrase wraps plaintext in an age passphrase (scrypt) stream.
func EncryptPassphrase(plaintext []byte, passphrase string) ([]byte, error) {
	r, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return nil, fmt.Errorf("age: passphrase recipient: %w", err)
	}
	return encrypt(plaintext, r)
}

// EncryptRecipients wraps plaintext encrypted to the given age recipients
// (age1... X25519 public keys); any can later decrypt with their identity.
func EncryptRecipients(plaintext []byte, recipients []string) ([]byte, error) {
	if len(recipients) == 0 {
		return nil, fmt.Errorf("age: at least one recipient is required")
	}
	rs := make([]age.Recipient, 0, len(recipients))
	for _, s := range recipients {
		r, err := age.ParseX25519Recipient(s)
		if err != nil {
			return nil, fmt.Errorf("age: parse recipient %q: %w", s, err)
		}
		rs = append(rs, r)
	}
	return encrypt(plaintext, rs...)
}

func encrypt(plaintext []byte, recipients ...age.Recipient) ([]byte, error) {
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, recipients...)
	if err != nil {
		return nil, fmt.Errorf("age: encrypt: %w", err)
	}
	if _, err := w.Write(plaintext); err != nil {
		return nil, fmt.Errorf("age: encrypt: %w", err)
	}
	if err := w.Close(); err != nil { // flushes the final chunk and MAC
		return nil, fmt.Errorf("age: encrypt: %w", err)
	}
	return buf.Bytes(), nil
}

// DecryptPassphrase decrypts an age passphrase stream; a wrong passphrase or
// tampered ciphertext is ErrDecrypt.
func DecryptPassphrase(ciphertext []byte, passphrase string) ([]byte, error) {
	id, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, fmt.Errorf("age: passphrase identity: %w", err)
	}
	return decrypt(ciphertext, id)
}

// DecryptIdentities decrypts using the identities parsed from an age identity
// file's contents; a non-matching identity or tampered ciphertext is ErrDecrypt.
func DecryptIdentities(ciphertext, identityFile []byte) ([]byte, error) {
	ids, err := age.ParseIdentities(bytes.NewReader(identityFile))
	if err != nil {
		return nil, fmt.Errorf("age: parse identities: %w", err)
	}
	return decrypt(ciphertext, ids...)
}

func decrypt(ciphertext []byte, identities ...age.Identity) ([]byte, error) {
	r, err := age.Decrypt(bytes.NewReader(ciphertext), identities...)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecrypt, err)
	}
	// With age's streaming AEAD, a tampered payload surfaces here, on read, not at
	// Decrypt (which only authenticates the header), so this is also ErrDecrypt.
	out, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecrypt, err)
	}
	return out, nil
}
