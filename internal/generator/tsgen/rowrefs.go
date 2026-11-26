package tsgen

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// References and attachments as orm.ts describes them to the HTTP
// runtime (D154): the sub-tables a @references or an @attachments table
// keeps, the tables a reference may point at, and the conversations whose
// ref blocks are references too. The runtime's orm.references and
// orm.attachments read them (runtime/http/typescript/src/rowfeatures.ts).
// Each is a JSON document whose members mirror RowReferencesDefinition and
// AttachmentsDefinition there.

// ormRowTable is a sub-table of a row: the holder's repository member, the
// sub-table, its column of the row's key, and whether that key is a UUID.
type ormRowTable struct {
	Member string `json:"member"`
	Table  string `json:"table"`
	Row    string `json:"row"`
	UUID   bool   `json:"uuid,omitempty"`
}

// ormReferencesHolder is a @references table's sub-table, with the tables
// it may point at (none for any) and its bound per row.
type ormReferencesHolder struct {
	ormRowTable
	Tables    []string `json:"tables,omitempty"`
	MaxPerRow int      `json:"maxPerRow"`
}

// ormReferenceTarget is a table a reference may point at: its member, its
// table and key column, whether the key is a UUID, and on an @audience
// table its owner's and its resolved audience's columns, which tell its
// owner how many rows they cannot see point at a private row.
type ormReferenceTarget struct {
	Member     string `json:"member"`
	Table      string `json:"table"`
	Key        string `json:"key"`
	UUID       bool   `json:"uuid,omitempty"`
	Owner      string `json:"owner,omitempty"`
	Restricted string `json:"restricted,omitempty"`
}

// ormCommentsSource is a table's Comments messages sub-table, whose ref
// blocks are references of the row's (D154).
type ormCommentsSource struct {
	ormRowTable
	Blocks string `json:"blocks"`
	State  string `json:"state"`
	ID     string `json:"id"`
}

// ormReferences is RowReferencesDefinition.
type ormReferences struct {
	Holders  map[string]ormReferencesHolder `json:"holders"`
	Targets  map[string]ormReferenceTarget  `json:"targets"`
	Comments map[string]ormCommentsSource   `json:"comments,omitempty"`
}

// ormAttachmentsHolder is an @attachments table's sub-table, with its
// bounds and the released queue's table.
type ormAttachmentsHolder struct {
	ormRowTable
	MaxFileBytes int64    `json:"maxFileBytes"`
	MaxRowBytes  int64    `json:"maxRowBytes"`
	MaxFiles     int      `json:"maxFiles"`
	MediaTypes   []string `json:"mediaTypes,omitempty"`
	Released     string   `json:"released"`
}

// ormAttachments is AttachmentsDefinition.
type ormAttachments struct {
	Holders map[string]ormAttachmentsHolder `json:"holders"`
}

// rowFeaturesOf writes the references and attachments documents of the
// schema's tables, as JSON, each "" for a database with none: references
// for a database with a @references table or a table whose Comments ref
// blocks reach rows, attachments for one with an @attachments table.
func rowFeaturesOf(schema *ir.Schema, tables []sqlutil.TableModel, members map[string]string) (string, string, error) {
	byType := map[string]*sqlutil.TableModel{}
	for i := range tables {
		byType[tables[i].Type] = &tables[i]
	}
	column := func(typeName, field string) (string, error) {
		t := byType[typeName]
		if t == nil {
			return "", fmt.Errorf("tsgen: %s has no table", typeName)
		}
		for _, c := range t.Columns {
			if c.Field == field {
				return c.Quoted, nil
			}
		}
		return "", fmt.Errorf("tsgen: table %s has no column of field %s", t.Name, field)
	}
	uuidKey := func(typeName string) bool {
		t := byType[typeName]
		if t == nil {
			return false
		}
		key := t.KeyColumn()
		return key.Def == nil || key.Def.TypeRef.Name == "Identity.UUID" || key.Def.TypeRef.Name == "Identity.UserID"
	}
	rowTable := func(row, sub, rowField string) (ormRowTable, error) {
		t := byType[sub]
		if t == nil {
			return ormRowTable{}, fmt.Errorf("tsgen: the sub-table %s of %s has no table", sub, row)
		}
		rowColumn, err := column(sub, rowField)
		if err != nil {
			return ormRowTable{}, err
		}
		return ormRowTable{Member: members[row], Table: t.Quoted, Row: rowColumn, UUID: uuidKey(row)}, nil
	}

	var refsJSON, attsJSON string
	comments := map[string]ormCommentsSource{}
	for _, name := range sortedTypeNames(schema) {
		td := schema.Types[name]
		if td.BehaviorTable == nil || td.BehaviorTable.Behavior != "Comments" || td.BehaviorTable.Table != "messages" {
			continue
		}
		rt, err := rowTable(td.BehaviorTable.Row, name, td.BehaviorTable.RowField)
		if err != nil {
			return "", "", err
		}
		source := ormCommentsSource{ormRowTable: rt}
		for field, into := range map[string]*string{"blocks": &source.Blocks, "state": &source.State, "message": &source.ID} {
			if *into, err = column(name, field); err != nil {
				return "", "", err
			}
		}
		comments[td.BehaviorTable.Row] = source
	}
	holders := schema.RowReferencesTables()
	if len(holders) > 0 || len(comments) > 0 {
		doc := ormReferences{Holders: map[string]ormReferencesHolder{}, Targets: map[string]ormReferenceTarget{}}
		if len(comments) > 0 {
			doc.Comments = comments
		}
		for _, td := range holders {
			rt, err := rowTable(td.Name, ir.RowReferencesTypeName(td.Name), schema.Types[ir.RowReferencesTypeName(td.Name)].RowTable.RowField)
			if err != nil {
				return "", "", err
			}
			doc.Holders[td.Name] = ormReferencesHolder{ormRowTable: rt, Tables: slices.Clone(td.RowReferences.Tables), MaxPerRow: td.RowReferences.PerRow()}
		}
		for _, name := range sortedTypeNames(schema) {
			td := schema.Types[name]
			t := byType[name]
			if t == nil || members[name] == "" || !referenceTarget(td) {
				continue
			}
			target := ormReferenceTarget{Member: members[name], Table: t.Quoted, Key: t.KeyColumn().Quoted, UUID: uuidKey(name)}
			if a := td.Audience; a != nil {
				owner := a.Owner
				if owner == "" {
					owner = sqlutil.AuditCreatedBy
				}
				var err error
				if target.Owner, err = column(name, owner); err != nil {
					if target.Owner, err = column(name, strings.TrimSuffix(owner, "Id")); err != nil {
						return "", "", err
					}
				}
				if target.Restricted, err = column(name, ir.EffectiveAudienceField); err != nil {
					return "", "", err
				}
			}
			doc.Targets[name] = target
		}
		data, err := json.Marshal(doc)
		if err != nil {
			return "", "", err
		}
		refsJSON = string(data)
	}
	if atts := schema.AttachmentsTables(); len(atts) > 0 {
		doc := ormAttachments{Holders: map[string]ormAttachmentsHolder{}}
		for _, td := range atts {
			sub := ir.AttachmentsTypeName(td.Name)
			rt, err := rowTable(td.Name, sub, schema.Types[sub].RowTable.RowField)
			if err != nil {
				return "", "", err
			}
			queue := schema.Types[td.Attachments.Released]
			if queue == nil || queue.Queue == nil {
				return "", "", fmt.Errorf("tsgen: @attachments on %s: %s is no queue", td.Name, td.Attachments.Released)
			}
			doc.Holders[td.Name] = ormAttachmentsHolder{
				ormRowTable:  rt,
				MaxFileBytes: td.Attachments.FileBytes(),
				MaxRowBytes:  td.Attachments.RowBytes(),
				MaxFiles:     td.Attachments.Files(),
				MediaTypes:   slices.Clone(td.Attachments.MediaTypes),
				Released:     sqlutil.QuoteIdentifier(sqlutil.QueueTableName(queue)),
			}
		}
		data, err := json.Marshal(doc)
		if err != nil {
			return "", "", err
		}
		attsJSON = string(data)
	}
	return refsJSON, attsJSON, nil
}

// referenceTarget reports whether a reference may point at td's rows: a
// plain table of the database with one key, as sqlgen's clear trigger
// reads it.
func referenceTarget(td *ir.TypeDef) bool {
	if td == nil || td.Role != ir.RoleDBTable || td.Queue != nil || td.Projection != nil || td.JsonField || td.GraphMember != nil ||
		td.BehaviorTable != nil || td.BehaviorQueue != nil || td.RowTable != nil {
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

func sortedTypeNames(schema *ir.Schema) []string {
	names := make([]string, 0, len(schema.Types))
	for name := range schema.Types {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
