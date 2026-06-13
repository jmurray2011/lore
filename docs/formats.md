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

Extraction is best-effort *text*: PDF layout, columns, and tables are not
preserved, and `.xlsx` formatting and formulas are dropped (cell values only).
See [chunking](retrieval.md#chunking) for how extracted text is split into the
retrieval unit.
