package domain

// Metadata is a small, flat set of structured attributes attached to a Document
// and carried on its chunks' index entries for retrieval filtering. Values are
// stored canonically as strings; the typed comparison semantics (numeric, date,
// glob, tag membership) live in Predicate, not in the storage. Keys are
// caller-defined: user-supplied --meta pairs, extracted front-matter, or
// path/mtime-derived fields.
type Metadata map[string]string

// Clone returns an independent copy so callers may retain or mutate it without
// aliasing stored state (adapters return copies, like vectors). Nil clones to nil.
func (m Metadata) Clone() Metadata {
	if m == nil {
		return nil
	}
	out := make(Metadata, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
