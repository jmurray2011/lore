package domain

import (
	"regexp"
	"strings"
)

// ClaimVerdict is the faithfulness verdict for one claim (sentence) of an answer.
type ClaimVerdict string

const (
	// VerdictSupported: the claim is entailed by the chunk(s) it cites.
	VerdictSupported ClaimVerdict = "supported"
	// VerdictUnsupported: the claim cites chunks but is not entailed by them.
	VerdictUnsupported ClaimVerdict = "unsupported"
	// VerdictUncited: the claim cites no chunk in the grounding set, so it cannot
	// be verified — flagged rather than trusted.
	VerdictUncited ClaimVerdict = "uncited"
)

// Claim is one sentence of an answer with the chunks it cites and its verdict. A
// cited claim starts with an empty Verdict (the Verifier fills it); an uncited
// claim is VerdictUncited from segmentation.
type Claim struct {
	Text        string
	CitedChunks []ChunkID
	Verdict     ClaimVerdict
	Rationale   string
}

// sentenceBoundaryRE finds candidate sentence ends: terminal punctuation followed
// by whitespace (group 1 = the punctuation), or one-or-more line breaks (group 3).
// Whether a candidate is a real boundary is decided by splitSentences.
var sentenceBoundaryRE = regexp.MustCompile(`([.!?]+)(\s+)|(\n+)`)

// acronymRE matches a dotted acronym or a lone initial in the token preceding a
// period — "U.S", "U.K", "e.g", "i.e", "a.m", "A.B.C", or a single "A" — none of
// which end a sentence.
var acronymRE = regexp.MustCompile(`^[A-Za-z](?:\.[A-Za-z])*$`)

// sentenceAbbrev are common abbreviations (sans trailing dot, lower-cased) that a
// period follows without ending a sentence.
var sentenceAbbrev = map[string]bool{
	"etc": true, "vs": true, "cf": true, "al": true, "approx": true,
	"no": true, "fig": true, "eq": true, "inc": true, "ltd": true,
	"co": true, "corp": true, "dept": true, "mr": true, "mrs": true,
	"ms": true, "dr": true, "prof": true, "sr": true, "jr": true, "st": true,
}

// segmentSentences breaks text into sentences at terminal punctuation followed by
// whitespace (or at line breaks), but does not split after a common abbreviation
// ("e.g.", "U.S.", "etc."), a dotted acronym, or a single-letter initial — the
// over-split a bare [.!?] regex causes on real prose. It is a heuristic biased
// toward keeping a claim whole (under-splitting) rather than fragmenting it, since
// a fragment orphans its citation. The terminal punctuation/line break is dropped
// from the returned segments.
func segmentSentences(text string) []string {
	var out []string
	start := 0
	for _, loc := range sentenceBoundaryRE.FindAllStringSubmatchIndex(text, -1) {
		punctStart := loc[2] // group 1 ([.!?]+) start, or -1 for a line-break boundary
		contentEnd := loc[0]
		if punctStart >= 0 {
			contentEnd = punctStart
			if isAbbreviation(text[start:punctStart]) {
				continue // not a real sentence end; fold into the current sentence
			}
		}
		out = append(out, text[start:contentEnd])
		start = loc[1] // resume past the full boundary (punctuation+space or newline)
	}
	if start < len(text) {
		out = append(out, text[start:])
	}
	return out
}

// isAbbreviation reports whether the last whitespace-delimited token of preceding
// (the text up to a candidate period) is an abbreviation, acronym, or initial that
// does not end a sentence.
func isAbbreviation(preceding string) bool {
	fields := strings.Fields(preceding)
	if len(fields) == 0 {
		return false
	}
	last := fields[len(fields)-1]
	if acronymRE.MatchString(last) {
		return true
	}
	return sentenceAbbrev[strings.ToLower(strings.Trim(last, "."))]
}

// claimCitationRE matches a bracketed citation marker like [<chunkID>] or
// [<id1>, <id2>] (mirrors the openai/cli citation regex).
var claimCitationRE = regexp.MustCompile(`\[([^\[\]]+)\]`)

// SegmentClaims splits an answer into claims (sentences), extracts the chunk IDs
// each cites (keeping only those present in citations — hallucinated IDs are
// dropped), and strips the citation markers from the claim text. A sentence that
// cites no valid chunk is marked VerdictUncited; a cited sentence is left
// unjudged for the Verifier. Empty/whitespace-only answers yield no claims.
func SegmentClaims(answer string, citations []Citation) []Claim {
	valid := make(map[ChunkID]bool, len(citations))
	for _, c := range citations {
		valid[c.ChunkID] = true
	}

	var claims []Claim
	for _, sentence := range segmentSentences(answer) {
		cited := citedChunks(sentence, valid)
		text := strings.TrimSpace(claimCitationRE.ReplaceAllString(sentence, ""))
		text = strings.Join(strings.Fields(text), " ") // collapse whitespace left by stripping markers
		if text == "" {
			continue
		}
		claim := Claim{Text: text, CitedChunks: cited}
		if len(cited) == 0 {
			claim.Verdict = VerdictUncited
		}
		claims = append(claims, claim)
	}
	return claims
}

// citedChunks extracts the valid chunk IDs cited in a sentence, in first-appearance
// order, without duplicates.
func citedChunks(sentence string, valid map[ChunkID]bool) []ChunkID {
	var out []ChunkID
	seen := make(map[ChunkID]bool)
	for _, m := range claimCitationRE.FindAllStringSubmatch(sentence, -1) {
		for _, part := range strings.Split(m[1], ",") {
			id := ChunkID(strings.TrimSpace(part))
			if valid[id] && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

// SupportRate is the fraction of claims that are supported (0..1). No claims is
// vacuously faithful (1.0); unsupported and uncited claims both lower it.
func SupportRate(claims []Claim) float64 {
	if len(claims) == 0 {
		return 1
	}
	supported := 0
	for _, c := range claims {
		if c.Verdict == VerdictSupported {
			supported++
		}
	}
	return float64(supported) / float64(len(claims))
}

// AllSupported reports whether every claim is supported — the condition for a
// faithfulness gate to pass.
func AllSupported(claims []Claim) bool {
	for _, c := range claims {
		if c.Verdict != VerdictSupported {
			return false
		}
	}
	return true
}
