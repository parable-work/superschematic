package topcoat

import (
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// shapes is a schema of records, and an operation builder, for the
// resource classification's test.
type shapes struct {
	schema *ir.Schema
	ops    []declared
	forms  pageForms
}

func newShapes() *shapes {
	schema := ir.NewSchema("fixture", ir.SchemaKindAPI)
	for _, name := range []string{"Thing", "Part", "Book", "Gadget", "Viewer", "Summary"} {
		schema.Types[name] = &ir.TypeDef{Name: name, Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		}}
	}
	return &shapes{schema: schema, forms: pageForms{args: map[string]*argForm{}, inputs: map[string]*form{}}}
}

// op adds an operation: its method and path, its result, its path
// arguments (each a string), a query or body argument named by extra, an
// input type, and whether it has an argument form.
type opShape struct {
	method, path string
	result       ir.TypeRef
	extra        string
	query        bool
	input        string
	argForm      bool
	service      bool
}

func (s *shapes) add(name string, shape opShape) {
	e := registry.RustEndpoint{
		Namespace: "thing",
		Name:      name,
		Method:    strings.ToLower(shape.method),
		Path:      "/api/" + shape.path,
		ArgsName:  "Thing" + strings.ToUpper(name[:1]) + name[1:] + "Args",
	}
	op := &ir.FieldDef{Name: name, TypeRef: shape.result, HTTPMethod: shape.method, RestPath: shape.path}
	for _, part := range strings.Split(shape.path, "/") {
		if strings.HasPrefix(part, "{") {
			param := strings.Trim(part, "{}")
			e.PathArgs = append(e.PathArgs, registry.RustParam{Name: param, Field: param})
			op.Arguments = append(op.Arguments, &ir.ArgumentDef{Name: param, TypeRef: ir.TypeRef{Name: "string"}, Required: true})
		}
	}
	if shape.extra != "" {
		param := registry.RustParam{Name: shape.extra, Field: shape.extra}
		if shape.query {
			e.QueryArgs = append(e.QueryArgs, param)
		} else {
			e.BodyArgs = append(e.BodyArgs, param)
		}
		op.Arguments = append(op.Arguments, &ir.ArgumentDef{Name: shape.extra, TypeRef: ir.TypeRef{Name: "string"}})
	}
	if shape.input != "" {
		e.Input = &registry.RustInput{RustType: "types::" + shape.input, Required: true}
		s.forms.inputs[shape.input] = &form{Name: shape.input + "Form", TypeName: shape.input}
	}
	if shape.argForm {
		s.forms.args[e.ArgsName] = &argForm{Name: e.ArgsName + "Form", ArgsName: e.ArgsName}
	}
	if shape.service {
		e.ServiceCallers = &ir.ServiceCallers{Mode: ir.ServiceCallersRequire}
	}
	s.ops = append(s.ops, declared{endpoint: e, op: op})
}

func list(name string) ir.TypeRef { return ir.TypeRef{Name: name, IsArray: true} }
func one(name string) ir.TypeRef  { return ir.TypeRef{Name: name} }

// TestTheOperationsDescribeResources classifies an operation of each shape
// a page serves, and of each it does not: a list and a detail make a
// collection a resource; POST on it is its new page; PATCH, DELETE and a
// POST, PUT or PATCH of a verb after an item are its edit page, its delete
// button and an action, each needing the detail; a collection under an
// item is nested, under a collection without pages of its own when nothing
// lists or reads that one; and every other operation is left out with its
// reason: a PUT or DELETE of a collection's path, nested or not, among
// them, and a DELETE of a verb after an item, which no page asks first.
func TestTheOperationsDescribeResources(t *testing.T) {
	s := newShapes()
	s.add("listThings", opShape{method: "GET", path: "things", result: list("Thing"), extra: "q", query: true, argForm: true})
	s.add("getThing", opShape{method: "GET", path: "things/{id}", result: one("Thing")})
	s.add("createThing", opShape{method: "POST", path: "things", result: one("Thing"), input: "ThingInput"})
	s.add("updateThing", opShape{method: "PATCH", path: "things/{id}", result: one("Thing"), input: "ThingPatch", argForm: true})
	s.add("replaceThing", opShape{method: "PUT", path: "things/{id}", result: one("Thing"), input: "ThingPatch", argForm: true})
	s.add("deleteThing", opShape{method: "DELETE", path: "things/{id}", result: one("Thing"), argForm: true})
	s.add("archiveThing", opShape{method: "POST", path: "things/{id}/archive", result: one("Thing"), extra: "reason", argForm: true})
	s.add("archiveAgain", opShape{method: "PUT", path: "things/{id}/archive", result: one("Thing"), argForm: true})
	s.add("labelThing", opShape{method: "PUT", path: "things/{id}/label", result: one("Thing"), extra: "label", argForm: true})
	s.add("scrapThing", opShape{method: "DELETE", path: "things/{id}/scrap", result: one("Thing"), argForm: true})
	s.add("replaceThings", opShape{method: "PUT", path: "things", result: list("Thing"), extra: "names", argForm: true})
	s.add("shipThing", opShape{method: "POST", path: "things/{id}/ship", result: one("Thing"), extra: "carrier"})
	s.add("editThing", opShape{method: "POST", path: "things/{id}/edit", result: one("Thing"), argForm: true})
	s.add("pokeThing", opShape{method: "POST", path: "things/{id}", result: one("Thing"), argForm: true})
	s.add("listParts", opShape{method: "GET", path: "things/{thingId}/parts", result: list("Part")})
	s.add("addPart", opShape{method: "POST", path: "things/{thingId}/parts", result: one("Part"), input: "PartInput", argForm: true})
	s.add("replaceParts", opShape{method: "PUT", path: "things/{thingId}/parts", result: list("Part"), extra: "names", argForm: true})
	s.add("clearParts", opShape{method: "DELETE", path: "things/{thingId}/parts", result: list("Part"), argForm: true})
	s.add("listBooks", opShape{method: "GET", path: "libraries/{libraryId}/books", result: list("Book")})
	s.add("foundLibrary", opShape{method: "POST", path: "libraries", result: one("Thing"), extra: "name", argForm: true})
	s.add("listGadgets", opShape{method: "GET", path: "gadgets", result: list("Gadget")})
	s.add("tuneGadget", opShape{method: "PATCH", path: "gadgets/{id}", result: one("Gadget"), input: "GadgetPatch", argForm: true})
	s.add("buildGadget", opShape{method: "POST", path: "gadgets", result: one("Gadget")})
	s.add("me", opShape{method: "GET", path: "auth/me", result: one("Viewer")})
	s.add("summarize", opShape{method: "GET", path: "things/{id}/summary", result: one("Summary")})
	s.add("count", opShape{method: "GET", path: "counts/{id}", result: one("number")})
	s.add("orderWidget", opShape{method: "POST", path: "widgets", result: one("Thing"), input: "WidgetInput"})
	s.add("reindex", opShape{method: "GET", path: "index", result: list("Thing"), service: true})
	s.add("readFile", opShape{method: "GET", path: "files/{name}.txt", result: one("Thing")})

	roots, out := classify(schemaSet{s.schema}, s.ops, s.forms)

	var keys []string
	for _, root := range roots {
		keys = append(keys, root.key)
	}
	if got := strings.Join(keys, " "); got != "gadgets libraries things" {
		t.Fatalf("roots %q, want gadgets, libraries and things", got)
	}
	gadgets, libraries, things := roots[0], roots[1], roots[2]
	for kind, c := range map[string]*classified{
		"list": things.list, "detail": things.detail, "create": things.create, "update": things.update, "delete": things.del,
	} {
		if c == nil {
			t.Errorf("things has no %s", kind)
		}
	}
	if things.list.d.endpoint.Name != "listThings" || things.update.d.endpoint.Name != "updateThing" || things.create.d.endpoint.Name != "createThing" {
		t.Errorf("things' list, update and create are %s, %s and %s", things.list.name(), things.update.name(), things.create.name())
	}
	var verbs []string
	for _, action := range things.actions {
		verbs = append(verbs, action.verb+":"+action.d.endpoint.Name)
	}
	if got := strings.Join(verbs, " "); got != "archive:archiveThing label:labelThing" {
		t.Errorf("things' actions are %s, want archive and label", got)
	}
	if things.param != "thing_id" {
		t.Errorf("things' item parameter is %s, want thing_id", things.param)
	}
	if len(things.children) != 1 {
		t.Fatalf("things has %d children, want parts", len(things.children))
	}
	parts := things.children[0]
	if parts.key != "things/{}/parts" || parts.parent != things || parts.list == nil || parts.create == nil || parts.param != "part_id" {
		t.Errorf("parts: key %s, list %v, create %v, param %s", parts.key, parts.list != nil, parts.create != nil, parts.param)
	}
	// Nothing lists or reads a library, so libraries has no page: only
	// books under one of its items do.
	if libraries.isResource() || len(libraries.children) != 1 || !libraries.children[0].isResource() || libraries.param != "library_id" {
		t.Errorf("libraries: resource %v, children %d, param %s", libraries.isResource(), len(libraries.children), libraries.param)
	}
	if gadgets.list == nil || gadgets.update != nil || gadgets.create != nil {
		t.Errorf("gadgets: list %v, update %v, create %v", gadgets.list != nil, gadgets.update != nil, gadgets.create != nil)
	}

	want := map[string]string{
		"thing.replaceThing":  "its resource's edit page updates it through thing.updateThing",
		"thing.archiveAgain":  "the action archive of its resource is thing.archiveThing",
		"thing.scrapThing":    "a DELETE of things/{id}/scrap is no action: an action's form posts from the item's page, without the confirmation a delete asks",
		"thing.replaceThings": "a PUT of things, a collection, changes all of it, which no page does",
		"thing.replaceParts":  "a PUT of things/{thingId}/parts, a collection, changes all of it, which no page does",
		"thing.clearParts":    "a DELETE of things/{thingId}/parts, a collection, changes all of it, which no page does",
		"thing.foundLibrary":  "no list or detail makes libraries a resource",
		"thing.shipThing":     "its arguments have no form",
		"thing.editThing":     "its word, edit, names the item's edit page",
		"thing.pokeThing":     "a POST of an item, with no word after it, is no action",
		"thing.tuneGadget":    "its resource, gadgets, has no detail page to hold it",
		"thing.buildGadget":   "it takes nothing a form could fill",
		"thing.me":            "a GET of auth/me, a path that ends in a word, is a list only when it returns a list of records",
		"thing.summarize":     "a GET of things/{id}/summary, a path that ends in a word, is a list only when it returns a list of records",
		"thing.count":         "a GET of counts/{id}, a path that ends in a parameter, is a detail only when it returns one record",
		"thing.orderWidget":   "no list or detail makes widgets a resource, nor POST widgets an action of one",
		"thing.reindex":       "a browser holds no service credential (@requireService)",
		"thing.readFile":      "its path segment {name}.txt is not a word or a parameter alone",
	}
	got := map[string]string{}
	for _, left := range out {
		got[left.Operation] = left.Reason
	}
	for operation, reason := range want {
		if got[operation] != reason {
			t.Errorf("%s left out because %q, want %q", operation, got[operation], reason)
		}
	}
	if len(got) != len(want) {
		t.Errorf("left out %v, want %d operations", got, len(want))
	}
}

// TestAPagePrefixIsAPath refuses each prefix a page cannot be served under
// and keeps the rest.
func TestAPagePrefixIsAPath(t *testing.T) {
	for _, prefix := range []string{"/console", "/admin/shop", "/a-b.c~d"} {
		if err := CheckPagesPrefix(prefix); err != nil {
			t.Errorf("%s: %v", prefix, err)
		}
	}
	for _, prefix := range []string{"", "console", "/", "/console/", "/api", "/api/console", "/_superschematic", "/_superschematic/x", "/con{id}", "/a//b", "/a/../b", "/café"} {
		if err := CheckPagesPrefix(prefix); err == nil {
			t.Errorf("%q: accepted, want it refused", prefix)
		}
	}
}

// TestNamesFromPaths gives a collection's parameter and labels from its
// words.
func TestNamesFromPaths(t *testing.T) {
	for word, want := range map[string]string{
		"projects": "project", "categories": "category", "addresses": "address", "statuses": "status",
		"boxes": "box", "news": "new", "status": "status", "people": "people",
	} {
		if got := singular(word); got != want {
			t.Errorf("singular(%s) = %s, want %s", word, got, want)
		}
	}
	for word, want := range map[string]string{"orderLines": "order_lines", "order-lines": "order_lines", "2fa": "r_2fa"} {
		if got := snakeWord(word); got != want {
			t.Errorf("snakeWord(%s) = %s, want %s", word, got, want)
		}
	}
}
