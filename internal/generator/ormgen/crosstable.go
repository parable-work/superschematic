package ormgen

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// The reads across tables in the Go ORM (D152, D155, D187): crosstable.go
// holds the database's calendar sources and search index, the
// definitions the schema runtime's crosstable package reads them by, and
// CrossTables, the crosstable.Host over the database: its pool, its
// access predicate (D180) and its grants. It is written for a database
// with a @calendarSource or a @searchable table. The search definition is
// tsgen's SearchableDefinition, member for member; the calendar's holds
// the columns a source is of, quoted, since a Go read across tables
// reads a table's rows by a statement held to the caller by the
// repository's own predicate, where TypeScript's reads them through the
// repository by its fields.

// CrossTablesModel is what crosstable.go is generated from: the
// definitions as the JSON crosstable.MustParse reads, each "" for none.
// SQLiteSearchable is the search definition on SQLite, for a database
// whose DB service lists the sqlite dialect (D186, #615): Postgres's, its
// words the FTS5 table beside the index in place of the weighed document,
// its key's type SQLite's and its index's audience compared through
// json_each, as tsgen's sqliteSearchable writes it.
type CrossTablesModel struct {
	Calendar         string
	Searchable       string
	SQLiteSearchable string
}

// crossTablesOf is the cross-table model of schema's tables, nil for a
// database with no calendar source and no @searchable table; with sqlite,
// it holds the search definition on SQLite too.
func crossTablesOf(schema *ir.Schema, tables []sqlutil.TableModel, sqlite bool) (*CrossTablesModel, error) {
	byType := map[string]*sqlutil.TableModel{}
	for i := range tables {
		byType[tables[i].Type] = &tables[i]
	}
	column := func(typeName, field string) (*sqlutil.ColumnModel, error) {
		t := byType[typeName]
		if t == nil {
			return nil, fmt.Errorf("ormgen: %s has no table", typeName)
		}
		for i := range t.Columns {
			if t.Columns[i].Field == field {
				return &t.Columns[i], nil
			}
		}
		return nil, fmt.Errorf("ormgen: table %s has no column of field %s", t.Name, field)
	}
	keyOf := func(t *sqlutil.TableModel) (quoted, sqlType string, uuid bool) {
		key := t.KeyColumn()
		sqlType = strings.ToLower(key.Type)
		if sqlType == "" {
			sqlType = "uuid"
		}
		uuid = key.Def == nil || key.Def.TypeRef.Name == "Identity.UUID" || key.Def.TypeRef.Name == "Identity.UserID"
		return key.Quoted, sqlType, uuid
	}

	model := &CrossTablesModel{}
	calendar := map[string]calendarTableDef{}
	searchable := map[string]searchableDef{}
	for _, name := range sortedTypeNamesOf(schema) {
		td := schema.Types[name]
		if len(td.CalendarSources) > 0 {
			t := byType[name]
			if t == nil {
				return nil, fmt.Errorf("ormgen: @calendarSource on %s: it has no table", name)
			}
			def := calendarTableDef{Table: t.Quoted, Live: sourceLive(t)}
			def.Key, def.KeyType, def.UUID = keyOf(t)
			for i, source := range td.CalendarSources {
				entry := calendarSourceDef{Name: ir.CalendarSourceName(td, i), Kind: source.Kind, Color: source.Color, Permission: source.Permission}
				title, err := column(name, source.Title)
				if err != nil {
					return nil, err
				}
				entry.Title = title.Quoted
				if source.Field != "" {
					field, err := column(name, source.Field)
					if err != nil {
						return nil, err
					}
					entry.Field = field.Quoted
					entry.Timestamp = field.Def != nil && field.Def.TypeRef.Name == "Temporal.DateTime"
				}
				if source.End != "" {
					end, err := column(name, source.End)
					if err != nil {
						return nil, err
					}
					entry.End = end.Quoted
				}
				if source.Owner != "" {
					owner, err := column(name, source.Owner)
					if err != nil {
						if owner, err = column(name, source.Owner+"Id"); err != nil {
							return nil, err
						}
					}
					entry.Owner = owner.Quoted
					entry.OwnerUUID = owner.Filter == sqlutil.FilterUUID
				}
				if source.Occurrences {
					occurrences, err := occurrencesOf(schema, td, byType)
					if err != nil {
						return nil, err
					}
					entry.Occurrences = occurrences
				}
				def.Sources = append(def.Sources, entry)
			}
			calendar[name] = def
		}
		if td.Searchable != nil {
			def, err := searchableOf(schema, td, byType, column)
			if err != nil {
				return nil, err
			}
			searchable[name] = def
		}
	}
	if len(calendar) == 0 && len(searchable) == 0 {
		return nil, nil
	}
	if len(calendar) > 0 {
		data, err := json.Marshal(calendar)
		if err != nil {
			return nil, err
		}
		model.Calendar = string(data)
	}
	if len(searchable) > 0 {
		data, err := json.Marshal(searchable)
		if err != nil {
			return nil, err
		}
		model.Searchable = string(data)
		if sqlite {
			onSQLite := make(map[string]searchableDef, len(searchable))
			for name, def := range searchable {
				def.Document = ""
				def.FTS = sqlutil.QuoteIdentifier(sqlutil.SearchFTSTable)
				def.KeyType = "text"
				def.IndexAudience = &searchIndexAudienceDef{Contains: sqlutil.SQLiteAudienceContains, Placeholder: sqlutil.SQLiteAudiencePlaceholder}
				onSQLite[name] = def
			}
			if data, err = json.Marshal(onSQLite); err != nil {
				return nil, err
			}
			model.SQLiteSearchable = string(data)
		}
	}
	return model, nil
}

// searchIndexAudienceDef is crosstable.SearchIndexAudience.
type searchIndexAudienceDef struct {
	Contains    string `json:"contains"`
	Placeholder string `json:"placeholder"`
}

// calendarTableDef is crosstable.CalendarTable.
type calendarTableDef struct {
	Table   string              `json:"table"`
	Key     string              `json:"key"`
	KeyType string              `json:"keyType"`
	UUID    bool                `json:"uuid,omitempty"`
	Live    string              `json:"live,omitempty"`
	Sources []calendarSourceDef `json:"sources"`
}

// calendarSourceDef is crosstable.CalendarSource.
type calendarSourceDef struct {
	Name        string                  `json:"name"`
	Kind        string                  `json:"kind"`
	Color       string                  `json:"color,omitempty"`
	Title       string                  `json:"title"`
	Field       string                  `json:"field,omitempty"`
	End         string                  `json:"end,omitempty"`
	Timestamp   bool                    `json:"timestamp,omitempty"`
	Owner       string                  `json:"owner,omitempty"`
	OwnerUUID   bool                    `json:"ownerUuid,omitempty"`
	Permission  string                  `json:"permission,omitempty"`
	Occurrences *calendarOccurrencesDef `json:"occurrences,omitempty"`
}

// calendarOccurrencesDef is crosstable.CalendarOccurrences.
type calendarOccurrencesDef struct {
	Table string `json:"table"`
	Row   string `json:"row"`
	Key   string `json:"key"`
	Due   string `json:"due,omitempty"`
	DueAt string `json:"dueAt"`
	State string `json:"state"`
}

// BehaviorsWithoutJSON reports a table that composes behaviors with no
// use, deadline, change or event of its own, whose repository imports
// encoding/json for the behaviors and uses it nowhere: a table the core
// reads and writes for another's behavior, as a Recurrence's relative
// target (D152), which a Go server of fixture-recurrence-db first built
// (D187).
func (r Repository) BehaviorsWithoutJSON() bool {
	return r.HasBehaviors && len(r.Uses) == 0 && r.DeadlineQueue == "" && r.ChangeQueue == "" && r.EventQueue == "" && !r.Versioned && !r.HasJSONUnionCollections
}

// occurrencesOf is the occurrences sub-table of td's Recurrence, its
// columns quoted (tsgen's occurrencesTableOf).
func occurrencesOf(schema *ir.Schema, td *ir.TypeDef, byType map[string]*sqlutil.TableModel) (*calendarOccurrencesDef, error) {
	var sub *ir.TypeDef
	for _, name := range sortedTypeNamesOf(schema) {
		if m := schema.Types[name].BehaviorTable; m != nil && m.Row == td.Name && m.Use == "Recurrence" && m.Table == "occurrences" {
			sub = schema.Types[name]
		}
	}
	if sub == nil {
		return nil, fmt.Errorf("ormgen: table %s's @calendarSource dates occurrences, and it keeps no Recurrence occurrences", td.Name)
	}
	t := byType[sub.Name]
	if t == nil {
		return nil, fmt.Errorf("ormgen: %s's occurrences are the type %s, which is no table of the schema", td.Name, sub.Name)
	}
	out := &calendarOccurrencesDef{Table: t.Quoted}
	for _, c := range t.Columns {
		if c.Def == nil {
			continue
		}
		switch c.Def.Name {
		case sub.BehaviorTable.RowField:
			out.Row = c.Quoted
		case "key":
			out.Key = c.Quoted
		case "due":
			out.Due = c.Quoted
		case "dueAt":
			out.DueAt = c.Quoted
		case "state":
			out.State = c.Quoted
		}
	}
	if out.Row == "" || out.Key == "" || out.DueAt == "" || out.State == "" {
		return nil, fmt.Errorf("ormgen: %s's occurrences table %s lacks a column the calendar reads", td.Name, t.Name)
	}
	return out, nil
}

// searchableDef is crosstable.Searchable, tsgen's SearchableDefinition.
type searchableDef struct {
	Index     string             `json:"index"`
	Document  string             `json:"document"`
	Table     string             `json:"table"`
	Key       string             `json:"key"`
	KeyType   string             `json:"keyType"`
	UUID      bool               `json:"uuid,omitempty"`
	Title     string             `json:"title"`
	Body      []string           `json:"body,omitempty"`
	Scope     string             `json:"scope,omitempty"`
	DeletedAt string             `json:"deletedAt,omitempty"`
	Audience  *searchAudienceDef `json:"audience,omitempty"`
	Comments  *searchCommentsDef `json:"comments,omitempty"`
	Files     *searchFilesDef    `json:"files,omitempty"`
	// FTS and IndexAudience are the definition's on SQLite alone (D186).
	FTS           string                  `json:"fts,omitempty"`
	IndexAudience *searchIndexAudienceDef `json:"indexAudience,omitempty"`
}

type searchAudienceDef struct {
	Effective string `json:"effective"`
	Agents    string `json:"agents"`
}

type searchCommentsDef struct {
	Table string `json:"table"`
	Row   string `json:"row"`
	Body  string `json:"body"`
	State string `json:"state"`
	Order string `json:"order"`
}

type searchFilesDef struct {
	Table string `json:"table"`
	Row   string `json:"row"`
	Text  string `json:"text"`
	State string `json:"state"`
}

// searchableOf is td's search index, as tsgen's searchableOf writes it.
func searchableOf(schema *ir.Schema, td *ir.TypeDef, byType map[string]*sqlutil.TableModel, column func(typeName, field string) (*sqlutil.ColumnModel, error)) (searchableDef, error) {
	s := td.Searchable
	quoted := func(typeName, field string) (string, error) {
		c, err := column(typeName, field)
		if err != nil {
			return "", err
		}
		return c.Quoted, nil
	}
	table, index := byType[td.Name], byType[ir.SearchIndexTypeName]
	if table == nil || index == nil {
		return searchableDef{}, fmt.Errorf("ormgen: @searchable on %s: the table or the search index has no table", td.Name)
	}
	key := table.KeyColumn()
	out := searchableDef{
		Index:    index.Quoted,
		Document: ir.SearchDocument("si"),
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
		return out, err
	}
	for _, field := range s.Body {
		c, err := quoted(td.Name, field)
		if err != nil {
			return out, err
		}
		out.Body = append(out.Body, c)
	}
	if s.Scope != "" {
		if out.Scope, err = quoted(td.Name, s.Scope); err != nil {
			return out, err
		}
	}
	if c, err := column(td.Name, sqlutil.AuditDeletedAt); err == nil && c.Audit == sqlutil.AuditDeletedAt {
		out.DeletedAt = c.Quoted
	}
	if td.Audience != nil {
		a := &searchAudienceDef{}
		if a.Effective, err = quoted(td.Name, ir.EffectiveAudienceField); err != nil {
			return out, err
		}
		if a.Agents, err = quoted(td.Name, ir.EffectiveAgentsField); err != nil {
			return out, err
		}
		out.Audience = a
	}
	if s.IndexesComments() {
		for _, name := range sortedTypeNamesOf(schema) {
			m := schema.Types[name].BehaviorTable
			if m == nil || m.Row != td.Name || m.Behavior != "Comments" || m.Table != "messages" || byType[name] == nil {
				continue
			}
			c := &searchCommentsDef{Table: byType[name].Quoted}
			for field, into := range map[string]*string{m.RowField: &c.Row, "body": &c.Body, "state": &c.State, "message": &c.Order} {
				if *into, err = quoted(name, field); err != nil {
					return out, err
				}
			}
			out.Comments = c
		}
	}
	if s.IndexesAttachments() && td.Attachments != nil {
		name := ir.AttachmentsTypeName(td.Name)
		if sub := schema.Types[name]; sub != nil && sub.RowTable != nil && byType[name] != nil {
			f := &searchFilesDef{Table: byType[name].Quoted}
			for field, into := range map[string]*string{sub.RowTable.RowField: &f.Row, ir.AttExtractedField: &f.Text, ir.AttStateField: &f.State} {
				if *into, err = quoted(name, field); err != nil {
					return out, err
				}
			}
			out.Files = f
		}
	}
	return out, nil
}
