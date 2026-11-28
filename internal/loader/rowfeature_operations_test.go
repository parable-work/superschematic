package loader

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/rowfeatureops"
	ir "github.com/parable-work/superschematic/ir"
)

// rowFeaturesAPIFixture is the API whose @rowFeatureOperations sets name
// fixture-references-db's Ticket and Note (#723), and rowFeaturesDBFixture
// its database.
const (
	rowFeaturesAPIFixture = "tsreader/testdata/services/fixture-references-api"
	rowFeaturesDBFixture  = "tsreader/testdata/services/fixture-references-db"
)

// loadRowFeaturesAPI loads fixture-references-api over its database.
func loadRowFeaturesAPI(t *testing.T) *ir.Schema {
	t.Helper()
	db, err := LoadService(filepath.FromSlash(rowFeaturesDBFixture))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := LoadService(filepath.FromSlash(rowFeaturesAPIFixture), WithDependencyLoader(func(string) (*ir.Schema, error) { return db, nil }))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	return schema
}

// TestRowFeatureOperationsExpand: the API loads with its
// @rowFeatureOperations sets filled from its database, operation by
// operation: the route, the arguments, the result, the clause with the
// operation's own permissions, the tool (the reads and the add of a
// reference) or a hidden one, the marker with the bucket on the operations
// that read it, and the Row types they share.
func TestRowFeatureOperationsExpand(t *testing.T) {
	schema := loadRowFeaturesAPI(t)
	sets := map[string]*ir.OperationSet{}
	for _, set := range schema.OperationSets {
		sets[set.Name] = set
	}
	const uuid = "Identity.UUID"
	type want struct {
		name, method, path string
		args               []string
		result             string
		tool               string
		bucket             string
		permissions        []string
	}
	ticket := []want{
		{"references", "GET", "ticket/{id}/references", []string{"id:" + uuid}, "RowReference[]", "ticket_references", "", nil},
		{"addReference", "POST", "ticket/{id}/references", []string{"id:" + uuid, "input:RowReferenceInput"}, "RowReference", "ticket_add_reference", "", nil},
		{"removeReference", "POST", "ticket/{id}/references/remove", []string{"id:" + uuid, "input:RowReferenceInput"}, "RowReferencesRemoved", "", "", nil},
		{"backlinks", "GET", "ticket/{id}/backlinks", []string{"id:" + uuid}, "RowBacklinks", "ticket_backlinks", "", nil},
		{"attachments", "GET", "ticket/{id}/files", []string{"id:" + uuid}, "RowAttachment[]", "ticket_attachments", "", nil},
		{"beginUpload", "POST", "ticket/{id}/files", []string{"id:" + uuid, "input:RowAttachmentBeginInput"}, "RowAttachmentBegun", "", "fixture-references-files", nil},
		{"completeUpload", "POST", "ticket/{id}/files/{file}/complete", []string{"id:" + uuid, "file:string"}, "RowAttachment", "", "fixture-references-files", nil},
		{"download", "GET", "ticket/{id}/files/{file}", []string{"id:" + uuid, "file:string"}, "RowAttachmentDownload", "", "fixture-references-files", nil},
		{"fileAttachment", "POST", "ticket/{id}/files/{file}/file", []string{"id:" + uuid, "file:string", "input:RowAttachmentFileInput"}, "RowAttachment", "", "", nil},
		{"removeAttachment", "DELETE", "ticket/{id}/files/{file}", []string{"id:" + uuid, "file:string"}, "boolean", "", "", nil},
	}
	check := func(set *ir.OperationSet, wants []want) {
		t.Helper()
		if set == nil {
			t.Fatal("no such set")
		}
		if len(set.Operations) != len(wants) {
			t.Fatalf("%s has %d operations, want %d: %s", set.Name, len(set.Operations), len(wants), marshal(t, set.Operations))
		}
		for i, w := range wants {
			op := set.Operations[i]
			where := set.Name + "." + w.name
			if op.Name != w.name || op.HTTPMethod != w.method || op.RestPath != w.path || op.Origin != ir.OriginRowFeatures || !op.Required || !op.Auth {
				t.Errorf("%s: %s %s %s, origin %q, required %v, auth %v", where, op.Name, op.HTTPMethod, op.RestPath, op.Origin, op.Required, op.Auth)
				continue
			}
			var args []string
			for _, a := range op.Arguments {
				args = append(args, a.Name+":"+a.TypeRef.Name)
			}
			if !slices.Equal(args, w.args) {
				t.Errorf("%s: arguments %v, want %v", where, args, w.args)
			}
			result := op.TypeRef.Name
			if op.TypeRef.IsArray {
				result += "[]"
			}
			if result != w.result {
				t.Errorf("%s: result %s, want %s", where, result, w.result)
			}
			switch {
			case w.tool == "" && (op.MCP == nil || !op.MCP.Hidden || op.MCP.HiddenReason == ""):
				t.Errorf("%s: MCP %s, want a hidden tool with its reason", where, marshal(t, op.MCP))
			case w.tool != "" && (op.MCP == nil || op.MCP.Hidden || op.MCP.Handle != w.tool):
				t.Errorf("%s: MCP %s, want the tool %s", where, marshal(t, op.MCP), w.tool)
			}
			marker := op.RowFeatureOperation
			if marker == nil || marker.Database != "fixture-references-db" || marker.Type != strings.TrimSuffix(set.Name, "Actions") || marker.Key != "id" || marker.Operation != w.name || marker.Bucket != w.bucket {
				t.Errorf("%s: marker %s", where, marshal(t, marker))
			}
			if !slices.Equal(op.Permissions, w.permissions) {
				t.Errorf("%s: permissions %v, want %v", where, op.Permissions, w.permissions)
			}
			if op.Docs == nil || op.Docs.Title == "" || (w.method == "GET") != (op.Docs.ReplayMode == ir.DocsReplayModeReadOnly) {
				t.Errorf("%s: docs %s", where, marshal(t, op.Docs))
			}
		}
	}
	check(sets["TicketActions"], ticket)
	var note []want
	for _, w := range ticket {
		if w.name == "removeAttachment" {
			continue
		}
		w.path = strings.Replace(w.path, "ticket/", "note/", 1)
		if w.tool != "" {
			w.tool = strings.Replace(w.tool, "ticket_", "note_", 1)
		}
		if w.name == "beginUpload" {
			w.permissions = []string{"notes.upload"}
		}
		note = append(note, w)
	}
	check(sets["NoteActions"], note)

	for _, name := range []string{
		rowfeatureops.RowRefType, rowfeatureops.ReferenceType, rowfeatureops.ReferenceInputType, rowfeatureops.RemovedType,
		rowfeatureops.BacklinkType, rowfeatureops.BacklinksType, rowfeatureops.AttachmentType, rowfeatureops.BeginInputType,
		rowfeatureops.UploadType, rowfeatureops.BegunType, rowfeatureops.DownloadType, rowfeatureops.FileInputType,
	} {
		if td := schema.Types[name]; td == nil || td.Origin != ir.OriginRowFeatures || td.Owner != schema.Name {
			t.Errorf("type %s = %s", name, marshal(t, td))
		}
	}
	for _, name := range []string{rowfeatureops.BacklinkViaEnum, rowfeatureops.AttachmentStateEnum, rowfeatureops.ProcessingEnum} {
		if ed := schema.Enums[name]; ed == nil || ed.Origin != ir.OriginRowFeatures {
			t.Errorf("enum %s = %s", name, marshal(t, ed))
		}
	}
	for _, name := range []string{"Generic.Int64", "Temporal.DateTime", uuid} {
		if schema.Scalars[name] == nil {
			t.Errorf("the API holds no scalar %s", name)
		}
	}
}
