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

# re-ingest changed files later; --prune also drops documents deleted at source
lore sync notes              # replays the sources add remembered (no path needed)
lore sync notes --prune      # also remove docs whose source file is gone
lore sync notes --prune --dry-run   # preview exactly what --prune would remove

# 3. retrieve the most similar chunks
lore query notes "rotation policy for signing keys" -k 5
lore query notes "rotation policy" --source '*.pdf'   # scope to matching documents

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
lore rm notes --doc file:///abs/path/to/report.pdf   # one document
lore rm notes                                          # whole collection
```

Query hits and answer citations are tagged with their source as `source#chunk`,
so every result traces back to the document it came from.

Add `--json` to any command for machine-readable output. Human output is
colorized on an interactive terminal and plain everywhere else; force it off with
`--no-color` or `NO_COLOR=1`.

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
| `LORE_LOG_LEVEL` | `log.level` | `info` | `debug`/`info`/`warn`/`error` |
| `LORE_LOG_FORMAT` | `log.format` | `text` | `text` or `json` |

Global flags override env, file, and defaults for the current command:

| Flag | Overrides | Meaning |
|---|---|---|
| `--config <path>` | — | read this TOML file instead of the default location |
| `--log-level <level>` | `log.level` | `debug`/`info`/`warn`/`error` |
| `--log-format <fmt>` | `log.format` | `text` or `json` |
| `-v`, `--verbose` | `log.level` | shorthand for `--log-level debug` |

The transient `429`/`503` responses of rate-limited providers are retried
automatically (honoring `Retry-After`).

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
