package ormgen

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// References and attachments on any row in the Go ORM (D154, D184):
// rowrefs.go holds the database's References and Attachments, the
// schema runtime's rowrefs over the database or a transaction, as the
// TypeScript ORM's orm.references and orm.attachments are the HTTP
// runtime's rowfeatures.ts. It is written for a database with a
// @references table, an @attachments table, or a table whose Comments ref
// blocks reach rows. Its definitions are the TypeScript ORM's (tsgen's
// rowrefs.go), keyed by type, with no repository member, which a Go
// repository is named by its type; Visible reads a table's rows through
// its repository, which keeps them to the caller (D180).

// RowFeaturesModel is what rowrefs.go is generated from.
type RowFeaturesModel struct {
	// References and Attachments are the definitions, as the JSON
	// rowrefs.MustParse reads, each "" for none.
	References  string
	Attachments string

	// Tables are the tables a reference, a backlink or a file reads rows
	// of through their repositories, sorted by type.
	Tables []RowFeatureTable

	// UsesTypes reports a key of the types module's, which rowrefs.go
	// imports.
	UsesTypes bool
}

// RowFeatureTable is one table rowrefs.go reads rows of: its type, whose
// repository Visible reads, and its key's Go type and kind, json (a UUID
// or a text key, read from its quoted wire form) or number.
type RowFeatureTable struct {
	Type    string
	KeyType string
	Number  bool
}

type rowTableDef struct {
	Table string `json:"table"`
	Row   string `json:"row"`
	UUID  bool   `json:"uuid,omitempty"`
}

type referencesHolderDef struct {
	rowTableDef
	Tables    []string `json:"tables,omitempty"`
	MaxPerRow int      `json:"maxPerRow"`
}

type referenceTargetDef struct {
	Table      string `json:"table"`
	Key        string `json:"key"`
	UUID       bool   `json:"uuid,omitempty"`
	Owner      string `json:"owner,omitempty"`
	Restricted string `json:"restricted,omitempty"`
}

type commentsSourceDef struct {
	rowTableDef
	Blocks string `json:"blocks"`
	State  string `json:"state"`
	ID     string `json:"id"`
}

type attachmentsHolderDef struct {
	rowTableDef
	MaxFileBytes int64    `json:"maxFileBytes"`
	MaxRowBytes  int64    `json:"maxRowBytes"`
	MaxFiles     int      `json:"maxFiles"`
	MediaTypes   []string `json:"mediaTypes,omitempty"`
	Released     string   `json:"released"`
}

// rowFeaturesOf is the row features model of schema's tables, nil for a
// database with no @references or @attachments table and no Comments.
func rowFeaturesOf(schema *ir.Schema, tables []sqlutil.TableModel, repositories []Repository) (*RowFeaturesModel, error) {
	byType := map[string]*sqlutil.TableModel{}
	for i := range tables {
		byType[tables[i].Type] = &tables[i]
	}
	repos := map[string]*Repository{}
	for i := range repositories {
		repos[repositories[i].TypeName] = &repositories[i]
	}
	column := func(typeName, field string) (string, error) {
		t := byType[typeName]
		if t == nil {
			return "", fmt.Errorf("ormgen: %s has no table", typeName)
		}
		for _, c := range t.Columns {
			if c.Field == field {
				return c.Quoted, nil
			}
		}
		return "", fmt.Errorf("ormgen: table %s has no column of field %s", t.Name, field)
	}
	uuidKey := func(typeName string) bool {
		t := byType[typeName]
		if t == nil {
			return false
		}
		key := t.KeyColumn()
		return key == nil || key.Def == nil || key.Def.TypeRef.Name == "Identity.UUID" || key.Def.TypeRef.Name == "Identity.UserID"
	}
	read := map[string]bool{}
	rowTable := func(row, sub, rowField string) (rowTableDef, error) {
		t := byType[sub]
		if t == nil {
			return rowTableDef{}, fmt.Errorf("ormgen: the sub-table %s of %s has no table", sub, row)
		}
		rowColumn, err := column(sub, rowField)
		if err != nil {
			return rowTableDef{}, err
		}
		if repos[row] == nil {
			return rowTableDef{}, fmt.Errorf("ormgen: %s, whose rows hold %s, has no repository", row, sub)
		}
		read[row] = true
		return rowTableDef{Table: t.Quoted, Row: rowColumn, UUID: uuidKey(row)}, nil
	}

	model := &RowFeaturesModel{}
	comments := map[string]commentsSourceDef{}
	for _, name := range sortedTypeNamesOf(schema) {
		td := schema.Types[name]
		if td.BehaviorTable == nil || td.BehaviorTable.Behavior != "Comments" || td.BehaviorTable.Table != "messages" {
			continue
		}
		rt, err := rowTable(td.BehaviorTable.Row, name, td.BehaviorTable.RowField)
		if err != nil {
			return nil, err
		}
		source := commentsSourceDef{rowTableDef: rt}
		for field, into := range map[string]*string{"blocks": &source.Blocks, "state": &source.State, "message": &source.ID} {
			if *into, err = column(name, field); err != nil {
				return nil, err
			}
		}
		comments[td.BehaviorTable.Row] = source
	}
	holders := schema.RowReferencesTables()
	if len(holders) > 0 || len(comments) > 0 {
		doc := struct {
			Holders  map[string]referencesHolderDef `json:"holders"`
			Targets  map[string]referenceTargetDef  `json:"targets"`
			Comments map[string]commentsSourceDef   `json:"comments,omitempty"`
		}{Holders: map[string]referencesHolderDef{}, Targets: map[string]referenceTargetDef{}}
		if len(comments) > 0 {
			doc.Comments = comments
		}
		for _, td := range holders {
			sub := ir.RowReferencesTypeName(td.Name)
			rt, err := rowTable(td.Name, sub, schema.Types[sub].RowTable.RowField)
			if err != nil {
				return nil, err
			}
			doc.Holders[td.Name] = referencesHolderDef{rowTableDef: rt, Tables: slices.Clone(td.RowReferences.Tables), MaxPerRow: td.RowReferences.PerRow()}
		}
		for _, name := range sortedTypeNamesOf(schema) {
			td := schema.Types[name]
			t := byType[name]
			if t == nil || repos[name] == nil || !referenceTarget(td) {
				continue
			}
			target := referenceTargetDef{Table: t.Quoted, Key: t.KeyColumn().Quoted, UUID: uuidKey(name)}
			if a := td.Audience; a != nil {
				owner := a.Owner
				if owner == "" {
					owner = sqlutil.AuditCreatedBy
				}
				var err error
				if target.Owner, err = column(name, owner); err != nil {
					if target.Owner, err = column(name, strings.TrimSuffix(owner, "Id")); err != nil {
						return nil, err
					}
				}
				if target.Restricted, err = column(name, ir.EffectiveAudienceField); err != nil {
					return nil, err
				}
			}
			doc.Targets[name] = target
			read[name] = true
		}
		data, err := json.Marshal(doc)
		if err != nil {
			return nil, err
		}
		model.References = string(data)
	}
	if atts := schema.AttachmentsTables(); len(atts) > 0 {
		doc := struct {
			Holders map[string]attachmentsHolderDef `json:"holders"`
		}{Holders: map[string]attachmentsHolderDef{}}
		for _, td := range atts {
			sub := ir.AttachmentsTypeName(td.Name)
			rt, err := rowTable(td.Name, sub, schema.Types[sub].RowTable.RowField)
			if err != nil {
				return nil, err
			}
			queue := schema.Types[td.Attachments.Released]
			if queue == nil || queue.Queue == nil {
				return nil, fmt.Errorf("ormgen: @attachments on %s: %s is no queue", td.Name, td.Attachments.Released)
			}
			doc.Holders[td.Name] = attachmentsHolderDef{
				rowTableDef:  rt,
				MaxFileBytes: td.Attachments.FileBytes(),
				MaxRowBytes:  td.Attachments.RowBytes(),
				MaxFiles:     td.Attachments.Files(),
				MediaTypes:   slices.Clone(td.Attachments.MediaTypes),
				Released:     sqlutil.QuoteIdentifier(sqlutil.QueueTableName(queue)),
			}
		}
		data, err := json.Marshal(doc)
		if err != nil {
			return nil, err
		}
		model.Attachments = string(data)
	}
	if model.References == "" && model.Attachments == "" {
		return nil, nil
	}
	names := make([]string, 0, len(read))
	for name := range read {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		repo := repos[name]
		number := strings.HasPrefix(repo.LookupKeyType, "int") || strings.HasPrefix(repo.LookupKeyType, "uint") || strings.HasPrefix(repo.LookupKeyType, "float")
		model.Tables = append(model.Tables, RowFeatureTable{Type: name, KeyType: repo.LookupKeyType, Number: number})
		model.UsesTypes = model.UsesTypes || strings.HasPrefix(repo.LookupKeyType, "types.")
	}
	return model, nil
}

// referenceTarget reports whether a reference may point at td's rows: a
// plain table of the database with one key, as sqlgen's clear trigger
// reads it (tsgen's referenceTarget).
func referenceTarget(td *ir.TypeDef) bool {
	if td == nil || td.Role != ir.RoleDBTable || td.Queue != nil || td.Projection != nil || td.JsonField || td.GraphMember != nil ||
		td.BehaviorTable != nil || td.BehaviorQueue != nil || td.RowTable != nil || td.SearchIndex {
		return false
	}
	keys := 0
	for _, fd := range td.Fields {
		if fd != nil && fd.Key {
			keys++
		}
	}
	return keys == 1
}

func sortedTypeNamesOf(schema *ir.Schema) []string {
	names := make([]string, 0, len(schema.Types))
	for name := range schema.Types {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
