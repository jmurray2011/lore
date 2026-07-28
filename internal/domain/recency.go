package domain

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MetaKeyModTime is the reserved metadata key under which ingestion records a
// source file's filesystem modification time (RFC3339). It is a recency signal
// (see HitTime), distinct from author-supplied front-matter dates.
const MetaKeyModTime = "mtime"

// modifiedDateKeys are front-matter fields meaning "last modified", matched
// case-insensitively and ranked ahead of the filesystem mtime — an explicit
// author signal of freshness.
var modifiedDateKeys = []string{"updated", "updated_at", "modified", "last_modified", "lastmod"}

// createdDateKeys are front-matter fields meaning "created"/"published" — a weaker
// freshness proxy, used only after modify-time and filename signals so an
// actively-edited document with only a stale "created:" is not misranked as old.
var createdDateKeys = []string{"created", "created_at", "date", "published", "publishdate", "publish_date"}

// HitTime infers the most meaningful timestamp for ranking a hit by recency,
// trying signals strongest-to-weakest and reporting whether one was found:
//
//  1. an explicit "last modified" front-matter date (updated/modified/lastmod/…),
//  2. the source file's recorded filesystem mtime (MetaKeyModTime),
//  3. a date embedded in the source filename/path (2026-06-09, 2026-W20, …),
//  4. a "created"/"date" front-matter date,
//  5. the document's ingest time.
//
// It assumes no single format: front-matter keys match case-insensitively, dates
// parse in several layouts (see parseDate), and filename dates cover ISO calendar
// dates and ISO weeks. A hit with none of these is unknown (ok=false) and keeps
// full weight in DecayByRecency rather than being treated as ancient.
func HitTime(h ChunkHit) (time.Time, bool) {
	if t, ok := metadataDate(h.Metadata, modifiedDateKeys); ok {
		return t, true
	}
	if t, ok := parseDate(h.Metadata[MetaKeyModTime]); ok {
		return t, true
	}
	if t, ok := dateFromPath(h.Source); ok {
		return t, true
	}
	if t, ok := metadataDate(h.Metadata, createdDateKeys); ok {
		return t, true
	}
	if !h.IngestedAt.IsZero() {
		return h.IngestedAt, true
	}
	return time.Time{}, false
}

// metadataDate returns the first parseable date among keys (matched
// case-insensitively against m), in key order.
func metadataDate(m Metadata, keys []string) (time.Time, bool) {
	for _, key := range keys {
		for k, v := range m {
			if v != "" && strings.EqualFold(k, key) {
				if t, ok := parseDate(v); ok {
					return t, true
				}
			}
		}
	}
	return time.Time{}, false
}

// pathDateRE matches an ISO calendar date (YYYY-MM-DD, separators -, _, or /)
// anywhere in a path; pathWeekRE matches an ISO week (YYYY-Www).
var (
	pathDateRE = regexp.MustCompile(`(\d{4})[-_/](\d{2})[-_/](\d{2})`)
	pathWeekRE = regexp.MustCompile(`(\d{4})-[Ww](\d{2})`)
)

// dateFromPath infers a date from a source URI's filename or path — an ISO
// calendar date or ISO week — taking the rightmost (most specific) valid match.
// Being intrinsic to the name, it survives file copies that reset mtime.
func dateFromPath(uri string) (time.Time, bool) {
	if m := lastMatch(pathDateRE, uri); m != nil {
		if t, err := time.Parse("2006-01-02", m[1]+"-"+m[2]+"-"+m[3]); err == nil {
			return t, true
		}
	}
	if m := lastMatch(pathWeekRE, uri); m != nil {
		year, _ := strconv.Atoi(m[1])
		week, _ := strconv.Atoi(m[2])
		if week >= 1 && week <= 53 {
			return isoWeekStart(year, week), true
		}
	}
	return time.Time{}, false
}

// lastMatch returns the rightmost submatch of re in s, or nil.
func lastMatch(re *regexp.Regexp, s string) []string {
	ms := re.FindAllStringSubmatch(s, -1)
	if len(ms) == 0 {
		return nil
	}
	return ms[len(ms)-1]
}

// isoWeekStart returns the Monday (UTC midnight) that begins ISO week `week` of
// `year`. Jan 4 is always in ISO week 1, so its Monday anchors the year.
func isoWeekStart(year, week int) time.Time {
	jan4 := time.Date(year, 1, 4, 0, 0, 0, 0, time.UTC)
	offset := (int(jan4.Weekday()) + 6) % 7 // days since Monday (Sun=0 → 6)
	week1Monday := jan4.AddDate(0, 0, -offset)
	return week1Monday.AddDate(0, 0, (week-1)*7)
}

// DecayByRecency reorders hits by relevance blended with an exponential time
// decay, then returns the top k (all of them when k <= 0). Each hit's cosine
// Score is multiplied by 2^(-age/halfLife), where age is now minus the hit's
// timestamp (HitTime); the hits are stable-sorted by this adjusted score. A hit
// with no known timestamp, or one dated in the future, keeps its full score
// (decay 1.0) — only known-older hits are demoted, so retrieval never buries an
// undated chunk on a guess. The cosine Score is preserved for display; only the
// order changes (mirroring rerank/MMR). halfLife <= 0 is a no-op reorder.
func DecayByRecency(hits []ChunkHit, halfLife time.Duration, now time.Time, k int) []ChunkHit {
	if halfLife > 0 && len(hits) > 1 {
		adjusted := make([]float64, len(hits))
		for i, h := range hits {
			factor := 1.0
			if ts, ok := HitTime(h); ok {
				if age := now.Sub(ts); age > 0 {
					factor = math.Exp2(-float64(age) / float64(halfLife))
				}
			}
			adjusted[i] = h.Score * factor
		}
		order := make([]int, len(hits))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool { return adjusted[order[a]] > adjusted[order[b]] })
		sorted := make([]ChunkHit, len(hits))
		for i, j := range order {
			sorted[i] = hits[j]
		}
		hits = sorted
	}
	if k > 0 && k < len(hits) {
		hits = hits[:k]
	}
	return hits
}
