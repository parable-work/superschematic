package sqlutil

// SearchFTSTable is the FTS5 table of the search index's words on SQLite
// (D186): sqlgen writes it and the triggers that keep it, and the
// TypeScript runtime's search matches and ranks it.
const SearchFTSTable = "search_index_fts"
