package domain

import (
	"fmt"
	"strings"
)

// Attachment is raw content handed to a Generator alongside retrieved chunks —
// an image or document the model reads directly. Unlike ingested content it is
// ephemeral: never chunked, embedded, or stored (DESIGN.md, "Document formats
// and multimodal"). How it is encoded for a provider, and whether the provider
// accepts it, are the Generator adapter's concern.
type Attachment struct {
	MediaType string // e.g. "image/png", "application/pdf"
	Name      string // original filename, for display/citation; optional
	Data      []byte
}

// NewAttachment validates and constructs an Attachment. Name may be empty.
func NewAttachment(mediaType, name string, data []byte) (Attachment, error) {
	if strings.TrimSpace(mediaType) == "" {
		return Attachment{}, fmt.Errorf("attachment: %w: media type must not be empty", ErrInvalidArgument)
	}
	if len(data) == 0 {
		return Attachment{}, fmt.Errorf("attachment: %w: data must not be empty", ErrInvalidArgument)
	}
	return Attachment{MediaType: mediaType, Name: name, Data: data}, nil
}
