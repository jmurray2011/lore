package domain

import (
	"crypto/sha256"
	"encoding/hex"
)

// ContentHash is the SHA-256 of document content, hex-encoded.
// Hash equality is what makes ingestion idempotent.
type ContentHash string

// HashContent computes the ContentHash of raw content.
func HashContent(content []byte) ContentHash {
	sum := sha256.Sum256(content)
	return ContentHash(hex.EncodeToString(sum[:]))
}
