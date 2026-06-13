package artifact_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jmurray2011/lore/internal/artifact"
)

func sampleBundle() artifact.Bundle {
	return artifact.Bundle{
		Collection: artifact.Collection{
			Name:       "kb",
			Model:      "text-embedding-3-large",
			Dimensions: 3072,
			CreatedAt:  time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC),
			Sources:    []string{"file:///docs", "file:///more"},
			Chunker: artifact.Chunker{
				Strategy: "structure", Version: 1, Size: 512, Overlap: 64,
				Tokenizer: "o200k_base", ContextPrefix: true,
			},
		},
		Documents: []artifact.Document{
			{
				SourceURI: "file:///docs/a.md", Hash: "abc123", Fingerprint: "fp1",
				IngestedAt: time.Date(2026, 6, 13, 12, 1, 0, 0, time.UTC),
				Chunks: []artifact.Chunk{
					{Seq: 0, Text: "alpha", HeadingPath: "Intro", Vector: []float32{0.1, 0.2, 0.3}},
					{Seq: 1, Text: "beta", Vector: []float32{0.4, 0.5, 0.6}},
				},
			},
		},
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	want := sampleBundle()
	var buf bytes.Buffer
	if err := artifact.Write(&buf, want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := artifact.Read(&buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestReadRejectsBadMagic(t *testing.T) {
	for _, in := range [][]byte{
		nil,
		[]byte("nope"),
		[]byte("NOTLOREXmore-bytes-here"),
	} {
		if _, err := artifact.Read(bytes.NewReader(in)); !errors.Is(err, artifact.ErrBadFormat) {
			t.Errorf("input %q: want ErrBadFormat, got %v", in, err)
		}
	}
}

func TestReadRejectsNewerVersion(t *testing.T) {
	var buf bytes.Buffer
	buf.WriteString(artifact.Magic)
	var ver [4]byte
	binary.BigEndian.PutUint32(ver[:], artifact.FormatVersion+1)
	buf.Write(ver[:])
	buf.WriteString("whatever gob would be")

	if _, err := artifact.Read(&buf); !errors.Is(err, artifact.ErrUnsupportedVersion) {
		t.Errorf("want ErrUnsupportedVersion, got %v", err)
	}
}
