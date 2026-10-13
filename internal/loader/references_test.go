package loader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
// type a reference names, beside the service, and the check resolves it
// there; without the import the target is no type of the service.
func TestReferenceToADependencysType(t *testing.T) {
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
	write("shelf-types/schema.config.json", `{"name": "shelf-types", "kind": "General", "outputs": {}}`)
	write("shelf-types/src/shelf.schema.json", `{"types": {"ShelfView": {"name": "ShelfView", "role": "EmbeddedStruct",
		"display": {"titleField": "title"},
		"fields": [{"name": "id", "typeRef": {"name": "string"}, "required": true}, {"name": "title", "typeRef": {"name": "string"}, "required": true}]}}}`)
	write("shop-api/schema.config.json", `{"name": "shop-api", "kind": "API", "dependencies": [{"name": "shelf-types", "kind": "General"}], "outputs": {}}`)
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
