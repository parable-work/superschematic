package loader

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/writer"
	ir "github.com/parable-work/superschematic/ir"
)

// TestReferencesResolveInTheLoadedFixture: fixture-refs-api's references
// resolve to listItems, by id and labeled by ItemView's titleField, with
// ids as the lookup, and the gift's to the members it names (D61).
func TestReferencesResolveInTheLoadedFixture(t *testing.T) {
	schema, err := LoadService(filepath.Join("tsreader", "testdata", "services", "fixture-refs-api"))
	if err != nil {
		t.Fatal(err)
	}
	byHolder := map[string]ir.HeldReference{}
	for _, held := range ir.ReferencesOf(schema) {
		byHolder[held.Holder] = held
	}
	for _, tc := range []struct {
		holder, options, value, label, lookup string
	}{
		{"BasketLine.itemId", "listItems", "id", "name", "ids"},
		{"BasketInput.giftSku", "listItems", "sku", "name", ""},
		{"Catalog.listItems(ids)", "listItems", "id", "name", "ids"},
		{"Catalog.getItem(itemId)", "listItems", "id", "name", "ids"},
	} {
		held, ok := byHolder[tc.holder]
		if !ok {
			t.Errorf("%s holds no reference", tc.holder)
			continue
		}
		got, err := ir.ResolveReference(schema, nil, held.Ref)
		if err != nil {
			t.Errorf("%s: %v", tc.holder, err)
			continue
		}
		lookup := ""
		if got.Lookup != nil {
			lookup = got.Lookup.Name
		}
		if got.Options.Name != tc.options || got.OptionsSet.Name != "Catalog" || got.Value.Name != tc.value || got.Label.Name != tc.label || lookup != tc.lookup {
			t.Errorf("%s resolves to options %s, value %s, label %s, lookup %q", tc.holder, got.Options.Name, got.Value.Name, got.Label.Name, lookup)
		}
	}
	if len(byHolder) != 4 {
		t.Errorf("references = %v", byHolder)
	}
}

// TestReferenceToADependencysType: the loader reads the dependency whose
// type a reference names, beside the service, a General service or another
// API (D66), and the check resolves it there; without the import the target
// is no type of the service.
func TestReferenceToADependencysType(t *testing.T) {
	for _, kind := range []string{"General", "API"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			write := func(rel, contents string) {
				t.Helper()
				path := filepath.Join(root, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			write("shelf-types/schema.config.json", `{"name": "shelf-types", "kind": "`+kind+`", "outputs": {}}`)
			write("shelf-types/src/shelf.schema.json", `{"types": {"ShelfView": {"name": "ShelfView", "role": "EmbeddedStruct",
				"display": {"titleField": "title"},
				"fields": [{"name": "id", "typeRef": {"name": "string"}, "required": true}, {"name": "title", "typeRef": {"name": "string"}, "required": true}]}}}`)
			write("shop-api/schema.config.json", `{"name": "shop-api", "kind": "API", "dependencies": [{"name": "shelf-types", "kind": "`+kind+`"}], "outputs": {}}`)
			api := `{"imports": [{"package": "@schemas/shelf-types", "types": ["ShelfView"]}],
				"types": {"PlaceInput": {"name": "PlaceInput", "role": "APIInput", "fields": [
					{"name": "shelfId", "typeRef": {"name": "string"}, "required": true, "references": {"type": "ShelfView", "value": "VALUE"}}]}},
				"operationSets": [{"name": "Shelves", "operations": [
					{"name": "listShelves", "httpMethod": "GET", "restPath": "shelves", "typeRef": {"name": "ShelfView", "isArray": true}, "required": true}]}]}`
			write("shop-api/src/shop.schema.json", strings.Replace(api, "VALUE", "id", 1))
			if _, err := LoadService(filepath.Join(root, "shop-api")); err != nil {
				t.Fatalf("LoadService: %v", err)
			}

			write("shop-api/src/shop.schema.json", strings.Replace(api, "VALUE", "code", 1))
			_, err := LoadService(filepath.Join(root, "shop-api"))
			if err == nil || !strings.Contains(err.Error(), "src/shop.schema.json: PlaceInput.shelfId: value code is not a field of ShelfView (fields: id, title)") {
				t.Fatalf("a value the dependency's type lacks: %v", err)
			}
		})
	}
}

// TestAPIsReferringToEachOtherAreRefused: two APIs whose references name
// each other's types, which the loader reads (D66), are refused as a
// cycle, not read without end (D61).
func TestAPIsReferringToEachOtherAreRefused(t *testing.T) {
	root := t.TempDir()
	write := func(rel, contents string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	api := func(self, other, own, theirs string) {
		write(self+"/schema.config.json", `{"name": "`+self+`", "kind": "API", "dependencies": [{"name": "`+other+`", "kind": "API"}], "outputs": {}}`)
		write(self+"/src/api.schema.json", `{"imports": [{"package": "@schemas/`+other+`", "types": ["`+theirs+`"]}],
		"types": {"`+own+`": {"name": "`+own+`", "role": "EmbeddedStruct", "fields": [
			{"name": "id", "typeRef": {"name": "string"}, "required": true},
			{"name": "otherId", "typeRef": {"name": "string"}, "required": true, "references": {"type": "`+theirs+`"}}]}},
		"operationSets": [{"name": "Items", "operations": [
			{"name": "listOwn", "httpMethod": "GET", "restPath": "own", "typeRef": {"name": "`+own+`", "isArray": true}, "required": true},
			{"name": "listTheirs", "httpMethod": "GET", "restPath": "theirs", "typeRef": {"name": "`+theirs+`", "isArray": true}, "required": true}]}]}`)
	}
	api("a-api", "b-api", "AView", "BView")
	api("b-api", "a-api", "BView", "AView")
	_, err := LoadService(filepath.Join(root, "a-api"))
	if err == nil || !strings.Contains(err.Error(), "a-api is already being read for its references (a-api -> b-api -> a-api), a cycle") {
		t.Fatalf("two APIs referring to each other: %v", err)
	}
}

// TestLoadServiceRefusesReferencesAndSearchesInTheDataForms: the verify
// pass holds the data forms to the rules the TypeScript form meets.
func TestLoadServiceRefusesReferencesAndSearchesInTheDataForms(t *testing.T) {
	for name, tc := range map[string]struct {
		config, schema, want string
	}{
		"a reference outside an API": {
			config: `{"name": "notes", "kind": "General", "outputs": {}}`,
			schema: `{"types": {"Note": {"name": "Note", "role": "EmbeddedStruct", "fields": [
				{"name": "id", "typeRef": {"name": "string"}, "required": true},
				{"name": "parentId", "typeRef": {"name": "string"}, "references": {"type": "Note"}}]}}}`,
			want: "src/notes.schema.json: Note.parentId: RefersTo is an API's; notes is a General schema",
		},
		"a search on a POST": {
			config: `{"name": "notes", "kind": "API", "outputs": {}}`,
			schema: `{"operationSets": [{"name": "Notes", "operations": [
				{"name": "find", "httpMethod": "POST", "restPath": "notes/find", "typeRef": {"name": "string", "isArray": true}, "required": true,
				 "arguments": [{"name": "q", "typeRef": {"name": "string"}, "isQuery": true, "search": true}]}]}]}`,
			want: "Notes.find: search argument q must be a query parameter of a GET operation",
		},
		"a reference no operation lists": {
			config: `{"name": "notes", "kind": "API", "outputs": {}}`,
			schema: `{"types": {"NoteView": {"name": "NoteView", "role": "EmbeddedStruct", "fields": [{"name": "id", "typeRef": {"name": "string"}, "required": true}]}},
				"operationSets": [{"name": "Notes", "operations": [
				{"name": "getNote", "httpMethod": "GET", "restPath": "notes/{noteId}", "typeRef": {"name": "NoteView"}, "required": true,
				 "arguments": [{"name": "noteId", "typeRef": {"name": "string"}, "required": true, "references": {"type": "NoteView"}}]}]}]}`,
			want: "Notes.getNote(noteId): no operation of notes lists NoteView (a GET returning NoteView[] with no path argument and no required one, that an end user may call); declare one or name it with options",
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := writeService(t, map[string]string{"schema.config.json": tc.config, "src/notes.schema.json": tc.schema})
			_, err := LoadService(dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadService = %v, want %q", err, tc.want)
			}
		})
	}
}

var refsKeysFixture = filepath.Join("tsreader", "testdata", "services", "fixture-refs-keys-api")

// copyRefsKeys copies fixture-refs-keys-api next to itself, so its tsconfig
// paths still resolve, applies the replacements to its schema file, and
// returns the copy's directory.
func copyRefsKeys(t *testing.T, replacements ...string) string {
	t.Helper()
	dir, err := os.MkdirTemp(filepath.Dir(refsKeysFixture), "fixture-refs-keys-api-loader-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.CopyFS(dir, os.DirFS(refsKeysFixture)); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(dir, "src", "directory.schema.ts")
	source, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for i := 0; i+1 < len(replacements); i += 2 {
		if !strings.Contains(text, replacements[i]) {
			t.Fatalf("fixture has no %q to replace", replacements[i])
		}
		text = strings.Replace(text, replacements[i], replacements[i+1], 1)
	}
	if err := os.WriteFile(filename, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestADerivedReferenceRoundTripsWithoutRefersTo: fixture-refs-keys-api's
// TenantUserView.tenantId reads the key of TenantUser.tenant with no
// @virtual, and refers to TenantView, which nothing writes: the IR holds
// no reference, and ir.DerivedReferences derives it (D61, amended). format
// writes the field as the plain value it is, from TypeScript to JSON and
// YAML and from JSON back to TypeScript, and each form read back derives
// the same reference.
func TestADerivedReferenceRoundTripsWithoutRefersTo(t *testing.T) {
	schema, err := LoadService(refsKeysFixture)
	if err != nil {
		t.Fatal(err)
	}
	db, err := LoadService(filepath.Join("tsreader", "testdata", "services", "fixture-db"))
	if err != nil {
		t.Fatal(err)
	}
	deps := map[string]*ir.Schema{"fixture-db": db}
	if written := ir.ReferencesOf(schema); len(written) != 0 {
		t.Fatalf("written references = %+v, want none", written)
	}
	if virtual := schema.Types["TenantUserView"].Source.Virtual; len(virtual) != 0 {
		t.Fatalf("virtual = %v, want none: a key needs no @virtual", virtual)
	}
	derived := func(schema *ir.Schema) []string {
		t.Helper()
		var out []string
		for _, held := range ir.DerivedReferences(schema, deps) {
			resolved, err := ir.ResolveReference(schema, deps, held.Ref)
			if err != nil {
				t.Fatalf("%s: %v", held.Holder, err)
			}
			out = append(out, held.Holder+" -> "+held.Ref.Type+" by "+resolved.Value.Name+", options "+resolved.Options.Name+", label "+resolved.Label.Name)
		}
		return out
	}
	want := []string{"TenantUserView.tenantId -> TenantView by id, options listTenants, label name"}
	if got := derived(schema); !reflect.DeepEqual(got, want) {
		t.Fatalf("derived = %q, want %q", got, want)
	}
	types, _ := json.Marshal(schema.Types)

	// write formats the schema and fails on any reference it writes.
	write := func(schema *ir.Schema, format writer.Format, dir string) string {
		t.Helper()
		files, err := writer.WriteService(schema, format, dir)
		if err != nil {
			t.Fatalf("WriteService(%s): %v", format, err)
		}
		if len(files) != 1 {
			t.Fatalf("WriteService(%s) wrote %v", format, files)
		}
		data, err := os.ReadFile(filepath.Join(dir, files[0]))
		if err != nil {
			t.Fatal(err)
		}
		for _, refersTo := range []string{"RefersTo", "references", "virtual"} {
			if bytes.Contains(data, []byte(refersTo)) {
				t.Errorf("%s output writes %s:\n%s", format, refersTo, data)
			}
		}
		return string(data)
	}
	config := `{"name": "fixture-refs-keys-api", "kind": "API", "dependencies": [{"name": "fixture-db", "kind": "DB"}], "outputs": {}}`
	var fromJSON *ir.Schema
	for _, format := range []writer.Format{writer.FormatJSON, writer.FormatYAML} {
		dir := writeService(t, map[string]string{"schema.config.json": config})
		write(schema, format, dir)
		reloaded, err := LoadService(dir)
		if err != nil {
			t.Fatalf("reloading the %s form: %v", format, err)
		}
		if got := derived(reloaded); !reflect.DeepEqual(got, want) {
			t.Fatalf("derived from the %s form = %q, want %q", format, got, want)
		}
		if format == writer.FormatJSON {
			fromJSON = reloaded
		}
	}

	// The TypeScript format writes from the JSON form, read back beside
	// the fixture so its paths resolve.
	copied := copyRefsKeys(t)
	if err := os.Remove(filepath.Join(copied, "src", "directory.schema.ts")); err != nil {
		t.Fatal(err)
	}
	if ts := write(fromJSON, writer.FormatTS, copied); !strings.Contains(ts, "\n  tenantId: Identity.UUID;\n") {
		t.Fatalf("the TypeScript form does not write tenantId as a plain value:\n%s", ts)
	}
	reloaded, err := LoadService(copied)
	if err != nil {
		t.Fatalf("reloading the TypeScript form: %v", err)
	}
	if got := derived(reloaded); !reflect.DeepEqual(got, want) {
		t.Fatalf("derived from the TypeScript form = %q, want %q", got, want)
	}
	if again, _ := json.Marshal(reloaded.Types); !bytes.Equal(again, types) {
		t.Fatalf("types changed from TypeScript to JSON and back\nwant %s\ngot  %s", types, again)
	}
}

// TestARelationsKeyIsCheckedInTheTypeScriptForm: the reader flattens the
// type a source's Relation names beside the source, so the key's type is
// held to the related type's id even when no type of the API is @source of
// it; and two types that are @source of it draw the loader's warning.
func TestARelationsKeyIsCheckedInTheTypeScriptForm(t *testing.T) {
	_, err := LoadService(copyRefsKeys(t, "@source(Tenant)\n", "", "tenantId: Identity.UUID;", "tenantId: string;"))
	if err == nil || !strings.Contains(err.Error(), "TenantUserView.tenantId reads the key of fixture-db.TenantUser.tenant, a Relation to Tenant, whose id is Identity.UUID, not string") {
		t.Fatalf("a key of another type: %v", err)
	}

	var warnings bytes.Buffer
	prev := WarningWriter
	WarningWriter = &warnings
	defer func() { WarningWriter = prev }()
	if _, err := LoadService(copyRefsKeys(t, "export class Directory {", "@source(Tenant)\nexport abstract class TenantCard {\n  id: Identity.UUID;\n  name: Identity.Name;\n}\n\nexport class Directory {")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warnings.String(), "TenantUserView.tenantId reads the key of fixture-db.TenantUser.tenant and refers to nothing: TenantCard and TenantView are both @source(fixture-db.Tenant); name one with RefersTo") {
		t.Fatalf("warnings = %q", warnings.String())
	}
}
