# lore

A fast, scriptable CLI for **retrieval-augmented generation (RAG)** and grounded
LLM Q&A over *specific* sets of documents — built for pipes, scripts, and CI, not
GUIs.

```console
$ lore init notes
$ lore add notes ./docs
$ lore ask notes "how does auth work?"
```

- **Fast** — single static binary, concurrent ingestion, brute-force vector
  search that handles tens of thousands of chunks in milliseconds.
- **Safe** — domain invariants enforced in code; no destructive op without
  explicit intent; meaningful exit codes.
- **Automation-first** — data to stdout, logs to stderr, `--json` on every
  command.
- **Provider-agnostic** — any OpenAI-compatible endpoint: OpenAI, Azure OpenAI,
  Ollama, vLLM, LM Studio, OpenRouter.

## Install

```console
# from source (Go 1.25+)
go build -o lore ./cmd/lore

# or install the command
go install github.com/jmurray2011/lore/cmd/lore@latest
```

## Quickstart

```console
# 1. create a collection (pinned to your embedding model's space)
lore init notes

# 2. ingest files or directories (idempotent — safe to re-run)
lore add notes ./docs report.pdf spreadsheet.xlsx
cat meeting.md | lore add notes --stdin --name meeting.md   # ingest piped content

# re-ingest changed files later; --prune also drops documents deleted at source
lore sync notes              # replays the sources add remembered (no path needed)
lore sync notes --prune      # also remove docs whose source file is gone
lore sync notes --prune --dry-run   # preview exactly what --prune would remove

# 3. retrieve the most similar chunks
lore query notes "rotation policy for signing keys" -k 5
lore query notes "rotation policy" --source '*.pdf'   # scope to matching documents
echo "rotation policy" | lore query notes -          # read the query from stdin

# 4. ask a grounded question (retrieval + synthesis with citations)
lore ask notes "what is our key rotation policy?"
# when nothing matches, ask warns on stderr and answers from model knowledge;
# --strict turns that into a hard error (exit 1) and skips the model call
lore ask notes "unrelated question" --strict

# inventory & cleanup
lore ls
lore status notes
lore docs notes                                        # list ingested documents
lore cat notes --doc file:///abs/path/to/report.pdf  # print a document's stored chunks
lore cat notes --chunk 3f2a…9c --chunk 7b1e…04        # print specific chunks by ID
lore rm notes --doc file:///abs/path/to/report.pdf   # one document
lore rm notes --chunk 3f2a…9c --chunk 7b1e…04        # specific chunks by ID
lore rm notes                                          # whole collection
```

Query hits and answer citations are tagged with their source as `source#chunk`,
so every result traces back to the document it came from.

`ask` is retrieval + synthesis in one step. To get between them — filter,
re-rank, threshold, or merge hits yourself — pipe `query --json` into
`synthesize`, which reads hits on stdin and answers from exactly those:

```bash
lore query kb "tenant isolation" --json \
  | jq 'map(select(.score > 0.3))' \
  | lore synthesize "how is tenant isolation enforced?"
```

Add `--json` to any command for machine-readable output. Human output is
colorized on an interactive terminal and plain everywhere else; force it off with
`--no-color` or `NO_COLOR=1`.

## Inspecting chunks / auditing citations

Citations point at chunks; these let you read the chunk behind a claim without
dumping the whole document.

```bash
# print specific chunks by ID (repeatable) — e.g. the ones an answer cited
lore cat notes --chunk 3f2a…9c --chunk 7b1e…04
lore cat notes --chunk 3f2a…9c --json        # same {chunk_id, seq, text} shape as cat --doc

# answer, then append the full text of each cited chunk under a Sources: block,
# numbered with the same [n] the answer used
lore ask notes "what is our key rotation policy?" --expand
lore ask notes "…" --expand --json           # answer object gains an "expansions": [...] array
```

`cat --chunk` and `cat --doc` are mutually exclusive. A malformed chunk ID is a
usage error (exit 2); a well-formed but absent one warns on stderr, still prints
the chunks that were found, and exits 3. `--expand` (and `--explain`, below) are
orthogonal to each other, to `--source`, and to `--strict` (strict still
hard-errors on an ungrounded question before either runs).

To delete a specific chunk rather than read it — e.g. a passage that should no
longer be retrievable — use `rm --chunk` (the write-side counterpart of
`cat --chunk`):

```bash
# remove specific chunks by ID (repeatable); the rest of the document stays
lore rm notes --chunk 3f2a…9c --chunk 7b1e…04
lore rm notes --chunk 3f2a…9c --json          # {removed: "chunks", chunk_ids: [...]}
```

`rm --chunk` and `rm --doc` are mutually exclusive; ID validation and the
missing-ID behavior match `cat --chunk` (malformed → exit 2; some absent →
stderr warning, the found ones still removed, exit 3). It deletes the chunk and
its vector from the index, **not** from the source document — re-ingesting that
source (`add`/`sync`) re-chunks it and brings the text back. For permanent
redaction, also remove or edit the source, or drop the whole document with
`rm --doc`.

## Retrieval diagnostics (`--explain`)

When an answer is bad, `--explain` tells you *whose* fault it is — retrieval or
synthesis — by surfacing the similarity-score distribution: the scores of the
returned chunks plus the score of the best candidate you *didn't* return (the
k+1th runner-up).

```bash
lore query notes "key rotation" -k 5 --explain     # distribution → stderr
lore ask   notes "key rotation policy?" --explain   # distribution → stderr, annotated with which chunks the answer cited
lore query notes "key rotation" -k 5 --explain --json   # stderr: { "explain": {returned, next_score, stats} }
lore ask   notes "…" --explain --json                   # answer object gains an "explain" key
```

How to read it:

- **Every score is low** (and the runner-up is no better) → *retrieval is
  starving*: nothing in the corpus is relevant. Fix retrieval — raise `-k`,
  re-scope `--source`, improve chunking, or reach for `--rerank` (below).
- **A chunk scores high but the answer ignored it** (`ask --explain` marks it
  `uncited`) → *synthesis problem*: the model had the context and didn't use it.
  Fix the prompt or model, not retrieval.
- **The runner-up (`next_score`) is nearly as high as the last returned hit** →
  your `-k` cutoff is arbitrary; you're likely dropping relevant chunks.

Diagnostics always go to **stderr** (as text, or JSON with `--json`) for
`query`, so stdout stays the bare hit array that `synthesize` consumes; for
`ask`, the JSON `explain` rides inside the answer object while the human block
goes to stderr — either way a piped answer or hit list stays clean. `--explain`
adds no model calls; it only fetches one extra candidate (k+1) for the runner-up.

## Supported document formats

| Format | Extensions | Notes |
|---|---|---|
| Plain text | `.txt` | UTF-8, newlines normalized |
| Markdown | `.md`, `.markdown` | embedded as-is |
| Word | `.docx` | text runs, paragraph breaks |
| Excel | `.xlsx` | one line per row, cells tab-joined |
| PDF | `.pdf` | best-effort text (no layout/tables; image-only PDFs yield nothing) |

Unsupported files are reported as a separate `unsupported` count (distinct from
`skipped`, which means already-ingested and unchanged), so a folder of mixed
types never hides files that were silently never ingested. Hidden files and
directories (`.git`, etc.) are never ingested.

## Configuration

Precedence: **flags > environment (`LORE_*`) > config file > defaults.** The
config file is TOML at `<user-config-dir>/lore/config.toml`.

| Env | TOML | Default | Meaning |
|---|---|---|---|
| `LORE_BASE_URL` | `provider.base_url` | `https://api.openai.com/v1` | OpenAI-compatible base URL |
| `LORE_API_KEY` | `provider.api_key` | — | API key |
| `LORE_AUTH` | `provider.auth` | `bearer` | `bearer` (OpenAI) or `api-key` (Azure) |
| `LORE_EMBED_MODEL` | `provider.embed_model` | `text-embedding-3-small` | embedding model / deployment |
| `LORE_DIMENSIONS` | `provider.dimensions` | `1536` | embedding dimensionality (must match the model) |
| `LORE_CHAT_MODEL` | `provider.chat_model` | `gpt-4o-mini` | chat model / deployment |
| `LORE_TIMEOUT` | `provider.timeout` | `120s` | per-request HTTP timeout (Go duration; `0` disables) |
| `LORE_STRUCTURED_OUTPUT` | `provider.structured_output` | `false` | request JSON-schema output (real citations) where supported |
| `LORE_IMAGE_INPUT` | `provider.image_input` | `false` | allow image attachments |
| `LORE_DOCUMENT_INPUT` | `provider.document_input` | `false` | allow document attachments |
| `LORE_STORAGE_BACKEND` | `storage.backend` | `sqlite` | `sqlite` or `memory` |
| `LORE_DB_PATH` | `storage.path` | `<user-config-dir>/lore/lore.db` | SQLite database file |
| `LORE_INGEST_CONCURRENCY` | `ingest.concurrency` | `8` | parallel embeds during ingest (lower for tight rate limits) |
| `LORE_CACHE` | `cache.enabled` | `false` | reuse synthesized `ask`/`synthesize` answers across runs |
| `LORE_CACHE_TTL` | `cache.ttl` | `720h` (30d) | max age of a reusable cached answer (Go duration) |
| `LORE_LOG_LEVEL` | `log.level` | `info` | `debug`/`info`/`warn`/`error` |
| `LORE_LOG_FORMAT` | `log.format` | `text` | `text` or `json` |

Global flags override env, file, and defaults for the current command:

| Flag | Overrides | Meaning |
|---|---|---|
| `--config <path>` | — | read this TOML file instead of the default location |
| `--log-level <level>` | `log.level` | `debug`/`info`/`warn`/`error` |
| `--log-format <fmt>` | `log.format` | `text` or `json` |
| `-v`, `--verbose` | `log.level` | shorthand for `--log-level debug` |
| `--no-cache` | `cache.enabled` | bypass the answer cache for this run (`ask`/`synthesize`) |

The transient `429`/`503` responses of rate-limited providers are retried
automatically (honoring `Retry-After`).

### Answer cache

`ask` and `synthesize` can reuse a previously synthesized answer instead of
calling the model again — useful for repeated questions in scripts and CI. It is
**opt-in** (off by default); enable it once:

```bash
export LORE_CACHE=true          # or [cache] enabled = true in config.toml
lore ask notes "what is our key rotation policy?"   # first run: calls the model, caches
lore ask notes "what is our key rotation policy?"   # repeat: served from the cache, no model call
lore ask notes "…" --no-cache                        # force a fresh answer this run
```

The cache is keyed on the question, the exact text of the chunks that ground the
answer, and the model + prompt identity — so it **self-invalidates**: edit a
source document (changing its chunk text), switch models, or change `-k`/
`--source` enough to retrieve different chunks, and the next ask re-synthesizes.
Entries expire after `LORE_CACHE_TTL` (default 30 days). Requests with `--attach`
are never cached. The cache is stored in the SQLite database, so it persists
across invocations (it does nothing for the `memory` storage backend, which is
per-process).

### Azure OpenAI

Use Azure's v1 (OpenAI-compatible) surface and the `api-key` auth scheme; pass
your **deployment names** as the models:

```bash
export LORE_BASE_URL="https://<resource>.openai.azure.com/openai/v1"   # or *.cognitiveservices.azure.com
export LORE_AUTH=api-key
export LORE_API_KEY="<key>"
export LORE_EMBED_MODEL="<embedding-deployment>"   LORE_DIMENSIONS=3072
export LORE_CHAT_MODEL="<chat-deployment>"
```

## Attachments

Send a file straight to a capable model alongside (or instead of) retrieval —
useful for "compare this against the collection" or asking about an image:

```console
lore ask notes "compare this design to our standards" --attach proposal.pdf
lore ask notes "what's in this diagram?" -k 0 --attach architecture.png   # -k 0 = attachment only
```

Images encode as `image_url`, documents as a file part; each requires the
matching capability (`LORE_IMAGE_INPUT` / `LORE_DOCUMENT_INPUT`) to be enabled.

## Output and exit codes

stdout carries data (human-readable on a TTY, JSON with `--json`); logs and
errors go to stderr.

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | runtime error |
| 2 | usage error |
| 3 | not found |
| 4 | invariant violation (e.g. embedding-space mismatch) |

## Architecture

Hexagonal (ports & adapters), dependencies pointing inward only:

```
cli  ──►  app (use cases)  ──►  domain
              ▲
              │ implements ports defined in app
        adapters (memstore, sqlite, openai, fs, docx, pdf, xlsx)
```

- `internal/domain` — entities, value objects, invariants. Stdlib only.
- `internal/app` — use cases (Ingest, Query, Ask, ...) and the small ports they
  consume.
- `internal/adapters/*` — one package per technology.
- `internal/conformance` — executable contract suites that keep storage/index
  adapters swappable.
- `internal/cli` + `cmd/lore` — the driving adapter and composition root.

Adding a new storage engine or provider means implementing the relevant ports
and passing the conformance suites — no changes to the core.
