package rustgen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// References and attachments as the Rust repositories read them (D154,
// D185): the sub-tables a @references or an @attachments table keeps, the
// tables a reference may point at, and the conversations whose ref blocks
// are references too, each keyed by its type's name. They are members of
// the definition in each dialect (orm::Definition's references and
// attachments, on SQLite too since #611), which the runtime's
// orm::rowfeatures reads; the TypeScript document of
// tsgen/rowrefs.go holds the same tables and columns, with a repository
// member a Rust repository has no need of, since it opens a table's
// repository by its type.

// rustRowReferencesHolder is orm::rowfeatures::ReferencesHolder.
type rustRowReferencesHolder struct {
	Table     string   `json:"table"`
	Row       string   `json:"row"`
	UUID      bool     `json:"uuid,omitempty"`
	Tables    []string `json:"tables,omitempty"`
	MaxPerRow int      `json:"maxPerRow"`
}

// rustReferenceTarget is orm::rowfeatures::ReferenceTarget.
type rustReferenceTarget struct {
	Table      string `json:"table"`
	Key        string `json:"key"`
	UUID       bool   `json:"uuid,omitempty"`
	Owner      string `json:"owner,omitempty"`
	Restricted string `json:"restricted,omitempty"`
}

// rustCommentsSource is orm::rowfeatures::CommentsSource.
type rustCommentsSource struct {
	Table  string `json:"table"`
	Row    string `json:"row"`
	UUID   bool   `json:"uuid,omitempty"`
	Blocks string `json:"blocks"`
	State  string `json:"state"`
	ID     string `json:"id"`
}

// rustRowReferences is orm::rowfeatures::RowReferencesDefinition.
type rustRowReferences struct {
	Holders  map[string]rustRowReferencesHolder `json:"holders"`
	Targets  map[string]rustReferenceTarget     `json:"targets"`
	Comments map[string]rustCommentsSource      `json:"comments,omitempty"`
}

// rustAttachmentsHolder is orm::rowfeatures::AttachmentsHolder.
type rustAttachmentsHolder struct {
	Table        string   `json:"table"`
	Row          string   `json:"row"`
	UUID         bool     `json:"uuid,omitempty"`
	MaxFileBytes int64    `json:"maxFileBytes"`
	MaxRowBytes  int64    `json:"maxRowBytes"`
	MaxFiles     int      `json:"maxFiles"`
	MediaTypes   []string `json:"mediaTypes,omitempty"`
	Released     string   `json:"released"`
}

// rustAttachments is orm::rowfeatures::AttachmentsDefinition.
type rustAttachments struct {
	Holders map[string]rustAttachmentsHolder `json:"holders"`
}

// rowFeaturesOf is the references and attachments of the schema's
// tables, each nil for a database with none: references for one with a
// @references table or a table whose Comments ref blocks reach rows,
// attachments for one with an @attachments table. A target is a table of
// the database with a repository and one key.
func rowFeaturesOf(schema *ir.Schema, tables []sqlutil.TableModel, repository map[string]bool) (*rustRowReferences, *rustAttachments, error) {
	byType := map[string]*sqlutil.TableModel{}
	for i := range tables {
		byType[tables[i].Type] = &tables[i]
	}
	column := func(typeName, field string) (string, error) {
		t := byType[typeName]
		if t == nil {
			return "", fmt.Errorf("rustgen: %s has no table", typeName)
		}
		for _, c := range t.Columns {
			if c.Field == field {
				return c.Quoted, nil
			}
		}
		return "", fmt.Errorf("rustgen: table %s has no column of field %s", t.Name, field)
	}
	uuidKey := func(typeName string) bool {
		t := byType[typeName]
		if t == nil {
			return false
		}
		key := t.KeyColumn()
		return key.Def == nil || key.Def.TypeRef.Name == "Identity.UUID" || key.Def.TypeRef.Name == "Identity.UserID"
	}
	sub := func(row, sub, rowField string) (table, rowColumn string, err error) {
		t := byType[sub]
		if t == nil {
			return "", "", fmt.Errorf("rustgen: the sub-table %s of %s has no table", sub, row)
		}
		rowColumn, err = column(sub, rowField)
		return t.Quoted, rowColumn, err
	}

	names := make([]string, 0, len(schema.Types))
	for name := range schema.Types {
		names = append(names, name)
	}
	slices.Sort(names)

	comments := map[string]rustCommentsSource{}
	for _, name := range names {
		td := schema.Types[name]
		if td.BehaviorTable == nil || td.BehaviorTable.Behavior != "Comments" || td.BehaviorTable.Table != "messages" {
			continue
		}
		table, row, err := sub(td.BehaviorTable.Row, name, td.BehaviorTable.RowField)
		if err != nil {
			return nil, nil, err
		}
		source := rustCommentsSource{Table: table, Row: row, UUID: uuidKey(td.BehaviorTable.Row)}
		for field, into := range map[string]*string{"blocks": &source.Blocks, "state": &source.State, "message": &source.ID} {
			if *into, err = column(name, field); err != nil {
				return nil, nil, err
			}
		}
		comments[td.BehaviorTable.Row] = source
	}
	var refs *rustRowReferences
	holders := schema.RowReferencesTables()
	if len(holders) > 0 || len(comments) > 0 {
		refs = &rustRowReferences{Holders: map[string]rustRowReferencesHolder{}, Targets: map[string]rustReferenceTarget{}}
		if len(comments) > 0 {
			refs.Comments = comments
		}
		for _, td := range holders {
			subName := ir.RowReferencesTypeName(td.Name)
			table, row, err := sub(td.Name, subName, schema.Types[subName].RowTable.RowField)
			if err != nil {
				return nil, nil, err
			}
			refs.Holders[td.Name] = rustRowReferencesHolder{Table: table, Row: row, UUID: uuidKey(td.Name), Tables: slices.Clone(td.RowReferences.Tables), MaxPerRow: td.RowReferences.PerRow()}
		}
		for _, name := range names {
			td := schema.Types[name]
			t := byType[name]
			if t == nil || !repository[name] || !referenceTarget(td) {
				continue
			}
			target := rustReferenceTarget{Table: t.Quoted, Key: t.KeyColumn().Quoted, UUID: uuidKey(name)}
			if a := td.Audience; a != nil {
				owner := a.Owner
				if owner == "" {
					owner = sqlutil.AuditCreatedBy
				}
				var err error
				if target.Owner, err = column(name, owner); err != nil {
					if target.Owner, err = column(name, strings.TrimSuffix(owner, "Id")); err != nil {
						return nil, nil, err
					}
				}
				if target.Restricted, err = column(name, ir.EffectiveAudienceField); err != nil {
					return nil, nil, err
				}
			}
			refs.Targets[name] = target
		}
	}
	var atts *rustAttachments
	if list := schema.AttachmentsTables(); len(list) > 0 {
		atts = &rustAttachments{Holders: map[string]rustAttachmentsHolder{}}
		for _, td := range list {
			subName := ir.AttachmentsTypeName(td.Name)
			table, row, err := sub(td.Name, subName, schema.Types[subName].RowTable.RowField)
			if err != nil {
				return nil, nil, err
			}
			queue := schema.Types[td.Attachments.Released]
			if queue == nil || queue.Queue == nil {
				return nil, nil, fmt.Errorf("rustgen: @attachments on %s: %s is no queue", td.Name, td.Attachments.Released)
			}
			atts.Holders[td.Name] = rustAttachmentsHolder{
				Table:        table,
				Row:          row,
				UUID:         uuidKey(td.Name),
				MaxFileBytes: td.Attachments.FileBytes(),
				MaxRowBytes:  td.Attachments.RowBytes(),
				MaxFiles:     td.Attachments.Files(),
				MediaTypes:   slices.Clone(td.Attachments.MediaTypes),
				Released:     sqlutil.QuoteIdentifier(sqlutil.QueueTableName(queue)),
			}
		}
	}
	return refs, atts, nil
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
