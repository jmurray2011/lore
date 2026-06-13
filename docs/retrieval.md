# Retrieval & answers

How lore turns a question into grounded, cited chunks — and the levers for
tuning that: the `query` → `synthesize` seam, citation auditing, diagnostics,
reranking, token budgets, cross-collection search, streaming, and chunking.

## `query` → `synthesize`: the retrieval/synthesis seam

`ask` is retrieval + synthesis in one step. To get *between* them — filter,
re-rank, threshold, or merge hits yourself — pipe `query --json` into
`synthesize`, which reads hits on stdin and answers from exactly those:

```bash
lore query kb "tenant isolation" --json \
  | jq 'map(select(.score > 0.3))' \
  | lore synthesize "how is tenant isolation enforced?"
```

Query hits and answer citations are tagged with their source as `source#chunk`,
so every result traces back to the document it came from. Add `--json` to any
command for machine-readable output; human output is colorized on an interactive
terminal and plain everywhere else (force it off with `--no-color` or
`NO_COLOR=1`).

## Auditing citations

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

## Retrieval diagnostics

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

## Token-budget retrieval

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
- **`fixed`** — fixed-size word windows (an escape hatch).

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
