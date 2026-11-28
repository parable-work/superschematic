package ir

// One search index per environment (#740).
//
// An environment whose stack names a search database (@stack({ search }))
// searches the databases of the environment through one index table in
// that database: [EnvironmentSearchIndexTypeName], which
// outputs.sql.search.environmentIndex adds to its DB schema. Each source
// database (outputs.sql.search.environment) keeps its own index as D155
// builds it, and its indexing worker copies each index row into the
// environment index after its commit: the row, where it came from (the
// database, the table, the key and the object it indexes), its copied
// audience, the reach of its database's grants, and a version of the
// source's sequence ([SearchEnvironmentSequence]) that orders a row's
// copies. Postgres alone.

// EnvironmentSearchIndexTypeName is the type of the environment's search
// index (#740), whose table is environment_search_index.
const EnvironmentSearchIndexTypeName = "EnvironmentSearchIndex"

// The fields of the environment index beside the search index's own
// (SearchSourceTableField, SearchTitleField and the rest).
const (
	// EnvironmentSearchDatabaseField is the source database's name in the
	// stack.
	EnvironmentSearchDatabaseField = "database"
	// EnvironmentSearchObjectField is what the row indexes:
	// EnvironmentSearchObjectRow, the record with its conversation and
	// files, for every row today.
	EnvironmentSearchObjectField = "object"
	// EnvironmentSearchReachField is the reach of the source database's
	// grants: an entry "<grant>" on a grant's target, and
	// "<grant>:<Type>:<path>" on each row a path of its role reaches.
	EnvironmentSearchReachField = "reach"
	// EnvironmentSearchVersionField orders a row's copies: a write takes
	// effect only over a lower version.
	EnvironmentSearchVersionField = "version"
)

// EnvironmentSearchObjectRow is the object of a row that indexes a record,
// its conversation and its files, as a database's index row does.
const EnvironmentSearchObjectRow = "row"

// SearchEnvironmentSequence is the sequence of a source database whose
// nextval versions each copy of one of its index rows (#740).
const SearchEnvironmentSequence = "search_environment_version"

// SearchEnvironmentPiece is the id of the raw SQL that makes
// SearchEnvironmentSequence in a source database.
const SearchEnvironmentPiece = "search_environment"

// SearchesEnvironment reports whether the schema's @searchable tables are
// searched through their environment's index
// (outputs.sql.search.environment, #740): its search index says so.
func (s *Schema) SearchesEnvironment() bool {
	if s == nil {
		return false
	}
	td := s.Types[SearchIndexTypeName]
	return td != nil && td.SearchEnvironment
}

// EnvironmentSearchIndex returns the schema's environment index
// (outputs.sql.search.environmentIndex, #740), or nil.
func (s *Schema) EnvironmentSearchIndex() *TypeDef {
	if s == nil {
		return nil
	}
	if td := s.Types[EnvironmentSearchIndexTypeName]; td != nil && td.SearchIndex {
		return td
	}
	return nil
}
