# Changelog

All notable changes to lore are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/); lore adheres to
[Semantic Versioning](https://semver.org/).

## [1.1.2] — 2026-07-28

### Changed

- Test fixtures for the spreadsheet chunker and xlsx extractor now use neutral
  vocabulary. No functional change; the binary is equivalent to 1.1.1.

Released so that the published binaries carry a commit stamp that resolves in
the public history.

## [1.1.1] — 2026-07-28

### Security

- Bumped two transitive dependencies carrying advisories, both reached through
  `charmbracelet/glamour`:
  - `golang.org/x/text` v0.31.0 → v0.39.0 ([GO-2026-5970](https://pkg.go.dev/vuln/GO-2026-5970))
  - `github.com/yuin/goldmark` v1.7.13 → v1.7.17 ([GO-2026-5320](https://pkg.go.dev/vuln/GO-2026-5320))

  `golang.org/x/sync` moved v0.19.0 → v0.21.0 as a consequence. No new
  dependencies, no source changes, no behavior change.

## [1.1.0] — 2026-07-28 — "Spreadsheets that retrieve"

### Added

- **CSV ingest.** `.csv` files are ingested as text instead of being counted
  `unsupported`. The extension maps to `text/csv` explicitly rather than through
  the OS mime table, which on some machines resolves `.csv` to a spreadsheet type
  and routes it to the wrong extractor. Content is passed through verbatim; csv
  quoting and embedded newlines are not parsed.
- **Sheet names in `.xlsx` extraction.** Each worksheet's rows are led by its
  workbook **tab name**, resolved through `xl/workbook.xml` and its relationships
  and emitted in tab order (previously sheets were unnamed and ordered by zip
  part name). Workbooks written without those parts fall back to the part name.
- **Sheet-aware chunking for `.xlsx` and `.csv`.** Every chunk cut from a table
  repeats that table's **sheet name and header row**, so a row retrieved from the
  middle of a workbook still names its own columns — previously it retrieved as
  bare cells with no recoverable header. Rows are never split across chunks or
  duplicated between them, and the sheet name is carried as the chunk's heading
  path. The first line of each table is treated as its header.

### Changed

- **`StructureChunkerVersion` 1 → 2** (breaking for ingest; see *Migration*).

### Migration

Sheet-aware chunking changes the structure strategy's output, so collections
record `structure/v2` from this release on. The chunker spec is pinned **per
collection, not per format**, so every collection created before 1.1 is
affected — including collections that contain no spreadsheets.

Affected collections stay **fully queryable**: `query`, `ask`, `cat`, `docs`,
and `export` are unchanged. Only `add` and `sync` refuse, with exit **4** and an
`ErrChunkerMismatch` message naming both specs. Rebuild a collection when you
next need to ingest into it:

```bash
lore status notes                          # check the pinned chunker
lore rm notes
lore init notes && lore add notes ./docs   # re-embeds from source
```

There is no in-place re-chunk; rebuilding re-embeds, so budget provider cost and
time accordingly for large collections.

## [1.0.0] — 2026-07-28 — "The auditable knowledge base"

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
- **`lore config path` / `lore config init`.** `path` prints the config file lore
  would read; `init` scaffolds a commented starter file there. Both skip the
  runtime build, so they work before a provider is configured — the first-run
  chicken-and-egg problem.
- **Lexical-only retrieval — `query`/`ask --lexical`.** BM25 with no embedder
  call at all, so a collection is searchable without a provider (and without
  spending tokens). Distinct from `--hybrid`, which fuses BM25 *with* vectors.
- **`import --re-embed`.** Rebuilds an imported collection's vectors in the
  local embedding space, so an artifact built against someone else's embedder
  becomes queryable with yours instead of being read-only. `import` also now
  warns up front when the local embedder cannot query the collection it just
  reconstructed.

### Changed

- **`lore eval` evaluates the retrieval you actually run.** It now accepts the
  `query`/`ask` retrieval flags (`--hybrid`, `--rerank`, `--recency`, `--mmr`,
  `--where`, `--source`, `--max-per-source`) and resolves each question through the
  same engine, so eval metrics reflect your configured pipeline instead of a fixed
  cosine baseline.
- **MCP `ask`/`query` tools reach retrieval parity with the CLI** — added `hybrid`,
  `mmr`, `recency`, and `where` arguments (previously rerank/source only). The MCP
  server and CLI now share one retrieval engine, so they can't drift.
- **`synthesize` reaches parity with `ask` on the synthesis side** — `--verify`/
  `--verify-strict`, `--stream`/`--no-stream`, and `--expand`. `synthesize --verify`
  checks claims against the **piped chunks** (no collection lookup), so faithfulness
  gating works for externally-retrieved or hand-assembled context, not just `ask`.
- Retrieval composition order is defined and documented: fuse/rerank/recency/MMR →
  `--max-per-source` → `--budget` trim.
- `query`/`ask` hit `--json` gains an optional `metadata` field; `docs --json`
  gains `metadata`. Both are `omitempty`, so output without metadata is unchanged.
- **A missing `--config` file is now a hard error**, not a silent fall-through to
  defaults. Explicitly naming a config file that isn't there almost always means
  a typo or a bad path, and silently ignoring it produced confusing downstream
  failures. Unrecognized keys in a config file now warn (naming the key) instead
  of being dropped, so a misspelled setting is visible rather than inert.
- **Provider auth failures are actionable.** A 401/403 from the embed, chat, or
  rerank endpoint is rewritten into guidance naming the env var to set, the
  config file in play, and the relevant docs page, instead of surfacing the raw
  provider error. Embedding-space mismatches now name the remedy, and
  `query`/`ask` errors gained shell-quoting hints, rerank-key guidance, and
  next steps on an unknown collection.
- **Root and `init` help teach the workflow** (`init` → `add` → `ask`) rather
  than only listing flags.

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
