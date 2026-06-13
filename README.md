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

## Two-stage retrieval (reranking)

Vector search is a *bi-encoder*: the query and each chunk are embedded
separately, so the similarity score misses relevance that only shows up when the
two are read *together*. A **cross-encoder reranker** scores each `(query,
chunk)` pair jointly and is the single biggest precision lever in a RAG stack.
The standard pattern is to cast a wide cheap net with vector search, then rerank
that pool down to a precise few:

```bash
# composable: vector top-50 → rerank → top-5 → synthesize
lore query kb "tenant isolation" -k 50 --json \
  | lore rerank "tenant isolation" -n 5 \
  | lore synthesize "how is tenant isolation enforced?"

# or in one step — retrieve a wide pool, rerank, return the final -k
lore query kb "tenant isolation" -k 5 --rerank                 # pool of 50 by default
lore ask   kb "how is tenant isolation enforced?" -k 5 --rerank --rerank-candidates 80
```

- `lore rerank "<query>"` reads the `query --json` hit array on stdin, reorders
  it by cross-encoder relevance, and re-emits it with an added `rerank_score`
  (the original `score` is preserved). `-n/--top-n` truncates; without it, all
  hits are re-emitted reordered.
- `query --rerank` / `ask --rerank` do the two stages in one command: `-k` is the
  **final** count, `--rerank-candidates` (default 50, must be ≥ `-k`) is the
  pre-rerank vector pool. `--source` still scopes the candidate retrieval.

**The reranker is a separate provider.** Rerank APIs are *not* OpenAI-shaped
(OpenAI has no rerank endpoint); the de-facto standard is the Cohere-style
`POST /rerank`, which Jina, Voyage, and others mimic. So reranking has its own
`rerank.*` / `LORE_RERANK_*` config (base URL, key, model) — a common setup is
"embed with OpenAI, rerank with Cohere/Jina". Requesting `--rerank`/`rerank`
without it configured is a usage error (exit 2); a rerank request that fails
after retries exits 1 and emits nothing — lore never silently falls back to
vector order. Pair it with `--explain`, which then shows both `sim=` and
`rerank=` scores and a rerank-ordered runner-up.

## Token-budget retrieval (`--budget`)

`-k` is a crude proxy for "how much context": chunks vary in size, so a fixed
count over- or under-fills the model's window. `--budget N` on `query` and `ask`
returns the top-ranked chunks whose **cumulative token count** reaches `N`
instead:

```bash
lore ask kb "summarize the controls" --budget 2000        # fill ~2000 tokens of context
lore query kb "controls" -k 20 --budget 1500 --json       # top-20, capped to 1500 tokens
```

- Tokens are counted with the same tokenizer used for chunk sizing.
- **Composes with `-k`:** `--budget` is applied *after* ranking and only ever
  *tightens* the bound — it takes top chunks until the budget fills, never
  exceeding `-k` chunks (default 8). Raise `-k` to let the budget consider more.
- **Composes with `--rerank`:** the budget applies to the **final** set — the
  candidate pool is reranked first, then trimmed to the token budget.
- `ask --json` reports the grounding set's token count as `grounding_tokens`;
  `query` reports it on stderr (its stdout stays the bare hit array).

## Streaming answers

On an interactive terminal `lore ask` streams the answer's tokens as they
arrive, so a slow (cache-missing) synthesis feels live instead of dead. Piped or
with `--json` it buffers and emits the whole answer at once — the same
TTY-vs-pipe split the color rendering uses, so scripts and the JSON contract are
unaffected.

```bash
lore ask kb "how do we deploy?"               # streams on a TTY
lore ask kb "how do we deploy?" --no-stream   # buffered + rich Markdown instead
lore ask kb "how do we deploy?" --stream | …  # force streaming even when piped
```

Because tokens print as they arrive, a streamed answer is raw text: it keeps the
model's own inline `[n]` markers, followed by a `Sources` list keyed to those
numbers — rather than the glamour-rendered, re-numbered form the buffered path
produces (you can't restyle text already printed). `--no-stream` restores the
buffered, rich-rendered output. Cached answers and `--json` are never streamed (a
cache hit is already instant), and with provider structured-output enabled the
answer is delivered whole (its JSON can't stream as prose).

## Cross-collection retrieval

Collections are independent corpora, but vectors from collections that share an
embedding space (same model + dimensions) are directly comparable. Two commands
exploit that.

**Search several collections at once** with repeatable `-c`/`--collection` on
`query` and `ask`. Their hits merge into one ranked top-k (or grounding set),
and each hit/citation is tagged with the collection it came from:

```bash
lore ask -c notes -c code -c slack "how do we deploy?"     # answer grounded across all three
lore query -c notes -c code "retry backoff" -k 10 --json   # merged hits, each with a "collection" field
```

All targeted collections must share an embedding space — the merge compares
their vectors directly, so a mismatch is an invariant violation (**exit 4**,
naming the offenders), checked before any retrieval runs. A single collection
(one `-c`, or the bare positional `<collection>`) behaves exactly as before.
Composes with `--rerank` (the *merged* pool is reranked), `--budget` (the merged
set is token-filled), and `--explain` (the distribution covers the merged set).

**Semantic diff between two collections** with `query <target>
--from-collection <source>`. Instead of embedding query text, this feeds the
source collection's *own stored vectors* back as queries (no re-embedding, so no
risk of a drifted model) and finds each source chunk's nearest neighbors in the
target. Results are grouped by source chunk:

```bash
# For each chunk in v2, find its closest match in v1, then surface the chunks
# in v2 whose best v1 match is weak — i.e. new or substantially changed content:
lore query v2 --from-collection v1 --json \
  | jq 'map(select(.hits[0].score < 0.7))'
```

Source and target must share an embedding space (exit 4 otherwise). `-k` bounds
the hits per source chunk; `--source` scopes the *target* side; `--json` emits
`[{ "from": {chunk_id, source, seq}, "hits": [ <hit>, ... ] }, ...]` with the
standard hit object inside `hits`.

## Supported document formats

| Format | Extensions | Notes |
|---|---|---|
| Plain text | `.txt` | UTF-8, newlines normalized |
| Markdown | `.md`, `.markdown` | heading-aware chunking, code-fence-safe |
| Word | `.docx` | text runs, paragraph breaks |
| Excel | `.xlsx` | one line per row, cells tab-joined |
| PDF | `.pdf` | best-effort text (no layout/tables; image-only PDFs yield nothing) |

Unsupported files are reported as a separate `unsupported` count (distinct from
`skipped`, which means already-ingested and unchanged), so a folder of mixed
types never hides files that were silently never ingested. Hidden files and
directories (`.git`, etc.) are never ingested.

## Chunking

Retrieval quality is bounded by *chunk* quality more than by index
sophistication. lore chunks documents with a pluggable, per-format strategy
selected by `chunk.strategy`:

- **`structure`** (default) — token-sized and boundary-aware:
  - **Markdown** splits on the heading hierarchy: each chunk is a section,
    carrying its full heading path (`Auth > Keys > Rotation`) as metadata. It
    **never splits inside a fenced code block**; oversized sections split at
    paragraph (then sentence, then word) boundaries; tiny adjacent sections merge
    up to the target size.
  - **Plain text / docx / pdf / xlsx** pack paragraphs up to the target size,
    never breaking mid-sentence where avoidable.
- **`fixed`** — the legacy fixed-size word windows (an escape hatch).

Sizes are measured in **tokens** (`chunk.size`, `chunk.overlap`) for the
structure strategy and in whitespace words for `fixed`, via a built-in,
offline tiktoken counter (`o200k_base`). The heading path is shown by `cat`
and in `--json`.

**Contextual embedding (`chunk.context_prefix`, default on).** For markdown,
the chunk's heading path is prepended to the text that gets **embedded**, so the
vector captures the chunk's place in the document. The **stored** text is always
the original — citations and `cat` show real content, never the prefixed form.

**Chunker pinning.** A collection records the chunker it was created with (shown
by `lore status`). Re-ingesting (`add`/`sync`) with a *different* chunker —
strategy, size, overlap, tokenizer, or the context-prefix setting — would leave
the collection holding two incompatible chunk layouts (unchanged documents skip
re-chunking), so lore **refuses it with exit 4** rather than silently mixing.
Changing the chunker means rebuilding: `lore init` a fresh collection and re-add.
Collections created before pinning existed are read-only in the same way —
queryable, but they must be rebuilt to ingest again.

Code-aware chunking (one chunk per function/class, via tree-sitter) is a planned
future strategy that slots into the same registry; source files currently use
the text chunker.

## Portable corpora

A built collection is a self-contained, queryable knowledge artifact — so build
the index once and *ship* it. `lore export` serializes a whole collection into a
single, versioned file; `lore import` reconstructs it anywhere, pins intact:

```bash
lore export kb -o kb.lore                 # one file: chunks, vectors, pins, metadata
lore import kb.lore                        # reconstruct locally (same name)
lore import kb.lore --name kb-staging      # …or under a new name
lore import kb.lore --force                # overwrite an existing collection
```

The artifact carries the embedding-space pin (model + dimensions) and the
chunker pin, so the reconstructed collection enforces the same invariants on
later `add`/`query` — the round-trip is lossless (a query against the imported
collection returns the same results as the original). The format is versioned;
an artifact newer than your `lore` understands is refused with a clear error
rather than mis-parsed.

This is what you commit to a repo, attach to a release, or hand a teammate ("the
docs, pre-indexed — ask it anything"), and it is the natural unit to point an
agent or MCP server at: a portable corpus, no re-indexing required.

## Encrypted corpora

`lore export --encrypt` wraps the artifact in an [age](https://age-encryption.org)
envelope. **Read the threat model before relying on it:**

- Encryption protects the **exported artifact at rest and in transit** —
  committing it, handing it over, backing it up to the cloud.
- It does **not** protect the working database during use: `import` decrypts to
  a normal plaintext working DB that lore queries; plaintext exists on disk while
  the collection is in use.
- **A lost key or passphrase means the corpus is permanently unrecoverable.**
  There is no recovery path. Back up the key separately from the artifact.
- The **entire artifact is encrypted, including the embedding vectors** — vectors
  are invertible to approximate plaintext, so lore never encrypts text while
  leaving vectors in the clear. It is whole-artifact or nothing.

The automation-safe, recommended way to supply the passphrase is
`--passphrase-cmd` (or `LORE_EXPORT_KEY_CMD`): lore runs the command and reads the
passphrase from its stdout, so the secret never lands in a flag value, in argv,
or on disk.

```bash
# Encrypt with a passphrase from your secret manager (never in shell history):
lore export kb -o kb.lore.age --encrypt --passphrase-cmd 'op read op://vault/lore/passphrase'
lore import kb.lore.age --passphrase-cmd 'op read op://vault/lore/passphrase'

# Or encrypt to a teammate's age public key; they import with their identity:
lore export kb -o kb.lore.age --encrypt --recipient age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p
lore import kb.lore.age --identity ~/.age/key.txt
```

On an interactive terminal with no command source, lore prompts for the
passphrase (twice on export, once on import; never echoed). With no key source
**and** no terminal — a script, a pipe — encryption/decryption fails with a usage
error rather than hanging. `--recipient`/`--identity` and the passphrase path are
mutually exclusive. Encryption is detected from the artifact's content, not its
file name, so a `.lore` that is encrypted (or a `.lore.age` that isn't) just
works; a wrong key or a tampered artifact fails to decrypt rather than producing
garbage.

The artifacts are ordinary `age` files: anyone with the key can decrypt them with
the standalone [`age`](https://age-encryption.org) CLI — no lore lock-in.

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
| `LORE_RERANK_BASE_URL` | `rerank.base_url` | — | rerank endpoint base URL (Cohere-style) |
| `LORE_RERANK_API_KEY` | `rerank.api_key` | — | rerank API key |
| `LORE_RERANK_AUTH` | `rerank.auth` | `bearer` | rerank auth scheme: `bearer` or `api-key` |
| `LORE_RERANK_MODEL` | `rerank.model` | — | reranker model name |
| `LORE_RERANK_TIMEOUT` | `rerank.timeout` | `120s` | rerank per-request timeout (Go duration; `0` disables) |
| `LORE_STORAGE_BACKEND` | `storage.backend` | `sqlite` | `sqlite` or `memory` |
| `LORE_DB_PATH` | `storage.path` | `<user-config-dir>/lore/lore.db` | SQLite database file |
| `LORE_INGEST_CONCURRENCY` | `ingest.concurrency` | `8` | parallel embeds during ingest (lower for tight rate limits) |
| `LORE_CHUNK_STRATEGY` | `chunk.strategy` | `structure` | `structure` (heading/paragraph-aware) or `fixed` (legacy word windows) |
| `LORE_CHUNK_SIZE` | `chunk.size` | `512` | target chunk size (tokens for `structure`, words for `fixed`) |
| `LORE_CHUNK_OVERLAP` | `chunk.overlap` | `64` | overlap between size-driven splits, same unit as size |
| `LORE_CHUNK_CONTEXT_PREFIX` | `chunk.context_prefix` | `true` | prepend a chunk's heading path to its embedded text (markdown) |
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
