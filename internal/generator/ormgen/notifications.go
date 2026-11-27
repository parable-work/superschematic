package ormgen

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// The notifier in the Go ORM (D153, D190): notifications.go holds the
// definitions the HTTP runtime's notify package runs on, the notifier's
// tables by part with their columns and every table's key, events and
// to-one relations, and Notifications, the notifications.Host over the
// database: its pool or a transaction, its dispatch queue, its access
// predicate (D180) and each table's repository, which a recipient reads a
// record through. It is written for a database that uses the notifier
// (ir.Schema.UsesNotifications) and declares its dispatch queue.

// NotificationsModel is what notifications.go is generated from.
type NotificationsModel struct {
	// Definitions are the definitions as the JSON notifications.MustParse
	// reads.
	Definitions string

	// Dispatch is the @notifier("dispatch") queue's class, the message's
	// type in the types package.
	Dispatch string

	// Tables are the repositories a record is read, found and updated
	// through, by type, sorted.
	Tables []NotificationsTable

	// Roles is the UserRole table's type and Grants the UserRoleGrant
	// table's, whose rows a user's permissions and a role's holders are
	// read from; each empty without one.
	Roles, Grants string
}

// NotificationsTable is a repository as the notifier reads it: its type,
// its key's Go type, and whether its rows are released (D139), which have
// no update.
type NotificationsTable struct {
	Type     string
	KeyType  string
	Released bool
}

// notifierPartDef is a notifier part's JSON (notifications.Part).
type notifierPartDef struct {
	Type    string                       `json:"type"`
	Table   string                       `json:"table"`
	Columns map[string]notifierColumnDef `json:"columns"`
}

type notifierColumnDef struct {
	Name string `json:"name"`
	UUID bool   `json:"uuid,omitempty"`
}

type notifierEventDef struct {
	Kind    string   `json:"kind"`
	Payload []string `json:"payload,omitempty"`
	Title   string   `json:"title,omitempty"`
	Urgency string   `json:"urgency,omitempty"`
}

type notifierRelationDef struct {
	Target string `json:"target"`
	Member string `json:"member"`
}

type notifierTableDef struct {
	Key       string                         `json:"key"`
	Events    []notifierEventDef             `json:"events,omitempty"`
	Relations map[string]notifierRelationDef `json:"relations,omitempty"`
	Part      string                         `json:"part,omitempty"`
}

type notifierDefinitions struct {
	Parts  map[string]notifierPartDef  `json:"parts"`
	Tables map[string]notifierTableDef `json:"tables"`
}

// notificationsOf is the notifier model of schema, nil for a database
// that does not use the notifier.
func notificationsOf(schema *ir.Schema, tables []sqlutil.TableModel, repositories []Repository) (*NotificationsModel, error) {
	if !schema.UsesNotifications() {
		return nil, nil
	}
	dispatch := schema.NotifierTable(ir.NotifierDispatch)
	if dispatch == nil {
		// The loader refuses a notifier with no dispatch queue; a schema
		// with an @event alone has no notifier to host.
		return nil, nil
	}
	byType := make(map[string]*sqlutil.TableModel, len(tables))
	for i := range tables {
		byType[tables[i].Type] = &tables[i]
	}
	defs := notifierDefinitions{Parts: map[string]notifierPartDef{}, Tables: map[string]notifierTableDef{}}
	model := &NotificationsModel{Dispatch: dispatch.Name}
	for _, repo := range repositories {
		td := schema.Types[repo.TypeName]
		t := byType[repo.TypeName]
		if td == nil || t == nil {
			continue
		}
		def := notifierTableDef{Key: repo.PrimaryKeyField}
		for _, event := range td.Events {
			if event != nil {
				def.Events = append(def.Events, notifierEventDef{Kind: event.Kind, Payload: event.Payload, Title: event.Title, Urgency: event.Urgency})
			}
		}
		for _, rel := range t.ToOne {
			if def.Relations == nil {
				def.Relations = map[string]notifierRelationDef{}
			}
			def.Relations[rel.Field] = notifierRelationDef{Target: rel.Target, Member: rel.Field}
		}
		part := ""
		switch {
		case td.ChannelIdentities != nil:
			part = "channelIdentities"
		case td.Notifier != nil:
			part = td.Notifier.Part
		}
		if part != "" {
			def.Part = part
			columns := map[string]notifierColumnDef{}
			for _, c := range t.Columns {
				if c.Field == "" {
					continue
				}
				columns[c.Field] = notifierColumnDef{Name: c.Quoted, UUID: strings.EqualFold(c.Type, "uuid")}
			}
			if len(columns) == 0 {
				return nil, fmt.Errorf("ormgen: @notifier(%q) on %s: its table has no columns", part, td.Name)
			}
			defs.Parts[part] = notifierPartDef{Type: td.Name, Table: t.Quoted, Columns: columns}
		}
		defs.Tables[repo.TypeName] = def
		model.Tables = append(model.Tables, NotificationsTable{Type: repo.TypeName, KeyType: repo.PrimaryKeyType, Released: repo.Released})
	}
	sort.Slice(model.Tables, func(i, j int) bool { return model.Tables[i].Type < model.Tables[j].Type })
	if role := schema.UserRoleTable(); role != nil {
		if _, ok := defs.Tables[role.Name]; ok {
			model.Roles = role.Name
		}
	}
	if _, ok := defs.Tables[ir.IdentityRoleGrantTable]; ok && model.Roles != "" {
		model.Grants = ir.IdentityRoleGrantTable
	}
	data, err := json.Marshal(defs)
	if err != nil {
		return nil, err
	}
	model.Definitions = string(data)
	return model, nil
}

// writeNotifications writes notifications.go for a database that uses the
// notifier; a database that does not has none, so a file an earlier build
// wrote goes.
func writeNotifications(generator *codegen.FileGenerator, outputDir string, output *ORMOutput) error {
	file := filepath.Join(outputDir, "notifications.go")
	if output.Notifications != nil {
		if err := generateFile(generator, "notifications.tmpl", file, output); err != nil {
			return fmt.Errorf("failed to write notifications.go: %w", err)
		}
		return nil
	}
	if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove notifications.go: %w", err)
	}
	return nil
}
