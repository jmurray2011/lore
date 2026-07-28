# Supported document formats

What `lore add` can ingest, and how lore distinguishes files it *can't* read
from files it *skipped*.

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

To skip files *before* they are read, pass `--exclude <glob>` (repeatable). The
glob matches a file's name or path; matched files are reported as a separate
`excluded` count, never silently dropped:

```bash
lore add notes ./docs --exclude '*(1).pdf'            # skip stray "copy" files
lore add notes ./docs --exclude '*.tmp' --exclude '*/drafts/*'
```

`--exclude` filters a path walk, so it has no effect with `--stdin` (one named
document, nothing to filter) and is rejected there as a usage error. A malformed
glob is also a usage error (exit 2). The exclusion is per-invocation and is *not*
remembered: a later `lore sync` (which replays the collection's remembered roots)
will re-ingest a file you excluded on `add` unless you exclude it again or delete
it at the source.

Extraction is best-effort *text*: PDF layout, columns, and tables are not
preserved, and `.xlsx` formatting and formulas are dropped (cell values only).
See [chunking](retrieval.md#chunking) for how extracted text is split into the
retrieval unit.

## Data formats (versioned)

Beyond ingested documents, lore reads and writes two versioned data formats,
stable as of 1.0:

| Format | Used by | Shape |
|---|---|---|
| Portable artifact (`LORECORP`) | `export` / `import` | framed magic + version + collection, documents, chunks, vectors (optionally `age`-encrypted) |
| Eval set | `lore eval -f` | JSONL: optional first line `{"version": 1}`, then one case per line — `{"question", "expected_sources"?, "expected_chunks"?, "expected_answer"?}` |

Both carry a version so a newer file is rejected with a clear error rather than
mis-parsed. See [retrieval.md](retrieval.md#faithfulness-verification--evaluation)
for the eval workflow.
