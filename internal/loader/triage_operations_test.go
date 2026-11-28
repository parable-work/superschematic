package loader

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// triageAPIFixture is the API whose @triageOperations set serves the
// inbound path's triage of fixture-parties-db (#746), and triageDBFixture
// its database.
const (
	triageAPIFixture = "tsreader/testdata/services/fixture-triage-api"
	triageDBFixture  = "tsreader/testdata/services/fixture-parties-db"
)

// loadTriageAPI loads the API at dir over fixture-parties-db.
func loadTriageAPI(t *testing.T, dir string) (*ir.Schema, error) {
	t.Helper()
	db, err := LoadService(filepath.FromSlash(triageDBFixture))
	if err != nil {
		t.Fatal(err)
	}
	return LoadService(dir, WithDependencyLoader(func(string) (*ir.Schema, error) { return db, nil }))
}

// TestTriageOperationsExpand: the API loads with its @triageOperations set
// filled from its database, operation by operation: the route, the
// arguments, the page of the list, the result, the class's permission, the
// tool (the two reads) or a hidden one, the marker, and the Triage types,
// the settle's party referring to the API's ContactView.
func TestTriageOperationsExpand(t *testing.T) {
	schema, err := loadTriageAPI(t, filepath.FromSlash(triageAPIFixture))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	var set *ir.OperationSet
	for _, s := range schema.OperationSets {
		if s.Name == "Triage" {
			set = s
		}
	}
	if set == nil || !set.IsTriageOperations() {
		t.Fatalf("no @triageOperations set Triage: %s", marshal(t, schema.OperationSets))
	}
	const uuid = "Identity.UUID"
	type want struct {
		name, method, path string
		args               []string
		result             string
		tool               string
	}
	wants := []want{
		{"triage", "GET", "triage", []string{"channel:string"}, "TriageMessage[]", "triage"},
		{"triageMessage", "GET", "triage/{message}", []string{"message:" + uuid}, "TriageMessage", "triage_message"},
		{"settleOnto", "POST", "triage/{message}/settle", []string{"message:" + uuid, "input:TriageSettleInput"}, "TriageSettled", ""},
		{"dismiss", "POST", "triage/{message}/dismiss", []string{"message:" + uuid}, "TriageMessage", ""},
	}
	if len(set.Operations) != len(wants) {
		t.Fatalf("Triage has %d operations, want %d: %s", len(set.Operations), len(wants), marshal(t, set.Operations))
	}
	for i, w := range wants {
		op := set.Operations[i]
		if op.Name != w.name || op.HTTPMethod != w.method || op.RestPath != w.path || op.Origin != ir.OriginTriage || !op.Required {
			t.Errorf("%s: %s %s %s, origin %q, required %v", w.name, op.Name, op.HTTPMethod, op.RestPath, op.Origin, op.Required)
			continue
		}
		var args []string
		for _, a := range op.Arguments {
			args = append(args, a.Name+":"+a.TypeRef.Name)
		}
		if !slices.Equal(args, w.args) {
			t.Errorf("%s: arguments %v, want %v", w.name, args, w.args)
		}
		result := op.TypeRef.Name
		if op.TypeRef.IsArray {
			result += "[]"
		}
		if result != w.result {
			t.Errorf("%s: result %s, want %s", w.name, result, w.result)
		}
		if !slices.Equal(op.Permissions, []string{"parties.triage"}) {
			t.Errorf("%s: permissions %v, want the class's", w.name, op.Permissions)
		}
		switch {
		case w.tool == "" && (op.MCP == nil || !op.MCP.Hidden || op.MCP.HiddenReason == ""):
			t.Errorf("%s: MCP %s, want a hidden tool with its reason", w.name, marshal(t, op.MCP))
		case w.tool != "" && (op.MCP == nil || op.MCP.Hidden || op.MCP.Handle != w.tool):
			t.Errorf("%s: MCP %s, want the tool %s", w.name, marshal(t, op.MCP), w.tool)
		}
		marker := op.TriageOperation
		if marker == nil || marker.Database != "fixture-parties-db" || marker.Messages != "Message" || marker.Party != "Contact" || !marker.Attachments || marker.Operation != w.name {
			t.Errorf("%s: marker %s", w.name, marshal(t, marker))
		}
		if op.Docs == nil || op.Docs.Title == "" || (w.method == "GET") != (op.Docs.ReplayMode == ir.DocsReplayModeReadOnly) {
			t.Errorf("%s: docs %s", w.name, marshal(t, op.Docs))
		}
	}
	if page := set.Operations[0].Page; page == nil || page.DefaultLimit != ir.TriageDefaultLimit || page.MaxLimit != ir.TriageMaxLimit {
		t.Errorf("triage's page = %+v", page)
	}
	if channel := set.Operations[0].Arguments[0]; !channel.IsQuery || channel.Required {
		t.Errorf("triage's channel = %+v, want an optional query argument", channel)
	}
	for _, name := range []string{"TriageMessage", "TriageAttachment", "TriageSettleInput", "TriageSettled"} {
		if td := schema.Types[name]; td == nil || td.Origin != ir.OriginTriage || td.Owner != schema.Name {
			t.Errorf("type %s = %s", name, marshal(t, td))
		}
	}
	for _, name := range []string{"TriageState", "TriageTrust", "TriageOutcome"} {
		if ed := schema.Enums[name]; ed == nil || ed.Origin != ir.OriginTriage {
			t.Errorf("enum %s = %s", name, marshal(t, ed))
		}
	}
	if d := schema.Types["TriageMessage"].Display; d == nil || d.Plural != "Held messages" || d.TitleField != "fromAddress" {
		t.Errorf("TriageMessage's display = %+v", d)
	}
	var party *ir.FieldDef
	for _, fd := range schema.Types["TriageSettleInput"].Fields {
		if fd.Name == "party" {
			party = fd
		}
	}
	if party == nil || party.References == nil || party.References.Type != "ContactView" {
		t.Errorf("TriageSettleInput.party = %s, want a reference to ContactView", marshal(t, party))
	}
}

// TestTriageOperationsRefusals: the checks the loader runs before it
// fills a @triageOperations set refuse what the expansion could not serve:
// an authored operation, a public set, a second set, a table that is no
// party, a refersTo of no type of the API, a name the API takes and a
// route another operation takes.
func TestTriageOperationsRefusals(t *testing.T) {
	db, err := LoadService(filepath.FromSlash(triageDBFixture))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(api *ir.Schema, set *ir.OperationSet)
		want   string
	}{
		{"an authored operation", func(_ *ir.Schema, set *ir.OperationSet) {
			set.Operations = []*ir.FieldDef{{Name: "extra", TypeRef: ir.TypeRef{Name: "string"}}}
		}, "Triage: a @triageOperations class has no members"},
		{"a public set", func(_ *ir.Schema, set *ir.OperationSet) {
			set.Permissions, set.Public = nil, true
		}, "Triage: @publicRoute on a @triageOperations class would open the inbound path's triage to anyone"},
		{"a second set", func(api *ir.Schema, set *ir.OperationSet) {
			again := *set
			again.Name = "MoreTriage"
			api.OperationSets = append(api.OperationSets, &again)
		}, "MoreTriage: @triageOperations appears on Triage too: an API serves its triage from one set"},
		{"a table that is no party", func(api *ir.Schema, set *ir.OperationSet) {
			for i := range api.Imports {
				if strings.HasSuffix(api.Imports[i].Package, "fixture-parties-db") {
					api.Imports[i].Types = append(api.Imports[i].Types, "Job")
				}
			}
			set.TriageOperations.Party = "Job"
		}, "Triage: @triageOperations names Job, which is no @party table of fixture-parties-db"},
		{"a party the API does not import", func(_ *ir.Schema, set *ir.OperationSet) {
			set.TriageOperations.Party = "Elsewhere"
		}, "Triage: @triageOperations names Elsewhere, which fixture-triage-api does not import"},
		{"a refersTo of no type", func(_ *ir.Schema, set *ir.OperationSet) {
			set.TriageOperations.RefersTo = "Missing"
		}, "Triage: @triageOperations' refersTo names Missing, which is no type of fixture-triage-api"},
		{"a name the API takes", func(api *ir.Schema, _ *ir.OperationSet) {
			api.Types["TriageMessage"] = &ir.TypeDef{Name: "TriageMessage", Role: ir.RoleEmbeddedStruct}
		}, "Triage: @triageOperations adds the type TriageMessage, and fixture-triage-api already declares or imports one of that name"},
		{"a route another operation takes", func(api *ir.Schema, _ *ir.OperationSet) {
			for _, other := range api.OperationSets {
				if other.Name == "ContactQueries" {
					other.Operations = append(other.Operations, &ir.FieldDef{Name: "heldElsewhere", HTTPMethod: "GET", RestPath: "triage", Required: true, TypeRef: ir.TypeRef{Name: "ContactView", IsArray: true}})
				}
			}
		}, "Triage: the operation triage it adds is routed at GET /api/triage, which ContactQueries.heldElsewhere's GET /api/triage also matches"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, err := loadTriageAPI(t, filepath.FromSlash(triageAPIFixture))
			if err != nil {
				t.Fatal(err)
			}
			var set *ir.OperationSet
			for _, each := range api.OperationSets {
				if each.IsTriageOperations() {
					each.Operations = nil
					set = each
				}
			}
			for name, td := range api.Types {
				if td.Origin == ir.OriginTriage {
					delete(api.Types, name)
				}
			}
			tc.change(api, set)
			result := verify.CheckTriageOperations(api, verify.Input{Database: db})
			var got []string
			for _, d := range result.Errors {
				got = append(got, d.Msg)
			}
			found := false
			for _, message := range got {
				found = found || strings.Contains(message, tc.want)
			}
			if !found {
				t.Errorf("CheckTriageOperations = %q, want %q among them", got, tc.want)
			}
		})
	}
}
