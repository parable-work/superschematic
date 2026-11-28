package ormgen

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// Parties, the inbound path and the agent dispatcher in the Go ORM (D157,
// D158, D192): orchestration.go holds the definitions the HTTP runtime's
// inbound and agents packages run on, the conversations' and the agents'
// tables by part with their columns, the @party tables, each table's
// behaviors' uses, relations and versioning, and the @runRequests and
// @agents("requests") queues, and Orchestration, the orchestration.Host
// over the database. The host is the notifier's (notifications.go, D190)
// with the methods this file adds to it: a table's create and its
// behaviors' operations through its repository, the grants, the roles,
// the references, attachments and search across tables, and the two
// queues. It is written for a database that uses the notifier and
// declares a @party table, the conversations, a @runRequests queue or the
// @agents tables.

// OrchestrationModel is what orchestration.go is generated from.
type OrchestrationModel struct {
	// Definitions are the definitions as the JSON orchestration.MustParse
	// reads.
	Definitions string

	// RunRequests is the @runRequests queue's class and Requests the
	// @agents("requests") queue's, each the message's type in the types
	// package; "" for none.
	RunRequests, Requests string

	// Tables are the repositories a row is created through and the
	// behaviors' operations run on, by type, sorted.
	Tables []OrchestrationTable

	// Grants is a database with an @accessGrants table, whose grants the
	// host issues, revokes and reads; Roles its UserRole table's type.
	Grants bool
	Roles  string

	// CrossTables is a database with search across tables (crosstable.go).
	CrossTables bool
	// References and Attachments are a database with references and one
	// with attachments (rowrefs.go).
	References, Attachments bool
}

// OrchestrationTable is a repository as the orchestration writes it: its
// type, its create's input type, whether its rows are released (D139),
// which take no create, and the ids of its behaviors' uses.
type OrchestrationTable struct {
	Type        string
	CreateInput string
	Released    bool
	Uses        []string
	// KeyType is its key's Go type, and WireKey the expression of id, a
	// key of it, in its wire form.
	KeyType string
	WireKey string
}

type orchestrationRelationDef struct {
	Target string `json:"target"`
	Column string `json:"column"`
	Member string `json:"member"`
}

type orchestrationTableDef struct {
	Uses      map[string]string                   `json:"uses,omitempty"`
	Relations map[string]orchestrationRelationDef `json:"relations,omitempty"`
	Versioned bool                                `json:"versioned,omitempty"`
	Part      string                              `json:"part,omitempty"`
}

type orchestrationPartyDef struct {
	Title string `json:"title,omitempty"`
}

type orchestrationDefinitions struct {
	Parts       map[string]notifierPartDef       `json:"parts"`
	Parties     map[string]orchestrationPartyDef `json:"parties,omitempty"`
	Tables      map[string]orchestrationTableDef `json:"tables"`
	RunRequests string                           `json:"runRequests,omitempty"`
	Requests    string                           `json:"requests,omitempty"`
}

// orchestrationOf is the orchestration model of schema, nil for a
// database that declares no party, conversations, run requests or agents,
// or no notifier, whose host the orchestration's is.
func orchestrationOf(schema *ir.Schema, tables []sqlutil.TableModel, repositories []Repository, output *ORMOutput) (*OrchestrationModel, error) {
	if output.Notifications == nil || (!schema.UsesParties() && !schema.UsesAgents()) {
		return nil, nil
	}
	byType := make(map[string]*sqlutil.TableModel, len(tables))
	for i := range tables {
		byType[tables[i].Type] = &tables[i]
	}
	sqlite := output.SQLite
	defs := orchestrationDefinitions{Parts: map[string]notifierPartDef{}, Parties: map[string]orchestrationPartyDef{}, Tables: map[string]orchestrationTableDef{}}
	model := &OrchestrationModel{
		Grants:      output.Access != nil && output.Access.Grants != "",
		CrossTables: output.CrossTables != nil,
		References:  output.RowFeatures != nil && output.RowFeatures.References != "",
		Attachments: output.RowFeatures != nil && output.RowFeatures.Attachments != "",
	}
	if model.Grants {
		model.Roles = output.Access.Roles
	}
	for _, repo := range repositories {
		td := schema.Types[repo.TypeName]
		t := byType[repo.TypeName]
		if td == nil || t == nil {
			continue
		}
		def := orchestrationTableDef{Versioned: t.Versioned}
		table := OrchestrationTable{Type: repo.TypeName, CreateInput: repo.CreateInput(), Released: repo.Released, KeyType: repo.PrimaryKeyType, WireKey: repo.WireKey("id")}
		for _, use := range repo.Uses {
			if def.Uses == nil {
				def.Uses = map[string]string{}
			}
			def.Uses[use.Member] = use.ID
			table.Uses = append(table.Uses, use.ID)
		}
		sort.Strings(table.Uses)
		for _, rel := range t.ToOne {
			column := ""
			for _, c := range t.Columns {
				if c.Name == rel.Column {
					column = c.Field
				}
			}
			if def.Relations == nil {
				def.Relations = map[string]orchestrationRelationDef{}
			}
			def.Relations[rel.Field] = orchestrationRelationDef{Target: rel.Target, Column: column, Member: rel.Field}
		}
		part := ""
		switch {
		case td.Conversations != nil:
			part = td.Conversations.Part
		case td.Agents != nil && td.Agents.Part != ir.AgentRequests:
			part = td.Agents.Part
		}
		if part != "" {
			def.Part = part
			columns := map[string]notifierColumnDef{}
			for _, c := range t.Columns {
				if c.Field == "" {
					continue
				}
				columns[c.Field] = notifierColumnOf(c, sqlite)
			}
			if len(columns) == 0 {
				return nil, fmt.Errorf("ormgen: the %s part on %s: its table has no columns", part, td.Name)
			}
			defs.Parts[part] = notifierPartDef{Type: td.Name, Table: t.Quoted, Columns: columns}
		}
		if td.Party != nil {
			defs.Parties[td.Name] = orchestrationPartyDef{Title: td.Party.Title}
		}
		defs.Tables[repo.TypeName] = def
		model.Tables = append(model.Tables, table)
	}
	sort.Slice(model.Tables, func(i, j int) bool { return model.Tables[i].Type < model.Tables[j].Type })
	if q := schema.RunRequestsQueue(); q != nil {
		model.RunRequests = q.Name
		defs.RunRequests = q.Name
	}
	if q := schema.AgentsTable(ir.AgentRequests); q != nil {
		model.Requests = q.Name
		defs.Requests = q.Name
	}
	data, err := json.Marshal(defs)
	if err != nil {
		return nil, err
	}
	model.Definitions = string(data)
	return model, nil
}

// writeOrchestration writes orchestration.go for a database with parties
// or agents; a database without has none, so a file an earlier build wrote
// goes.
func writeOrchestration(generator *codegen.FileGenerator, outputDir string, output *ORMOutput) error {
	file := filepath.Join(outputDir, "orchestration.go")
	if output.Orchestration != nil {
		if err := generateFile(generator, "orchestration.tmpl", file, output); err != nil {
			return fmt.Errorf("failed to write orchestration.go: %w", err)
		}
		return nil
	}
	if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove orchestration.go: %w", err)
	}
	return nil
}
