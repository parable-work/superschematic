package migrateplan

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/sqlmigrate"
)

// developmentSources reads development mode's General documents of the
// engine examples (examples/engine-notes and examples/engine-jobs'
// schemas/dev), copied into testdata/development.
func developmentSources(t *testing.T, names ...string) []Source {
	t.Helper()
	out := make([]Source, len(names))
	for i, name := range names {
		path := filepath.Join("testdata", "development", name+".schema.json")
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		out[i] = Source{Name: path, Data: data}
	}
	return out
}

func columnNames(model *sqlmigrate.Model, table string) []string {
	for _, t := range model.Tables {
		if t.Name != table {
			continue
		}
		var names []string
		for _, column := range t.Columns {
			names = append(names, column.Name)
		}
		return names
	}
	return nil
}

// TestDevelopmentDocumentsPlanAsTheirTables: development mode's General
// documents plan as the tables format --to=ts writes them as (D117): each
// instance type a table of one DB service, keyed by id where it marks no
// key, its Workflow the field status with its columns, and its other
// behaviors' sub-tables beside it. The notes example is one service of its
// one name; the jobs example's three documents are of the service named.
func TestDevelopmentDocumentsPlanAsTheirTables(t *testing.T) {
	for _, dialect := range []sqlmigrate.Dialect{sqlmigrate.Postgres, sqlmigrate.SQLite} {
		schema, model, err := Documents(developmentSources(t, "notes"), dialect, Env{})
		require.NoError(t, err, dialect)
		assert.Equal(t, "notes", schema.Name)
		assert.Equal(t, "notes", model.Service)
		assert.Subset(t, tableNames(model), []string{"note", "note_comments_messages", "note_revisions_proposals"}, dialect)
		assert.Subset(t, columnNames(model, "note"), []string{"id", "title", "body", "tags", "status", "status_entered_at"}, dialect)
		assert.NotContains(t, columnNames(model, "note"), "workflow_status", dialect)

		_, model, err = Documents(developmentSources(t, "batches", "jobs", "workers"), dialect, Env{Service: "jobs-db"})
		require.NoError(t, err, dialect)
		assert.Equal(t, "jobs-db", model.Service)
		assert.Subset(t, tableNames(model), []string{"batch", "job", "worker", "job_dependencies_blockers", "job_retries_attempts"}, dialect)
		assert.Subset(t, columnNames(model, "job"), []string{"id", "title", "topic", "priority", "step", "batch_id", "status"}, dialect)
		assert.Subset(t, columnNames(model, "worker"), []string{"id", "subject", "status"}, dialect)

		plan, err := Plan(developmentSources(t, "notes"), nil, dialect, nil, Env{})
		require.NoError(t, err, dialect)
		assert.NotEmpty(t, plan.Steps, dialect)
	}
}

// TestDevelopmentDocumentsNameTheirService: General documents of several
// names lower into a service only when it is named, and a service named
// is held to the documents of a DB service.
func TestDevelopmentDocumentsNameTheirService(t *testing.T) {
	_, err := Load(developmentSources(t, "batches", "jobs", "workers"), sqlmigrate.Postgres, Env{})
	require.Error(t, err)
	assert.Equal(t, "the documents are development mode's General schemas Batch, Job and Worker, which lower to the tables of one DB service (D117): name the service (service, --service)", err.Error())

	_, err = Load(sources(orderDocument), sqlmigrate.Postgres, Env{Service: "crm-db"})
	require.Error(t, err)
	assert.Equal(t, "order.schema.json is a document of shop-db, and the plan is of crm-db: a plan from documents takes one DB service's documents", err.Error())

	schema, err := Load(sources(orderDocument), sqlmigrate.Postgres, Env{Service: "shop-db"})
	require.NoError(t, err)
	assert.Equal(t, "shop-db", schema.Name)
}

// TestDevelopmentDocumentsRefuseAStatusField: a General type with a field
// status and a Workflow on the type has no table form, so the plan is
// refused (decision 3).
func TestDevelopmentDocumentsRefuseAStatusField(t *testing.T) {
	doc := `{"name": "tickets", "kind": "General", "types": {"Ticket": {"name": "Ticket", "role": "EmbeddedStruct",
		"behaviors": [{"name": "Workflow", "config": {"states": ["open", "closed"], "transitions": [{"from": "open", "to": "closed"}]}}],
		"fields": [{"name": "status", "typeRef": {"name": "string"}}]}}}`
	_, err := Load(sources(doc), sqlmigrate.SQLite, Env{})
	require.Error(t, err)
	assert.Equal(t, "tickets: type Ticket has a field status, and its Workflow lowers to the field form status: Workflow<{ ... }>, which its declaration names its value field (D117): rename the field", err.Error())
}

// TestRequestNamesTheServiceGeneralDocumentsLowerInto: a plan request's
// service names the DB service, and its layout is the tables' (D117).
func TestRequestNamesTheServiceGeneralDocumentsLowerInto(t *testing.T) {
	var documents []json.RawMessage
	for _, source := range developmentSources(t, "batches", "jobs", "workers") {
		documents = append(documents, source.Data)
	}
	body, err := json.Marshal(map[string]any{"version": 1, "dialect": "sqlite", "documents": documents, "from": nil, "service": "jobs-db", "layout": true})
	require.NoError(t, err)
	req, err := ReadRequest(bytes.NewReader(body))
	require.NoError(t, err)
	response, err := Answer(req, Env{})
	require.NoError(t, err)
	var plan struct {
		Service string `json:"service"`
	}
	require.NoError(t, json.Unmarshal(response.Plan, &plan))
	assert.Equal(t, "jobs-db", plan.Service)
	var layout struct {
		Types map[string]struct {
			Uses []struct {
				ID       string `json:"id"`
				Behavior string `json:"behavior"`
			} `json:"uses"`
		} `json:"types"`
	}
	require.NoError(t, json.Unmarshal(response.Layout, &layout))
	require.Contains(t, layout.Types, "Job")
	var ids []string
	for _, use := range layout.Types["Job"].Uses {
		ids = append(ids, use.ID)
	}
	assert.Contains(t, ids, "status", "the Workflow is the use of the field status")
	assert.NotContains(t, ids, "Workflow")
	assert.Contains(t, ids, "Lease")
}
