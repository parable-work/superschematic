package tsgen

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// A @searchable table as orm.ts describes it to the HTTP runtime (D155):
// where its index rows are, which of its columns an index row is built
// from, and the sub-tables whose text it holds too. The runtime's search
// (runtime/http/typescript/src/search-tables.ts) ranks every table's index
// rows in one query and builds a row's index row from these. Each is a
// JSON document whose members mirror SearchableDefinition there.

// searchIndexAlias is the alias a search names the index by, which
// Document's expression qualifies its columns with.
const searchIndexAlias = "si"

// ormSearchable is SearchableDefinition.
type ormSearchable struct {
	// Index is the index table, quoted, and Document the expression of an
	// index row's weighed words over it as searchIndexAlias.
	Index    string `json:"index"`
	Document string `json:"document"`
	// FTS is the FTS5 table of the index's words on SQLite, quoted, which
	// a search there matches and ranks in place of Document (D186).
	FTS string `json:"fts,omitempty"`
	// IndexAudience is how the index's audience is compared on SQLite.
	IndexAudience *ormSearchIndexAudience `json:"indexAudience,omitempty"`
	// Table is the table, quoted; Key its key column, quoted, KeyType the
	// key column's SQL type, and UUID whether its wire form is base62.
	Table   string `json:"table"`
	Key     string `json:"key"`
	KeyType string `json:"keyType"`
	UUID    bool   `json:"uuid,omitempty"`
	// Title, Body and Scope are the columns an index row's title, body
	// and scope are of, quoted.
	Title string   `json:"title"`
	Body  []string `json:"body,omitempty"`
	Scope string   `json:"scope,omitempty"`
	// DeletedAt is a soft-deleting table's column, quoted.
	DeletedAt string `json:"deletedAt,omitempty"`
	// Audience are an @audience table's resolved columns, quoted.
	Audience *ormSearchAudience `json:"audience,omitempty"`
	// Comments is the table's Comments messages sub-table, and Files its
	// attachments sub-table, when the declaration indexes them.
	Comments *ormSearchComments `json:"comments,omitempty"`
	Files    *ormSearchFiles    `json:"files,omitempty"`
}

type ormSearchAudience struct {
	Effective string `json:"effective"`
	Agents    string `json:"agents"`
}

// ormSearchIndexAudience is how a search compares the index's copied
// audience with a caller's ids on SQLite (sqlutil.SQLiteAudienceContains),
// absent on Postgres, whose array overlap the runtime writes (D186).
type ormSearchIndexAudience struct {
	Contains    string `json:"contains"`
	Placeholder string `json:"placeholder"`
}

type ormSearchComments struct {
	Table string `json:"table"`
	Row   string `json:"row"`
	Body  string `json:"body"`
	State string `json:"state"`
	Order string `json:"order"`
}

type ormSearchFiles struct {
	Table string `json:"table"`
	Row   string `json:"row"`
	Text  string `json:"text"`
	State string `json:"state"`
}

// searchableOf writes td's SearchableDefinition, as JSON, or "" for a
// table that is not @searchable.
func searchableOf(schema *ir.Schema, td *ir.TypeDef, tables []sqlutil.TableModel) (string, error) {
	s := td.Searchable
	if s == nil {
		return "", nil
	}
	byType := map[string]*sqlutil.TableModel{}
	for i := range tables {
		byType[tables[i].Type] = &tables[i]
	}
	column := func(typeName, field string) (*sqlutil.ColumnModel, error) {
		t := byType[typeName]
		if t == nil {
			return nil, fmt.Errorf("tsgen: %s has no table", typeName)
		}
		for i := range t.Columns {
			if t.Columns[i].Field == field {
				return &t.Columns[i], nil
			}
		}
		return nil, fmt.Errorf("tsgen: table %s has no column of field %s", t.Name, field)
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
		return "", fmt.Errorf("tsgen: @searchable on %s: the table or the search index has no table", td.Name)
	}
	key := table.KeyColumn()
	out := ormSearchable{
		Index:    index.Quoted,
		Document: ir.SearchDocument(searchIndexAlias),
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
		return "", err
	}
	for _, field := range s.Body {
		c, err := quoted(td.Name, field)
		if err != nil {
			return "", err
		}
		out.Body = append(out.Body, c)
	}
	if s.Scope != "" {
		if out.Scope, err = quoted(td.Name, s.Scope); err != nil {
			return "", err
		}
	}
	if c, err := column(td.Name, sqlutil.AuditDeletedAt); err == nil && c.Audit == sqlutil.AuditDeletedAt {
		out.DeletedAt = c.Quoted
	}
	if td.Audience != nil {
		a := &ormSearchAudience{}
		if a.Effective, err = quoted(td.Name, ir.EffectiveAudienceField); err != nil {
			return "", err
		}
		if a.Agents, err = quoted(td.Name, ir.EffectiveAgentsField); err != nil {
			return "", err
		}
		out.Audience = a
	}
	if s.IndexesComments() {
		for _, name := range sortedTypeNames(schema) {
			m := schema.Types[name].BehaviorTable
			if m == nil || m.Row != td.Name || m.Behavior != "Comments" || m.Table != "messages" || byType[name] == nil {
				continue
			}
			c := &ormSearchComments{Table: byType[name].Quoted}
			for field, into := range map[string]*string{m.RowField: &c.Row, "body": &c.Body, "state": &c.State, "message": &c.Order} {
				if *into, err = quoted(name, field); err != nil {
					return "", err
				}
			}
			out.Comments = c
		}
	}
	if s.IndexesAttachments() && td.Attachments != nil {
		name := ir.AttachmentsTypeName(td.Name)
		if sub := schema.Types[name]; sub != nil && sub.RowTable != nil && byType[name] != nil {
			f := &ormSearchFiles{Table: byType[name].Quoted}
			for field, into := range map[string]*string{sub.RowTable.RowField: &f.Row, ir.AttExtractedField: &f.Text, ir.AttStateField: &f.State} {
				if *into, err = quoted(name, field); err != nil {
					return "", err
				}
			}
			out.Files = f
		}
	}
	data, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
