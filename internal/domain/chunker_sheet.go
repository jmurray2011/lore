package domain

// SheetHeadingPrefix introduces a sheet boundary in extracted tabular text. An
// extractor that can name its tables (the xlsx extractor, from the workbook's
// tab names) emits one such line before each table's rows; SheetChunker splits
// on it and repeats it on every chunk. It lives in domain rather than beside the
// extractor because it is the contract between them, and both sides must agree
// on it — the tabular analogue of markdown's "#".
const SheetHeadingPrefix = "# Sheet: "
