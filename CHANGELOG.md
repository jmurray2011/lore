# Changelog

All notable changes to lore are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/); lore adheres to
[Semantic Versioning](https://semver.org/).

## [1.0.0] — 2026-06-13 — "The auditable knowledge base"

**1.0 is a stability commitment.** The CLI surface, the `--json` contracts, the
exit-code semantics, and the on-disk/portable formats are now versioned and
stable. Existing collections built with pre-1.0 lore keep working — schema
changes migrate in place and degrade gracefully (see *Migration*).

### Added

- **Faithfulness verification — `ask --verify` / `--verify-strict`.** A second
  pass checks that each sentence (claim) of an answer is entailed by the chunk(s)
  it cites. `--json` gains a `verification` array (`{claim, cited_chunks, verdict,
  rationale}`) and an aggregate `support_rate`; `--verify-strict` exits non-zero
  (code **5**) when any claim is unsupported — a CI faithfulness gate. The
  entailment judgment reuses the configured chat model (no new dependency).
- **Evaluation harness — `lore eval <collection> -f questions.jsonl`.** Runs a
  versioned JSONL eval set and reports retrieval quality (recall@k, precision@k,
  MRR, nDCG, hit-rate) and, with `--verify`, answer faithfulness (support rate),
  per-question and aggregate, human and `--json`. `--fail-under <metric>=<value>`
  (repeatable) exits **5** when an aggregate is below threshold — a CI quality gate.
- **Hybrid retrieval — `query`/`ask --hybrid`.** Fuses BM25 keyword search with
  vector search via Reciprocal Rank Fusion (no score-normalization headaches),
  recovering exact-keyword, rare-token, and identifier matches pure cosine misses.
  Backed by SQLite FTS5 (no new dependency) and an in-memory BM25 for the memory
  backend. Default configurable via `[retrieval] hybrid`.
- **Metadata + `--where` filtering.** Documents carry structured metadata from
  `add --meta key=value` and markdown front-matter; `query`/`ask`/`docs --where`
  filter by a small predicate grammar (`= != < <= > >= ~`, AND-combined, with
  numeric/date/glob/tag-list semantics). Metadata is exposed in `--json`.
- **Diversity — `--max-per-source N` and `--mmr` (`--mmr-lambda`).** Stop a
  dominant document from sweeping `-k`; Maximal Marginal Relevance trades
  relevance against diversity.
- **Recency — `query`/`ask --recency` (`--half-life-days`, default 90).** Vector
  similarity has no notion of time, so a stale chunk can outrank a newer
  correction. Recency re-ranks a wider candidate pool by relevance blended with
  an exponential time decay, surfacing fresh-but-slightly-less-similar chunks that
  pure cosine buries. A document's date is **inferred from the file**, not assumed
  from one format: last-modified front matter (`updated`/`modified`/…,
  case-insensitive) → the file's filesystem `mtime` (captured at ingest, also
  `--where`-queryable) → a date in the filename/path (`2026-06-09`, `2026-W20`) →
  `created`/`date` front matter → ingest time. Undated documents keep full weight
  (never demoted on a guess). Composes with `--hybrid`/`--where`/`--source`/
  `--budget` and multiple collections; mutually exclusive with `--rerank`/`--mmr`.
- **Exit code 5** — a quality gate was not met (`ask --verify-strict`,
  `lore eval --fail-under`), distinct from runtime (1) and usage (2) errors.

### Changed

- **`synthesize` reaches parity with `ask` on the synthesis side** — `--verify`/
  `--verify-strict`, `--stream`/`--no-stream`, and `--expand`. `synthesize --verify`
  checks claims against the **piped chunks** (no collection lookup), so faithfulness
  gating works for externally-retrieved or hand-assembled context, not just `ask`.
- Retrieval composition order is defined and documented: fuse/rerank/recency/MMR →
  `--max-per-source` → `--budget` trim.
- `query`/`ask` hit `--json` gains an optional `metadata` field; `docs --json`
  gains `metadata`. Both are `omitempty`, so output without metadata is unchanged.

### Deferred

- **Code-aware (tree-sitter) chunking** is deferred past 1.0 to keep
  `CGO_ENABLED=0` the default build; the resolved approach (a pure-Go heuristic
  code chunker) is recorded for a future release.
- **Verdict caching** for `--verify` is deferred; the `Verifier` is behind a port,
  so a cache decorator over the existing answer cache is a clean follow-up.
- Cross-collection (`-c`) `--hybrid`/`--mmr` are usage errors for now (single
  collection only).

### Migration (from pre-1.0 collections)

- **No rebuild required to keep querying.** SQLite collections migrate in place:
  new `metadata` columns and the FTS5 table are added on first open.
- **Metadata** is captured for documents ingested (new or changed) after the
  upgrade; re-add with `--meta` (or edit content) to annotate existing documents.
- **Hybrid (`--hybrid`)** uses the lexical index, which is populated as you ingest;
  a pre-1.0 collection's existing documents degrade to vector-only under
  `--hybrid` until rebuilt (re-`add`). New ingestion is indexed for hybrid
  automatically.
- **Portable artifacts** (`export`/`import`) stay compatible: the new metadata
  field is additive and the format version is unchanged; the lexical index is
  rebuilt from chunks on import.

### Stable contracts (as of 1.0.0)

- CLI command and flag surface; `--json` shapes; exit codes (0 ok · 1 runtime ·
  2 usage · 3 not-found · 4 invariant · 5 quality-gate-unmet).
- The portable artifact format (`LORECORP`) and the eval-set JSONL format
  (`{"version": 1}`), both versioned for forward compatibility.
