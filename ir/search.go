package ir

import "fmt"

// Search across tables (D155).
//
// A DB table marked @searchable names the field that titles each of its
// rows and the fields that hold its body. The loader adds one index table
// to the database ([SearchIndexTypeName]): a row per record of every
// @searchable table, holding the record's table and key, its title, its
// body, the text of its conversation (Comments) and of its files'
// extracted text (@attachments), an optional embedding, and the columns
// the audience check reads (the row's effective audience and agent
// access, D156). A trigger on each @searchable table copies the access
// columns into the index row in the writer's transaction, and deletes the
// index row with the record (a delete or a soft delete); every write that
// changes the indexed text enqueues a message on the declaration's queue,
// whose worker rebuilds the record's index row. The runtime ranks every
// table's rows in one query, with the caller's access checked inside it.

// SearchableDef is the @searchable declaration of a DB table (D155).
//
// In TypeScript: @searchable<T>({ title, body?, scope?, queue, comments?, attachments? })
type SearchableDef struct {
	// Title is the text field that titles each row: a search ranks a match
	// in it highest, and a hit shows it.
	Title string `json:"title" yaml:"title"`

	// Body are the text fields that hold the row's body, in order. Empty
	// indexes the title alone.
	Body []string `json:"body,omitempty" yaml:"body,omitempty"`

	// Scope is the field whose value a search's scope keeps to: a text or
	// a key (a relation's key among them) the index row copies as text, as
	// a workspace or a project. Empty puts every row in no scope, which a
	// search with a scope leaves out.
	Scope string `json:"scope,omitempty" yaml:"scope,omitempty"`

	// Queue is the @queue class of the schema a write that changes the
	// indexed text enqueues a message on, naming the row, whose fields the
	// loader fills ([SearchIndexingFields]). Its worker calls the
	// runtime's indexSearch, which rebuilds the row's index row.
	Queue string `json:"queue" yaml:"queue"`

	// Comments false leaves the text of the row's conversation (Comments
	// on the table) out of its index row; nil or true indexes it when the
	// table implements Comments.
	Comments *bool `json:"comments,omitempty" yaml:"comments,omitempty"`

	// Attachments false leaves its files' extracted text (@attachments)
	// out; nil or true indexes it when the table declares @attachments.
	Attachments *bool `json:"attachments,omitempty" yaml:"attachments,omitempty"`
}

// IndexesComments reports whether the declaration indexes the row's
// conversation, when its table implements Comments.
func (s *SearchableDef) IndexesComments() bool { return s != nil && (s.Comments == nil || *s.Comments) }

// IndexesAttachments reports whether the declaration indexes its files'
// extracted text, when its table declares @attachments.
func (s *SearchableDef) IndexesAttachments() bool {
	return s != nil && (s.Attachments == nil || *s.Attachments)
}

// Fields are the fields the declaration indexes: the title, then the body
// fields, in order.
func (s *SearchableDef) Fields() []string {
	if s == nil {
		return nil
	}
	return append([]string{s.Title}, s.Body...)
}

// OriginSearch marks a type or a field the loader added for @searchable
// (D155).
const OriginSearch = "search"

// SearchIndexTypeName is the type of the index table the loader adds to a
// database with a @searchable table: SearchIndex, whose table is
// search_index.
const SearchIndexTypeName = "SearchIndex"

// The fields of the index table (D155).
const (
	SearchIDField             = "id"
	SearchSourceTableField    = "sourceTable"
	SearchSourceKeyField      = "sourceKey"
	SearchTitleField          = "title"
	SearchBodyField           = "body"
	SearchCommentsField       = "comments"
	SearchFilesField          = "files"
	SearchScopeField          = "scope"
	SearchAudienceField       = "audience"
	SearchAgentsField         = "agents"
	SearchTextHashField       = "textHash"
	SearchEmbeddingField      = "embedding"
	SearchEmbeddingModelField = "embeddingModel"
	SearchIndexedAtField      = "indexedAt"
)

// SearchTextConfig is the text search configuration the index's words are
// parsed and folded by: Postgres's default parser, its words folded to
// lower case and none stemmed or dropped, the configuration the engine's
// full-text index falls back to where it has no unaccent (D58, D155).
const SearchTextConfig = "simple"

// SearchDocument is the expression of the index row's words, each weighed
// by where it is: the title A, the body B, the conversation C and the
// files' text D, which ts_rank_cd ranks by. The index's GIN index covers
// this expression, and a search matches it as written.
func SearchDocument(qualifier string) string {
	column := func(name string) string {
		if qualifier == "" {
			return name
		}
		return qualifier + "." + name
	}
	part := func(name, weight string) string {
		return fmt.Sprintf("setweight(to_tsvector('%s'::regconfig, COALESCE(%s, '')), '%s')", SearchTextConfig, column(name), weight)
	}
	return "(" + part("title", "A") + " || " + part("body", "B") + " || " + part("comments", "C") + " || " + part("files", "D") + ")"
}

// SearchIndexingFields are the fields the loader fills in a @searchable
// declaration's queue: the row's table, by its type's name, and its key
// as the database writes it as text.
var SearchIndexingFields = []string{"sourceTable", "sourceKey"}

// SearchableTables returns the schema's @searchable tables, by name.
func (s *Schema) SearchableTables() []*TypeDef {
	var out []*TypeDef
	for _, name := range s.typeNames() {
		if td := s.Types[name]; td != nil && td.Searchable != nil {
			out = append(out, td)
		}
	}
	return out
}

// validateSearchable checks what a @searchable declaration alone decides
// (D155): a title, a queue, and no field named twice. Whether the fields
// and the queue are the table's and the schema's is the loader's
// verification to check.
func validateSearchable(td *TypeDef) []error {
	s := td.Searchable
	if s == nil {
		return nil
	}
	var errs []error
	if s.Title == "" {
		errs = append(errs, fmt.Errorf("%s: @searchable names no title, the field that titles each row", td.Name))
	}
	if s.Queue == "" {
		errs = append(errs, fmt.Errorf("%s: @searchable names no queue: a write that changes the indexed text enqueues a message on it, and its worker rebuilds the row's index row", td.Name))
	}
	seen := map[string]bool{}
	for _, field := range s.Fields() {
		if field == "" {
			continue
		}
		if seen[field] {
			errs = append(errs, fmt.Errorf("%s: @searchable names %s twice", td.Name, field))
		}
		seen[field] = true
	}
	return errs
}
