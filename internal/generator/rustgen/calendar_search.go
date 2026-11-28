package rustgen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// The calendar (D152) and search across tables (D155) as the Rust
// repositories read them (D188): a table's @calendarSource declarations
// and its @searchable index, members of the table in the Postgres and the
// SQLite definitions (orm::Table's calendar and searchable), which the
// runtime's orm::calendar and orm::search_tables read, on SQLite through
// their sqlite modules (#615). They hold what tsgen's calendar.go and
// searchable.go write into orm.ts and orm-sqlite.ts, member for member,
// so the two runtimes read the same tables and columns.

// rustCalendarSource is orm::calendar::CalendarSource.
type rustCalendarSource struct {
	Name        string                   `json:"name"`
	Kind        string                   `json:"kind"`
	Title       string                   `json:"title"`
	Color       string                   `json:"color,omitempty"`
	Field       string                   `json:"field,omitempty"`
	End         string                   `json:"end,omitempty"`
	Owner       string                   `json:"owner,omitempty"`
	Permission  string                   `json:"permission,omitempty"`
	Occurrences *rustCalendarOccurrences `json:"occurrences,omitempty"`
}

// rustCalendarOccurrences is orm::calendar::CalendarOccurrences: the
// Recurrence's occurrences sub-table, unquoted, and its columns of the
// row's key, an occurrence's key, wall due, due instant and state.
type rustCalendarOccurrences struct {
	Table string `json:"table"`
	Row   string `json:"row"`
	Key   string `json:"key"`
	Due   string `json:"due"`
	DueAt string `json:"dueAt"`
	State string `json:"state"`
}

// calendarOf is td's calendar sources, each named
// (ir.CalendarSourceName); nil for a table with none.
func calendarOf(schema *ir.Schema, td *ir.TypeDef, tables []sqlutil.TableModel) ([]rustCalendarSource, error) {
	if td == nil {
		return nil, nil
	}
	var out []rustCalendarSource
	for i, source := range td.CalendarSources {
		entry := rustCalendarSource{
			Name:  ir.CalendarSourceName(td, i),
			Kind:  source.Kind,
			Title: source.Title,
			Color: source.Color,
			Field: source.Field,
			End:   source.End,
			Owner: source.Owner,
			// Permission is the schema's (#601).
			Permission: source.Permission,
		}
		if source.Occurrences {
			occurrences, err := occurrencesOf(schema, td, tables)
			if err != nil {
				return nil, err
			}
			entry.Occurrences = occurrences
		}
		out = append(out, entry)
	}
	return out, nil
}

// occurrencesOf is the occurrences sub-table of td's Recurrence.
func occurrencesOf(schema *ir.Schema, td *ir.TypeDef, tables []sqlutil.TableModel) (*rustCalendarOccurrences, error) {
	var sub *ir.TypeDef
	for _, name := range sortedNames(schema) {
		if m := schema.Types[name].BehaviorTable; m != nil && m.Row == td.Name && m.Use == "Recurrence" && m.Table == "occurrences" {
			sub = schema.Types[name]
		}
	}
	if sub == nil {
		return nil, fmt.Errorf("rustgen: table %s's @calendarSource dates occurrences, and it keeps no Recurrence occurrences", td.Name)
	}
	for _, model := range tables {
		if model.Type != sub.Name {
			continue
		}
		out := &rustCalendarOccurrences{Table: model.Name}
		for _, c := range model.Columns {
			if c.Def == nil {
				continue
			}
			switch c.Def.Name {
			case sub.BehaviorTable.RowField:
				out.Row = c.Name
			case "key":
				out.Key = c.Name
			case "due":
				out.Due = c.Name
			case "dueAt":
				out.DueAt = c.Name
			case "state":
				out.State = c.Name
			}
		}
		if out.Row == "" || out.Key == "" || out.DueAt == "" || out.State == "" {
			return nil, fmt.Errorf("rustgen: %s's occurrences table %s lacks a column the calendar reads", td.Name, model.Name)
		}
		return out, nil
	}
	return nil, fmt.Errorf("rustgen: %s's occurrences are the type %s, which is no table of the schema", td.Name, sub.Name)
}

// searchIndexAlias is the alias a search names the index by, which
// rustSearchable's Document qualifies its columns with.
const searchIndexAlias = "si"

// rustSearchable is orm::search_tables::SearchableDefinition.
type rustSearchable struct {
	Index    string `json:"index"`
	Document string `json:"document"`
	// Config is the text search configuration the index's words are in
	// on Postgres, when it is not simple: search_unaccent, which folds
	// diacritics (#740).
	Config    string              `json:"config,omitempty"`
	Table     string              `json:"table"`
	Key       string              `json:"key"`
	KeyType   string              `json:"keyType"`
	UUID      bool                `json:"uuid,omitempty"`
	Title     string              `json:"title"`
	Body      []string            `json:"body,omitempty"`
	Scope     string              `json:"scope,omitempty"`
	DeletedAt string              `json:"deletedAt,omitempty"`
	Audience  *rustSearchAudience `json:"audience,omitempty"`
	Comments  *rustSearchComments `json:"comments,omitempty"`
	Files     *rustSearchFiles    `json:"files,omitempty"`
	// FTS is the FTS5 table of the index's words on SQLite (D186), which
	// a search there matches and ranks in place of Document.
	FTS string `json:"fts,omitempty"`
}

// sqliteSearchable is a @searchable table's index on SQLite (D186), as
// tsgen's sqliteSearchable writes it: Postgres's, its words the FTS5 table
// beside the index in place of the weighed document, and its key's type
// SQLite's. Nil for nil.
func sqliteSearchable(postgres *rustSearchable) *rustSearchable {
	if postgres == nil {
		return nil
	}
	s := *postgres
	s.Document = ""
	s.Config = ""
	s.FTS = sqlutil.QuoteIdentifier(sqlutil.SearchFTSTable)
	s.KeyType = "text"
	return &s
}

type rustSearchAudience struct {
	Effective string `json:"effective"`
	Agents    string `json:"agents"`
}

type rustSearchComments struct {
	Table string `json:"table"`
	Row   string `json:"row"`
	Body  string `json:"body"`
	State string `json:"state"`
	Order string `json:"order"`
}

type rustSearchFiles struct {
	Table string `json:"table"`
	Row   string `json:"row"`
	Text  string `json:"text"`
	State string `json:"state"`
}

// searchableOf is td's search index definition, or nil for a table that
// is not @searchable.
func searchableOf(schema *ir.Schema, td *ir.TypeDef, tables []sqlutil.TableModel) (*rustSearchable, error) {
	if td == nil || td.Searchable == nil {
		return nil, nil
	}
	s := td.Searchable
	byType := map[string]*sqlutil.TableModel{}
	for i := range tables {
		byType[tables[i].Type] = &tables[i]
	}
	column := func(typeName, field string) (*sqlutil.ColumnModel, error) {
		t := byType[typeName]
		if t == nil {
			return nil, fmt.Errorf("rustgen: %s has no table", typeName)
		}
		for i := range t.Columns {
			if t.Columns[i].Field == field {
				return &t.Columns[i], nil
			}
		}
		return nil, fmt.Errorf("rustgen: table %s has no column of field %s", t.Name, field)
	}
	quoted := func(typeName, field string) (string, error) {
		c, err := column(typeName, field)
		if err != nil {
			return "", err
		}
		return c.Quoted, nil
	}
	table, index := byType[td.Name], byType[ir.SearchIndexTypeName]
	if table == nil || index == nil {
		return nil, fmt.Errorf("rustgen: @searchable on %s: the table or the search index has no table", td.Name)
	}
	key := table.KeyColumn()
	out := &rustSearchable{
		Index:    index.Quoted,
		Document: ir.SearchDocumentIn(schema.SearchConfig(), searchIndexAlias),
		Config:   searchConfigOf(schema),
		Table:    table.Quoted,
		Key:      key.Quoted,
		KeyType:  strings.ToLower(key.Type),
		UUID:     key.Def == nil || key.Def.TypeRef.Name == "Identity.UUID" || key.Def.TypeRef.Name == "Identity.UserID",
	}
	if out.KeyType == "" {
		out.KeyType = "uuid"
	}
	var err error
	if out.Title, err = quoted(td.Name, s.Title); err != nil {
		return nil, err
	}
	for _, field := range s.Body {
		c, err := quoted(td.Name, field)
		if err != nil {
			return nil, err
		}
		out.Body = append(out.Body, c)
	}
	if s.Scope != "" {
		if out.Scope, err = quoted(td.Name, s.Scope); err != nil {
			return nil, err
		}
	}
	if c, err := column(td.Name, sqlutil.AuditDeletedAt); err == nil && c.Audit == sqlutil.AuditDeletedAt {
		out.DeletedAt = c.Quoted
	}
	if td.Audience != nil {
		a := &rustSearchAudience{}
		if a.Effective, err = quoted(td.Name, ir.EffectiveAudienceField); err != nil {
			return nil, err
		}
		if a.Agents, err = quoted(td.Name, ir.EffectiveAgentsField); err != nil {
			return nil, err
		}
		out.Audience = a
	}
	if s.IndexesComments() {
		for _, name := range sortedNames(schema) {
			m := schema.Types[name].BehaviorTable
			if m == nil || m.Row != td.Name || m.Behavior != "Comments" || m.Table != "messages" || byType[name] == nil {
				continue
			}
			c := &rustSearchComments{Table: byType[name].Quoted}
			for field, into := range map[string]*string{m.RowField: &c.Row, "body": &c.Body, "state": &c.State, "message": &c.Order} {
				if *into, err = quoted(name, field); err != nil {
					return nil, err
				}
			}
			out.Comments = c
		}
	}
	if s.IndexesAttachments() && td.Attachments != nil {
		name := ir.AttachmentsTypeName(td.Name)
		if sub := schema.Types[name]; sub != nil && sub.RowTable != nil && byType[name] != nil {
			f := &rustSearchFiles{Table: byType[name].Quoted}
			for field, into := range map[string]*string{sub.RowTable.RowField: &f.Row, ir.AttExtractedField: &f.Text, ir.AttStateField: &f.State} {
				if *into, err = quoted(name, field); err != nil {
					return nil, err
				}
			}
			out.Files = f
		}
	}
	return out, nil
}

// sortedNames are the schema's type names in order.
func sortedNames(schema *ir.Schema) []string {
	names := make([]string, 0, len(schema.Types))
	for name := range schema.Types {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// searchConfigOf is the schema's search index's text search configuration
// when it is not simple (#740), "" when it is.
func searchConfigOf(schema *ir.Schema) string {
	if config := schema.SearchConfig(); config != ir.SearchTextConfig {
		return config
	}
	return ""
}
