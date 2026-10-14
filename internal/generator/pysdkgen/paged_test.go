package pysdkgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/pygen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// pagedService is the core fixture whose lists page by a cursor (D60).
const pagedService = "fixture-paged-api"

// pageVectors is the page vectors the runtimes are held to, which the
// probe's stub server pages by.
const pageVectors = "../../../runtime/http/testdata/page_parity.json"

// loadPagedAPI loads fixture-paged-api: listItems pages ItemView two to a
// page, at most five, beside a list and a boolean query parameter;
// listTags pages strings with the default limits; getItem and placeBasket
// do not page.
func loadPagedAPI(t *testing.T) (*ir.Schema, *apigen.APIOutput) {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, pagedService))
	if err != nil {
		t.Fatalf("load %s: %v", pagedService, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: pagedService,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return schema, apiOutput
}

func generatePagedSDK(t *testing.T, apiOutput *apigen.APIOutput) *SDKOutput {
	t.Helper()
	sdkOutput, err := Generate(apiOutput, "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return sdkOutput
}

// TestPagedMethodsTakeCursorAndLimit: a paged operation's method takes
// cursor and limit after its own query parameters, both optional, checked
// by the bounds the route reads them with, returns Page[T] and has a
// <method>_all beside it; an operation that does not page is unchanged.
func TestPagedMethodsTakeCursorAndLimit(t *testing.T) {
	_, apiOutput := loadPagedAPI(t)
	sdkOutput := generatePagedSDK(t, apiOutput)
	if !sdkOutput.HasPagedEndpoints {
		t.Error("SDKOutput.HasPagedEndpoints = false, want true")
	}
	if len(sdkOutput.Namespaces) != 1 {
		t.Fatalf("namespaces = %+v", sdkOutput.Namespaces)
	}
	namespace := sdkOutput.Namespaces[0]
	if !namespace.HasPagedEndpoints {
		t.Error("NamespaceInfo.HasPagedEndpoints = false, want true")
	}
	endpoints := map[string]EndpointInfo{}
	for _, endpoint := range namespace.Endpoints {
		endpoints[endpoint.MethodName] = endpoint
	}

	for _, tc := range []struct {
		method, output, item string
		params               []string
		maxLimit             float64
	}{
		{
			"list_items", "Page[ItemView]", "ItemView",
			[]string{"ids", "in_stock", "cursor", "limit", "timeout_seconds", "extra_headers"}, 5,
		},
		{"list_tags", "Page[str]", "str", []string{"cursor", "limit", "timeout_seconds", "extra_headers"}, 100},
	} {
		endpoint, ok := endpoints[tc.method]
		if !ok {
			t.Fatalf("no method %s", tc.method)
		}
		if !endpoint.Paged || endpoint.OutputTypeHint != tc.output || endpoint.ItemTypeHint != tc.item ||
			endpoint.AllMethodName != tc.method+"_all" {
			t.Errorf("%s: paged %t, output %q, item %q, all %q", tc.method, endpoint.Paged, endpoint.OutputTypeHint, endpoint.ItemTypeHint, endpoint.AllMethodName)
		}
		var names []string
		for _, param := range endpoint.MethodParams {
			names = append(names, param.Name)
		}
		if len(names) != len(tc.params) {
			t.Fatalf("%s parameters = %v, want %v", tc.method, names, tc.params)
		}
		for i := range names {
			if names[i] != tc.params[i] {
				t.Errorf("%s parameters = %v, want %v", tc.method, names, tc.params)
				break
			}
		}
		for _, declaration := range []string{"cursor: str | None = None", "limit: int | None = None"} {
			if !hasMethodParam(endpoint, declaration) {
				t.Errorf("%s has no parameter %q", tc.method, declaration)
			}
		}
		page := endpoint.QueryParams[len(endpoint.QueryParams)-2:]
		cursor, limit := page[0], page[1]
		if cursor.Name != "cursor" || cursor.ValidateMinLength != nil || *cursor.ValidateMaxLength != ir.PageCursorMaxLength ||
			cursor.ValidatePattern != ir.PageCursorPattern || !cursor.EmptyIsAbsent {
			t.Errorf("%s cursor = %+v", tc.method, cursor)
		}
		if limit.Name != "limit" || *limit.ValidateMin != 1 || *limit.ValidateMax != tc.maxLimit {
			t.Errorf("%s limit = %+v", tc.method, limit)
		}
	}

	for _, method := range []string{"get_item", "place_basket"} {
		endpoint := endpoints[method]
		if endpoint.Paged || endpoint.AllMethodName != "" || hasMethodParam(endpoint, "cursor: str | None = None") {
			t.Errorf("%s pages: %+v", method, endpoint)
		}
	}
}

// TestWriteSDKGoldenPaged pins every file of the Python SDK for
// fixture-paged-api: page.py and its export, the paged methods, their
// _all iterators and _coerce_page. Regenerate with:
// go test ./internal/generator/pysdkgen -run TestWriteSDKGoldenPaged -update
func TestWriteSDKGoldenPaged(t *testing.T) {
	_, apiOutput := loadPagedAPI(t)
	outDir := t.TempDir()
	if err := WriteSDK(generatePagedSDK(t, apiOutput), outDir); err != nil {
		t.Fatalf("WriteSDK: %v", err)
	}
	compareGoldenTree(t, outDir, filepath.Join("testdata", "golden", pagedService))
}

// TestPagedSDKImportsAndRuns writes the Python types package and the
// Python SDK of fixture-paged-api, byte-compiles both, and runs pagedProbe
// against a local server that pages as the runtimes do (D60), its cursors
// and pages held to page_parity.json: the first page, the next asked with
// cursor and limit, the last page's null next, the iterator asking for each
// page only when its items are reached, an error raised where the
// iteration reaches it, a repeated cursor ending it, and a cursor or limit
// the route would refuse failing before any request. The probe needs
// pydantic and skips without it.
func TestPagedSDKImportsAndRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	python, err := findCompatiblePython()
	if err != nil {
		t.Skipf("no compatible python: %v", err)
	}
	schema, apiOutput := loadPagedAPI(t)

	root := t.TempDir()
	typesDir := filepath.Join(root, "types")
	sdkDir := filepath.Join(root, "sdk")
	typesOutput, err := pygen.Generate(schema, pygen.Options{SchemaName: pagedService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("pygen.Generate: %v", err)
	}
	if err := pygen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("pygen.WriteTypes: %v", err)
	}
	sdkOutput := generatePagedSDK(t, apiOutput)
	if err := WriteSDK(sdkOutput, sdkDir); err != nil {
		t.Fatalf("WriteSDK: %v", err)
	}
	if sdkOutput.TypesPackage != typesOutput.PythonModuleName {
		t.Fatalf("SDK types package %q, types module %q", sdkOutput.TypesPackage, typesOutput.PythonModuleName)
	}

	compile := exec.Command(python, "-m", "compileall", "-q", root)
	if out, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("generated packages do not byte-compile: %v\n%s", err, out)
	}
	if err := exec.Command(python, "-c", "import pydantic").Run(); err != nil {
		t.Skip("pydantic not available; skipping the Python SDK probe")
	}
	vectors, err := filepath.Abs(pageVectors)
	if err != nil {
		t.Fatal(err)
	}
	probe := exec.Command(python, "-c", pagedProbe,
		typesDir, sdkDir, sdkOutput.PackageName, sdkOutput.SDKClassName, typesOutput.PythonModuleName,
		sdkOutput.Namespaces[0].AccessorName, vectors)
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("Python SDK probe failed: %v\n%s", err, out)
	}
}

// pagedProbe takes the types and SDK directories, the SDK package and
// class, the types package, the namespace accessor and the page vectors as
// arguments.
const pagedProbe = `
import base64
import json
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import parse_qs, urlsplit

types_dir, sdk_dir, sdk_name, sdk_class, types_name, accessor, vectors_path = sys.argv[1:]
sys.path.insert(0, types_dir)
sys.path.insert(0, sdk_dir)
sdk_package = __import__(sdk_name)
types_package = __import__(types_name)
APIError = sdk_package.APIError
ClientConfig = sdk_package.ClientConfig
Page = sdk_package.Page
SDKError = sdk_package.SDKError
ValidationError = sdk_package.ValidationError
ItemView = types_package.ItemView


def encode_cursor(key):
    """A cursor as the runtimes write one: the unpadded base64url of the
    key's compact JSON."""
    text = json.dumps(key, separators=(",", ":"), sort_keys=True, ensure_ascii=False)
    return base64.urlsafe_b64encode(text.encode()).decode().rstrip("=")


def decode_cursor(cursor):
    return json.loads(base64.urlsafe_b64decode(cursor + "=" * (-len(cursor) % 4)).decode())


def from_rows(rows, limit, key):
    """Page::from_rows: limit of up to limit + 1 rows, next the key of the
    last one kept when there were more."""
    items = rows[:limit]
    return {"items": items, "next": encode_cursor(key(items[-1])) if len(rows) > limit else None}


# The stub pages as the runtimes do.
with open(vectors_path) as vectors_file:
    vectors = json.load(vectors_file)
for vector in vectors["cursors"]:
    assert encode_cursor(json.loads(vector["keyJSON"])) == vector["cursor"], vector["name"]
    assert decode_cursor(vector["cursor"]) == json.loads(vector["keyJSON"]), vector["name"]
for vector in vectors["fromRows"]:
    assert from_rows(vector["rows"], vector["limit"], lambda row: row) == vector["page"], vector["name"]
refused = {vector["cursor"]: vector["problem"] for vector in vectors["refused"]}
NOT_JSON = "bm90IGpzb24"
assert NOT_JSON in refused

ITEMS = [
    {"id": f"0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e4{n}", "name": f"Item {n}", "sku": f"item-{n}", "inStock": n % 2 == 0}
    for n in range(5)
]
TAGS = ["blue", "green", "red"]
requests = []
# next_override, when set, is the next the stub answers instead of its own;
# drop_next leaves next out of the page.
stub = {"next_override": None, "drop_next": False}


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        url = urlsplit(self.path)
        query = parse_qs(url.query, keep_blank_values=True)
        requests.append((url.path, query))
        cursor = query.get("cursor", [None])[0]
        if url.path == "/api/items":
            limit = int(query.get("limit", ["2"])[0])
            start = 0
            if cursor is not None:
                try:
                    start = [item["id"] for item in ITEMS].index(decode_cursor(cursor)["id"]) + 1
                except Exception:
                    return self.answer(refused[NOT_JSON]["status"], {"type": "about:blank", "title": "Bad Request", **refused[NOT_JSON]})
            page = from_rows(ITEMS[start : start + limit + 1], limit, lambda item: {"id": item["id"]})
            if stub["next_override"] is not None:
                page["next"] = stub["next_override"]
            if stub["drop_next"]:
                del page["next"]
        else:
            assert url.path == "/api/tags", self.path
            # A server that answers the cursor it was sent, after the first page.
            page = {"items": TAGS[:2], "next": "MQ"} if cursor is None else {"items": TAGS[2:], "next": cursor}
        self.answer(200, {"data": page, "meta": {"requestId": "req-1"}})

    def answer(self, status, payload):
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json" if status == 200 else "application/problem+json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


server = HTTPServer(("127.0.0.1", 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
sdk = getattr(sdk_package, sdk_class)(ClientConfig(base_url=f"http://127.0.0.1:{server.server_address[1]}", max_network_retries=0))
catalog = getattr(sdk, accessor)


def names_of(items):
    """The names of the items, which the models hold as sent (an
    Identity.UUID they hold in its canonical form)."""
    return [item.name for item in items]


NAMES = [item["name"] for item in ITEMS]


# The first page: no cursor or limit sent, the route's default limit of 2,
# each item an ItemView, next the cursor after the second.
first = catalog.list_items()
assert isinstance(first, Page), type(first)
assert requests[-1] == ("/api/items", {}), requests[-1]
assert [type(item) for item in first.items] == [ItemView, ItemView], first.items
assert names_of(first.items) == NAMES[:2], first.items
assert first.next == encode_cursor({"id": ITEMS[1]["id"]}), first.next

# The next page is asked with the last one's next, beside the same filters.
second = catalog.list_items(ids=[ITEMS[0]["id"], ITEMS[4]["id"]], in_stock=True, cursor=first.next, limit=2)
assert requests[-1] == (
    "/api/items",
    {"ids": [ITEMS[0]["id"] + "," + ITEMS[4]["id"]], "inStock": ["true"], "cursor": [first.next], "limit": ["2"]},
), requests[-1]
assert names_of(second.items) == NAMES[2:4], second.items

# The last page's next is null.
last = catalog.list_items(cursor=second.next, limit=5)
assert names_of(last.items) == NAMES[4:] and last.next is None, last

# The iterator asks for each page only when its items are reached.
before = len(requests)
every = catalog.list_items_all(in_stock=False, limit=2)
assert len(requests) == before, "the iterator asked before it was iterated"
taken = [next(every), next(every)]
assert len(requests) == before + 1, requests[before:]
taken.append(next(every))
assert len(requests) == before + 2, requests[before:]
taken.extend(every)
assert names_of(taken) == NAMES, taken
assert [query for _, query in requests[before:]] == [
    {"inStock": ["false"], "limit": ["2"]},
    {"inStock": ["false"], "limit": ["2"], "cursor": [encode_cursor({"id": ITEMS[1]["id"]})]},
    {"inStock": ["false"], "limit": ["2"], "cursor": [encode_cursor({"id": ITEMS[3]["id"]})]},
], requests[before:]

# It starts from the cursor it is given.
assert names_of(catalog.list_items_all(cursor=second.next)) == NAMES[4:]
assert [str(item) for item in catalog.list_tags().items] == ["blue", "green"]

# An error raises where the iteration reaches it, after the items before it.
stub["next_override"] = NOT_JSON
broken = catalog.list_items_all()
assert names_of([next(broken), next(broken)]) == NAMES[:2]
try:
    next(broken)
except APIError as err:
    assert err.status_code == 400 and err.code == "bad_request", err
    assert err.message == "cursor is not one this operation returned", err.message
else:
    raise AssertionError("expected the refused cursor's APIError")
stub["next_override"] = None

# A page whose next is the cursor it was asked with ends the iteration
# with an error rather than a loop, after its items.
tags = catalog.list_tags_all()
assert [next(tags), next(tags), next(tags)] == TAGS
try:
    next(tags)
except SDKError as err:
    assert str(err) == "list_tags returned the cursor it was sent", err
else:
    raise AssertionError("expected an SDKError for the repeated cursor")


def refused_before_the_request(call, want):
    before = len(requests)
    try:
        call()
    except ValidationError as err:
        got = {path: errors[0]["validator"] for path, errors in err.errors.items()}
        assert got == want, f"validators = {got}, want {want}"
    else:
        raise AssertionError(f"expected a ValidationError for {want}")
    assert len(requests) == before, requests[before:]


# A cursor or limit the route would refuse fails before any request.
refused_before_the_request(lambda: catalog.list_items(limit=0), {"limit": "min"})
refused_before_the_request(lambda: catalog.list_items(limit=6), {"limit": "max"})
refused_before_the_request(lambda: catalog.list_tags(limit=101), {"limit": "max"})
refused_before_the_request(lambda: catalog.list_items(cursor="e30="), {"cursor": "pattern"})
refused_before_the_request(lambda: catalog.list_items(cursor="A" * 1025), {"cursor": "maxLength"})
refused_before_the_request(lambda: next(catalog.list_items_all(limit=9)), {"limit": "max"})

# An empty cursor is none, as the route reads it: the first page, asked
# without a cursor.
again = catalog.list_items(cursor="")
assert requests[-1] == ("/api/items", {}), requests[-1]
assert names_of(again.items) == NAMES[:2], again.items
assert names_of(catalog.list_items_all(cursor="")) == NAMES

# A page without next is the last, as the other SDKs read one.
stub["drop_next"] = True
assert catalog.list_items().next is None
assert names_of(catalog.list_items_all(limit=3)) == NAMES[:3]
stub["drop_next"] = False
server.shutdown()
`

// addGuidePages adds a schema type named Page to the loaded
// fixture-paged-api, with Catalog.listPages, a paged list of it, and
// Catalog.getPage, which returns one: the namespace imports the type
// Page beside the SDK's Page.
func addGuidePages(t *testing.T, schema *ir.Schema) {
	t.Helper()
	item := schema.Types["ItemView"]
	schema.Types["Page"] = &ir.TypeDef{
		Name:    "Page",
		Comment: "A page of the catalog's guide.",
		Role:    item.Role,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
			{Name: "title", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}
	for _, set := range schema.OperationSets {
		if set.Name != "Catalog" {
			continue
		}
		set.Operations = append(set.Operations,
			&ir.FieldDef{
				Name: "listPages", Comment: "The pages of the guide.", TypeRef: ir.TypeRef{Name: "Page", IsArray: true}, Required: true,
				HTTPMethod: "GET", RestPath: "pages", Page: &ir.OperationPage{DefaultLimit: 20, MaxLimit: 100},
			},
			&ir.FieldDef{
				Name: "getPage", Comment: "One page of the guide.", TypeRef: ir.TypeRef{Name: "Page"}, Required: true,
				Arguments:  []*ir.ArgumentDef{{Name: "pageId", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true}},
				HTTPMethod: "GET", RestPath: "pages/{pageId}",
			})
		return
	}
	t.Fatal("fixture-paged-api has no Catalog operation set")
}

// TestASchemaTypeNamedPageKeepsTheSDKPage: a namespace that imports a
// schema type named Page imports the SDK's Page as SDKPage, as the
// TypeScript SDK names it, so the type checker's Page is the schema's in
// getPage and the SDK's page is SDKPage in every paged method; a
// namespace without one keeps Page. The module imports and runs: listPages
// answers an SDK page of the schema's pages.
func TestASchemaTypeNamedPageKeepsTheSDKPage(t *testing.T) {
	schema, apiOutput := loadPagedAPI(t)
	addGuidePages(t, schema)
	apiOutput, err := apigen.Generate(schema, apigen.Options{Provider: sessionauth.Provider{}, SchemaName: pagedService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	sdkOutput := generatePagedSDK(t, apiOutput)
	if namespace := sdkOutput.Namespaces[0]; namespace.PageType != "SDKPage" {
		t.Fatalf("PageType = %q", namespace.PageType)
	}
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk")
	if err := WriteSDK(sdkOutput, sdkDir); err != nil {
		t.Fatalf("WriteSDK: %v", err)
	}
	module, err := os.ReadFile(filepath.Join(sdkDir, sdkOutput.PackageName, "namespaces", sdkOutput.Namespaces[0].ModuleName+".py"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(module)
	for _, want := range []string{
		"from ..page import Page as SDKPage\n",
		"        Page,\n",
		") -> SDKPage[Page]:",
		") -> SDKPage[ItemView]:",
		") -> Page:",
		"def _coerce_page(self, raw: Any, item_type_name: str) -> SDKPage[Any]:",
		"return SDKPage(items=",
	} {
		if !strings.Contains(source, want) {
			t.Errorf("the namespace module lacks %q", want)
		}
	}
	if strings.Contains(source, "from ..page import Page\n") || strings.Contains(source, "-> Page[") {
		t.Error("the namespace module still names the SDK's Page as Page")
	}

	if testing.Short() {
		return
	}
	python, err := findCompatiblePython()
	if err != nil {
		t.Skipf("no compatible python: %v", err)
	}
	typesDir := filepath.Join(root, "types")
	typesOutput, err := pygen.Generate(schema, pygen.Options{SchemaName: pagedService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("pygen.Generate: %v", err)
	}
	if err := pygen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("pygen.WriteTypes: %v", err)
	}
	if err := exec.Command(python, "-c", "import pydantic").Run(); err != nil {
		t.Skip("pydantic not available; skipping the Python SDK probe")
	}
	probe := exec.Command(python, "-c", guidePagesProbe,
		typesDir, sdkDir, sdkOutput.PackageName, sdkOutput.SDKClassName, typesOutput.PythonModuleName, sdkOutput.Namespaces[0].AccessorName)
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("Python SDK probe failed: %v\n%s", err, out)
	}
}

// guidePagesProbe takes the types and SDK directories, the SDK package and
// class, the types package and the namespace accessor as arguments.
const guidePagesProbe = `
import json
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

types_dir, sdk_dir, sdk_name, sdk_class, types_name, accessor = sys.argv[1:]
sys.path.insert(0, types_dir)
sys.path.insert(0, sdk_dir)
sdk_package = __import__(sdk_name)
types_package = __import__(types_name)
GUIDE = {"id": "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40", "title": "Anvils"}


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        data = {"items": [GUIDE], "next": None} if self.path == "/api/pages" else GUIDE
        body = json.dumps({"data": data, "meta": {"requestId": "req-1"}}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


server = HTTPServer(("127.0.0.1", 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
sdk = getattr(sdk_package, sdk_class)(sdk_package.ClientConfig(base_url=f"http://127.0.0.1:{server.server_address[1]}", max_network_retries=0))
catalog = getattr(sdk, accessor)
page = catalog.list_pages()
assert isinstance(page, sdk_package.Page), type(page)
assert [type(item) for item in page.items] == [types_package.Page], page.items
assert page.items[0].title == "Anvils" and page.next is None, page
one = catalog.get_page(GUIDE["id"])
assert isinstance(one, types_package.Page) and one.title == "Anvils", one
server.shutdown()
`
