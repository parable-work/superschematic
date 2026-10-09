package topcoat_test

import (
	"bytes"
	"flag"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/extensions/topcoat"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/loader"
	"github.com/parable-work/superschematic/registry"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fixtures = "../../internal/loader/tsreader/testdata/services"

// formsService is the extension's own fixture, whose input types are of
// every kind a form field takes, and one a form cannot hold.
const formsService = "fixture-forms-api"

// viewsService is the extension's own fixture whose result type has a
// field of every kind a display component renders, and a @display.
const viewsService = "fixture-views-api"

// serviceDir is a fixture's directory: the extension's own, or the
// loader's.
func serviceDir(name string) string {
	if name == formsService || name == viewsService {
		return filepath.Join("testdata", "services", name)
	}
	return filepath.Join(fixtures, name)
}

// rustOutputs are a fixture's outputs with the server in Rust and the
// Topcoat crate on.
func rustOutputs() map[string]any {
	return map[string]any{
		"types":   map[string]any{"rust": map[string]any{"enabled": true}},
		"api":     map[string]any{"enabled": true, "language": "RUST"},
		"topcoat": map[string]any{"enabled": true},
	}
}

// build runs fixture-api's pipeline with the extension linked, into
// outputRoot, with outputs as its config's outputs block.
func build(t *testing.T, outputRoot string, outputs map[string]any) (*registry.Result, error) {
	t.Helper()
	return buildService(t, "fixture-api", outputRoot, outputs)
}

// buildService runs a fixture's pipeline with the extension linked. Its
// encrypted operations are made plain: the Rust router has no decryption
// step.
func buildService(t *testing.T, name, outputRoot string, outputs map[string]any) (*registry.Result, error) {
	t.Helper()
	return buildWithNaming(t, registry.DefaultNaming(), name, outputRoot, outputs)
}

// buildWithNaming is buildService under names, a superschematic.toml.
func buildWithNaming(t *testing.T, names registry.Naming, name, outputRoot string, outputs map[string]any) (*registry.Result, error) {
	t.Helper()
	reg, err := registry.Assemble(names, topcoat.Extension{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	service := serviceDir(name)
	schema, cfg, err := loader.LoadServiceWithConfig(service, loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("LoadServiceWithConfig: %v", err)
	}
	for _, set := range schema.OperationSets {
		set.Encrypted = false
		for _, op := range set.Operations {
			op.Encrypted = false
			for _, arg := range op.Arguments {
				arg.Encrypted = false
			}
		}
	}
	cfg.Outputs = outputs
	return registry.Generate(schema, cfg, registry.Options{
		OutputRoot:  outputRoot,
		ServicePath: service,
		Paths:       testpaths.Local(t),
		Naming:      names,
		Registry:    reg,
		LoadDependency: func(name string) (*ir.Schema, error) {
			return loader.LoadService(filepath.Join(fixtures, name), loader.WithRegistry(reg))
		},
	})
}

// TestTheCrateIsWrittenOnlyBesideARustServer builds fixture-api with
// outputs.topcoat on: beside a Rust server the crate is written; beside a
// Go server, or with the section off, the output is skipped with its
// reason; a key the section does not declare is refused.
func TestTheCrateIsWrittenOnlyBesideARustServer(t *testing.T) {
	root := testpaths.TempDir(t)
	result, err := build(t, root, rustOutputs())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got, want := result.Outputs[topcoat.OutputKey], topcoat.Dir(root, "fixture-api"); got != want {
		t.Errorf("topcoat output in %q, want %q", got, want)
	}

	goServer := rustOutputs()
	goServer["types"] = map[string]any{"go": map[string]any{"enabled": true}}
	goServer["api"] = map[string]any{"enabled": true}
	result, err = build(t, testpaths.TempDir(t), goServer)
	if err != nil {
		t.Fatalf("build with a Go server: %v", err)
	}
	if _, written := result.Outputs[topcoat.OutputKey]; written {
		t.Error("a Go server got a Topcoat crate")
	}
	if !skipped(result, "the API server is not Rust") {
		t.Errorf("a Go server's build did not say why it skipped the crate: %v", result.Skipped)
	}

	off := rustOutputs()
	off["topcoat"] = map[string]any{"enabled": false}
	if result, err = build(t, testpaths.TempDir(t), off); err != nil || !skipped(result, "outputs.topcoat.enabled is false") {
		t.Errorf("outputs.topcoat off: %v, skipped %v", err, result.Skipped)
	}

	unknown := rustOutputs()
	unknown["topcoat"] = map[string]any{"enabled": true, "pages": true}
	if _, err := build(t, testpaths.TempDir(t), unknown); err == nil || !strings.Contains(err.Error(), "pages") {
		t.Errorf("an undeclared key in outputs.topcoat: %v", err)
	}
}

// TestTheNamingFileListsServices builds fixture-api, whose config has no
// outputs.topcoat, under a superschematic.toml whose [extension.topcoat]
// lists it: the crate is written, as the section would write it. The core
// binary never reads the table, so the same configs build there. A key the
// table does not declare fails assembly.
func TestTheNamingFileListsServices(t *testing.T) {
	names, err := registry.ParseNaming([]byte("[extension.topcoat]\nservices = [\"fixture-api\"]\n"), "superschematic.toml")
	if err != nil {
		t.Fatalf("ParseNaming: %v", err)
	}
	outputs := rustOutputs()
	delete(outputs, "topcoat")
	root := testpaths.TempDir(t)
	result, err := buildWithNaming(t, names, "fixture-api", root, outputs)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := result.Outputs[topcoat.OutputKey]; got != topcoat.Dir(root, "fixture-api") {
		t.Errorf("a listed service wrote no crate: outputs %v, skipped %v", result.Outputs, result.Skipped)
	}

	unlisted, err := registry.ParseNaming([]byte("[extension.topcoat]\nservices = [\"other-api\"]\n"), "superschematic.toml")
	if err != nil {
		t.Fatalf("ParseNaming: %v", err)
	}
	if result, err = buildWithNaming(t, unlisted, "fixture-api", testpaths.TempDir(t), outputs); err != nil || result.Outputs[topcoat.OutputKey] != "" {
		t.Errorf("an unlisted service wrote a crate: %v, %v", err, result.Outputs)
	}

	for _, table := range []string{"services = \"fixture-api\"", "pages = true"} {
		names, err := registry.ParseNaming([]byte("[extension.topcoat]\n"+table+"\n"), "superschematic.toml")
		if err != nil {
			t.Fatalf("ParseNaming: %v", err)
		}
		if _, err := registry.Assemble(names, topcoat.Extension{}); err == nil {
			t.Errorf("[extension.topcoat] %s: assembled, want it refused", table)
		}
	}
}

func skipped(result *registry.Result, reason string) bool {
	for _, skip := range result.Skipped {
		if strings.Contains(skip, reason) {
			return true
		}
	}
	return false
}

// TestGolden compares the crates written for fixture-api, whose
// operations need a caller, fixture-nested-arrays-api, whose operations
// need none and whose result nests records in lists of lists,
// fixture-forms-api, whose input types make forms, and fixture-views-api,
// whose result's fields are of every kind a display component renders,
// with testdata/golden/<service>; -update rewrites them.
func TestGolden(t *testing.T) {
	for _, service := range []string{"fixture-api", "fixture-nested-arrays-api", formsService, viewsService} {
		root := testpaths.TempDir(t)
		if _, err := buildService(t, service, root, rustOutputs()); err != nil {
			t.Fatalf("build %s: %v", service, err)
		}
		got := treeOf(t, topcoat.Dir(root, service))
		golden := filepath.Join("testdata", "golden", service)
		if *update {
			if err := os.RemoveAll(golden); err != nil {
				t.Fatal(err)
			}
			for file, data := range got {
				path := filepath.Join(golden, file)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			continue
		}
		want := treeOf(t, golden)
		for file, data := range got {
			if !bytes.Equal(data, want[file]) {
				t.Errorf("%s/%s differs from golden (run with -update to accept)", service, file)
			}
		}
		for file := range want {
			if _, ok := got[file]; !ok {
				t.Errorf("%s/%s is in the golden but was not written", service, file)
			}
		}
	}
}

// treeOf reads every file under dir, by its slash path relative to dir.
func treeOf(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		files[filepath.ToSlash(rel)] = data
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// TestRecordsOff builds with outputs.topcoat.records false: the crate has
// no records module.
func TestRecordsOff(t *testing.T) {
	root := testpaths.TempDir(t)
	outputs := rustOutputs()
	outputs["topcoat"] = map[string]any{"enabled": true, "records": false}
	if _, err := build(t, root, outputs); err != nil {
		t.Fatalf("build: %v", err)
	}
	lib, err := os.ReadFile(filepath.Join(topcoat.Dir(root, "fixture-api"), "src", "lib.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(lib), "mod records") {
		t.Error("lib.rs declares records with outputs.topcoat.records false")
	}
	if _, err := os.Stat(filepath.Join(topcoat.Dir(root, "fixture-api"), "src", "records.rs")); !os.IsNotExist(err) {
		t.Errorf("records.rs written with records false: %v", err)
	}
}

// TestTheCrateServesATopcoatApp builds fixture-api's types, API and
// Topcoat crates and runs cargo clippy, then cargo test, on the Topcoat
// crate with topcoatAppTest: a Topcoat app mounts the JSON API, its pages
// call operations in-process by their routes' rules (401, 403, the
// router's 400, success), a guard admits a caller, and a record mirrors a
// result type without its @uiHidden field.
func TestTheCrateServesATopcoatApp(t *testing.T) {
	cargoTestCrate(t, "fixture-api", topcoatAppTest)
}

// TestACrateWithoutCallersBuilds does the same for
// fixture-nested-arrays-api with nestedRecordsTest: its operations need no
// caller, so the app's setup takes no PageAuthenticator, and its result's
// record holds lists of lists of nested records, an enum's strings and an
// optional list read from the API's JSON.
func TestACrateWithoutCallersBuilds(t *testing.T) {
	cargoTestCrate(t, "fixture-nested-arrays-api", nestedRecordsTest)
}

// TestFormsServeATopcoatApp does the same for fixture-forms-api with
// formsAppTest: a page renders the signup form's fields with the
// attributes its input type's rules give them; a post that breaks a rule,
// or that the operation refuses, re-renders as sent with 422 and each
// field's errors; a valid post signs up and redirects.
func TestFormsServeATopcoatApp(t *testing.T) {
	cargoTestCrate(t, formsService, formsAppTest)
}

// TestAnInputAFormCannotHoldHasNoForm builds fixture-forms-api: NoteInput
// holds a list, so the crate has no form for it.
func TestAnInputAFormCannotHoldHasNoForm(t *testing.T) {
	root := testpaths.TempDir(t)
	if _, err := buildService(t, formsService, root, rustOutputs()); err != nil {
		t.Fatalf("build: %v", err)
	}
	forms, err := os.ReadFile(filepath.Join(topcoat.Dir(root, formsService), "src", "forms.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(forms), "pub struct SignupInputForm") || strings.Contains(string(forms), "NoteInputForm") {
		t.Error("forms.rs: want a form for SignupInput and none for NoteInput")
	}

	outputs := rustOutputs()
	outputs["topcoat"] = map[string]any{"enabled": true, "forms": false}
	root = testpaths.TempDir(t)
	if _, err := buildService(t, formsService, root, outputs); err != nil {
		t.Fatalf("build: %v", err)
	}
	if _, err := os.Stat(filepath.Join(topcoat.Dir(root, formsService), "src", "forms.rs")); !os.IsNotExist(err) {
		t.Errorf("forms.rs written with forms false: %v", err)
	}
}

// TestViewsRenderRecords does the same for fixture-views-api with
// viewsAppTest: pages render an order's detail and a table of orders, each
// kind of field as its rules say, values escaped, the type's @display
// naming the detail, captioning the table and choosing its columns, and a
// comment thread that nests itself.
func TestViewsRenderRecords(t *testing.T) {
	cargoTestCrate(t, viewsService, viewsAppTest)
}

// TestDisplayShapesTheComponents builds fixture-views-api: OrderView's
// @display gives its table the caption Orders and its summary fields as
// columns, in order, its title field heading each row and naming the
// detail with the noun as fallback; a nested Address shows its title in a
// cell, and Money, which declares no title, its detail; Comment, which
// nests itself, boxes its views.
func TestDisplayShapesTheComponents(t *testing.T) {
	root := testpaths.TempDir(t)
	if _, err := buildService(t, viewsService, root, rustOutputs()); err != nil {
		t.Fatalf("build: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(topcoat.Dir(root, viewsService), "src", "views.rs"))
	if err != nil {
		t.Fatal(err)
	}
	views := string(data)
	_, table, ok := strings.Cut(views, "pub async fn order_view_table(")
	if !ok {
		t.Fatal("views.rs has no order_view_table")
	}
	var columns []string
	for _, line := range strings.Split(table, "\n") {
		if _, rest, ok := strings.Cut(line, `<th scope="col" data-field="`); ok {
			column, _, _ := strings.Cut(rest, `"`)
			columns = append(columns, column)
		}
	}
	if got, want := strings.Join(columns, ","), "reference,status,placedAt,shipTo"; got != want {
		t.Errorf("order_view_table's columns are %s, want the summary fields %s", got, want)
	}
	for _, want := range []string{
		`<caption>"Orders"</caption>`,
		`<th scope="row" data-field="reference">(row.reference)</th>`,
		`let label = label_of(Some(record.reference.as_str()), Some("Order"));`,
		`<td data-field="shipTo">(row.ship_to.recipient)</td>`,
		`<td data-field="price">money_detail(record: row.price)</td>`,
		`<dt>"Ship to"</dt>`,
		`"on_hold" => "On hold",`,
	} {
		if !strings.Contains(views, want) {
			t.Errorf("views.rs lacks %s", want)
		}
	}
	if strings.Contains(views, "internalNote") {
		t.Error("views.rs renders the @uiHidden field internalNote")
	}
	_, comment, _ := strings.Cut(views, "pub async fn comment_detail(")
	comment, _, _ = strings.Cut(comment, "#[component]")
	if !strings.Contains(comment, ".boxed()") {
		t.Error("comment_detail, whose record nests itself, does not box its view")
	}
}

// TestViewsOff builds fixture-views-api with outputs.topcoat.views false,
// then records false: neither crate has the views module.
func TestViewsOff(t *testing.T) {
	for _, off := range []string{"views", "records"} {
		root := testpaths.TempDir(t)
		outputs := rustOutputs()
		outputs["topcoat"] = map[string]any{"enabled": true, off: false}
		if _, err := buildService(t, viewsService, root, outputs); err != nil {
			t.Fatalf("build with %s false: %v", off, err)
		}
		dir := topcoat.Dir(root, viewsService)
		lib, err := os.ReadFile(filepath.Join(dir, "src", "lib.rs"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(lib), "mod views") {
			t.Errorf("lib.rs declares views with %s false", off)
		}
		if _, err := os.Stat(filepath.Join(dir, "src", "views.rs")); !os.IsNotExist(err) {
			t.Errorf("views.rs written with %s false: %v", off, err)
		}
	}
}

// cargoTestCrate builds service's crates with the Topcoat crate on, adds
// test as tests/app.rs of the Topcoat crate, and runs cargo clippy with
// warnings denied, then cargo test, on it.
func cargoTestCrate(t *testing.T, service, test string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available")
	}
	root := testpaths.TempDir(t)
	if _, err := buildService(t, service, root, rustOutputs()); err != nil {
		t.Fatalf("build %s: %v", service, err)
	}
	dir := topcoat.Dir(root, service)
	manifest, err := os.ReadFile(filepath.Join(dir, "Cargo.toml"))
	if err != nil {
		t.Fatal(err)
	}
	manifest = append(manifest, []byte(`
[dev-dependencies]
async-trait = "0.1.89"
http = "1"
tokio = { version = "1.52.2", features = ["macros", "rt-multi-thread"] }
`)...)
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tests", "app.rs"), []byte(test), 0o644); err != nil {
		t.Fatal(err)
	}
	target := os.Getenv("CARGO_TARGET_DIR")
	if target == "" {
		target = filepath.Join(t.TempDir(), "target")
	}
	for _, args := range [][]string{
		{"clippy", "--quiet", "--all-targets", "--", "-D", "warnings"},
		{"test", "--quiet"},
	} {
		cmd := exec.Command(cargo, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("cargo %s on the Topcoat crate of %s: %v\n%s", args[0], service, err, out)
		}
	}
}

// TestPublicImportsOnly holds the extension to the D10 promise: it imports
// the public packages and the IR, never an internal package of the core.
func TestPublicImportsOnly(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasPrefix(path, "github.com/parable-work/superschematic/internal") {
				t.Errorf("%s imports %s; an extension uses the public packages only", file, path)
			}
		}
	}
}

// topcoatAppTest is tests/app.rs of fixture-api's Topcoat crate.
const topcoatAppTest = `use std::sync::Arc;

use async_trait::async_trait;
use http::request::Parts;
use schemas_fixture_api_topcoat::api::runtime::{ApiError, Authenticator, Principal, RequestContext};
use schemas_fixture_api_topcoat::api::{
    types, Implementations, SessionImplementation, TenantCreateTenantArgs, TenantGetTenantArgs, TenantImplementation,
    TenantListTenantsArgs, TenantUpdateSecretArgs,
};
use schemas_fixture_api_topcoat::procedures::{self, TenantCreateTenantArgsRecord, TenantGetTenantArgsRecord};
use schemas_fixture_api_topcoat::records::{CreateTenantInputRecord, TenantViewRecord};
use schemas_fixture_api_topcoat::{operations, PageAuthenticator, RouterBuilderFixtureApiExt};
use topcoat::context::Cx;
use topcoat::router::{page, to_bytes, Body, Router, RouterBuilderDiscoverExt, StatusCode};
use topcoat::view::{view, View};

// The API's own authenticator, for requests to the mounted JSON API.
struct NoRequests;

#[async_trait]
impl Authenticator for NoRequests {
    async fn authenticate(&self, _request: &Parts) -> Result<Option<Principal>, ApiError> {
        Ok(None)
    }
}

// The page's caller: the same one for every page of the app.
struct Caller(Option<Principal>);

#[async_trait]
impl PageAuthenticator for Caller {
    async fn principal(&self, _cx: &Cx) -> Result<Option<Principal>, ApiError> {
        Ok(self.0.clone())
    }
}

struct Tenants;

fn uuid(text: &str) -> types::IdentityUUID {
    serde_json::from_value(serde_json::json!(text)).unwrap()
}

fn tenant(name: &str, label: &str) -> types::TenantView {
    types::TenantView { id: uuid("1"), name: name.to_string(), user_count: 3.0, internal_debug_label: label.to_string() }
}

#[async_trait]
impl SessionImplementation for Tenants {
    async fn current_tenant(&self, _ctx: RequestContext) -> Result<types::TenantView, ApiError> {
        Ok(tenant("me", ""))
    }
}

#[async_trait]
impl TenantImplementation for Tenants {
    async fn list_tenants(&self, _ctx: RequestContext, _args: TenantListTenantsArgs) -> Result<Vec<types::TenantView>, ApiError> {
        Ok(vec![])
    }
    async fn create_tenant(&self, ctx: RequestContext, args: TenantCreateTenantArgs) -> Result<types::TenantView, ApiError> {
        let caller = ctx.principal.map(|principal| principal.subject).unwrap_or_default();
        Ok(tenant(&args.input.name, &caller))
    }
    async fn get_tenant(&self, _ctx: RequestContext, _args: TenantGetTenantArgs) -> Result<types::TenantView, ApiError> {
        Ok(tenant("got", ""))
    }
    async fn update_secret(&self, _ctx: RequestContext, _args: TenantUpdateSecretArgs) -> Result<types::TenantView, ApiError> {
        Ok(tenant("updated", ""))
    }
}

fn outcome(result: Result<types::TenantView, ApiError>) -> String {
    match result {
        Ok(view) => format!("created {} by {}", view.name, view.internal_debug_label),
        Err(err) => err.to_string(),
    }
}

async fn create(cx: &Cx, name: &str) -> String {
    let args = TenantCreateTenantArgs { input: types::CreateTenantInput { name: name.to_string(), slug: "acme".to_string() } };
    outcome(operations::tenant_create_tenant(cx, args).await)
}

#[page(POST "/tenants")]
async fn create_tenant(cx: &Cx) -> topcoat::Result<impl View> {
    let said = create(cx, "Acme").await;
    Ok(view! { <p>(said)</p> })
}

#[page(POST "/tenants/short")]
async fn create_short_tenant(cx: &Cx) -> topcoat::Result<impl View> {
    let said = create(cx, "a").await;
    Ok(view! { <p>(said)</p> })
}

#[page("/tenants/readable")]
async fn readable(cx: &Cx) -> topcoat::Result<impl View> {
    let said = match operations::can_tenant_list_tenants(cx).await {
        Ok(caller) => format!("readable by {}", caller.map(|caller| caller.subject).unwrap_or_default()),
        Err(err) => err.to_string(),
    };
    Ok(view! { <p>(said)</p> })
}

// A procedure's body, from a page: the problem as status, code and each
// field error's path and rule, or the result.
fn problem(result: Result<TenantViewRecord, procedures::ProblemRecord>) -> String {
    match result {
        Ok(tenant) => format!("created {}", tenant.name),
        Err(problem) => {
            let fields: Vec<String> = problem.errors.iter().map(|error| format!("{}:{}", error.path, error.validator)).collect();
            format!("{} {} [{}]", problem.status, problem.code, fields.join(","))
        }
    }
}

async fn create_through_procedure(cx: &Cx, name: &str) -> String {
    let args = TenantCreateTenantArgsRecord { input: CreateTenantInputRecord { name: name.to_string(), slug: "acme".to_string() } };
    problem(procedures::call_tenant_create_tenant(cx, args).await)
}

#[page(POST "/procedures/create")]
async fn procedure_create(cx: &Cx) -> topcoat::Result<impl View> {
    let said = create_through_procedure(cx, "Acme").await;
    Ok(view! { <p>(said)</p> })
}

#[page(POST "/procedures/create-short")]
async fn procedure_create_short(cx: &Cx) -> topcoat::Result<impl View> {
    let said = create_through_procedure(cx, "a").await;
    Ok(view! { <p>(said)</p> })
}

#[page(POST "/procedures/get-unparsed")]
async fn procedure_get_unparsed(cx: &Cx) -> topcoat::Result<impl View> {
    let args = TenantGetTenantArgsRecord { id: "not a uuid!".to_string(), include_archived: false };
    let said = problem(procedures::call_tenant_get_tenant(cx, args).await);
    Ok(view! { <p>(said)</p> })
}

// The app registers its pages and the crate's procedures by discovery.
fn app(caller: Option<Principal>) -> Router {
    let implementations =
        Implementations { session: Arc::new(Tenants), tenant: Arc::new(Tenants), authenticator: Arc::new(NoRequests) };
    Router::builder().discover().fixture_api(implementations, Caller(caller)).build()
}

async fn send(router: &Router, method: &str, uri: &str) -> (StatusCode, String) {
    let request = http::Request::builder().method(method).uri(uri).body(Body::empty()).unwrap();
    let response = router.handle(request).await;
    let status = response.status();
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    (status, String::from_utf8(bytes.to_vec()).unwrap())
}

#[tokio::test]
async fn a_page_calls_an_operation_by_its_route_rules() {
    let (status, body) = send(&app(None), "POST", "/tenants").await;
    assert_eq!(status, StatusCode::OK);
    assert!(body.contains("401 unauthorized: Authentication required"), "{body}");

    let reader = Some(Principal::new("ada", ["tenants.read"]));
    let (_, body) = send(&app(reader.clone()), "POST", "/tenants").await;
    assert!(body.contains("403 forbidden: Insufficient permissions"), "{body}");

    let writer = Some(Principal::new("ada", ["tenants"]));
    let (_, body) = send(&app(writer.clone()), "POST", "/tenants/short").await;
    assert!(body.contains("400 bad_request: Request body does not match the declared input"), "{body}");

    let (_, body) = send(&app(writer), "POST", "/tenants").await;
    assert!(body.contains("created Acme by ada"), "{body}");

    let (_, body) = send(&app(reader), "GET", "/tenants/readable").await;
    assert!(body.contains("readable by ada"), "{body}");
    let (_, body) = send(&app(None), "GET", "/tenants/readable").await;
    assert!(body.contains("401 unauthorized"), "{body}");
}

#[tokio::test]
async fn a_procedure_answers_a_refusal_as_a_record() {
    let writer = Some(Principal::new("ada", ["tenants"]));
    let (_, body) = send(&app(writer.clone()), "POST", "/procedures/create").await;
    assert!(body.contains("created Acme"), "{body}");
    let (_, body) = send(&app(writer.clone()), "POST", "/procedures/create-short").await;
    assert!(body.contains("400 bad_request [name:minLength]"), "{body}");
    let (_, body) = send(&app(None), "POST", "/procedures/create").await;
    assert!(body.contains("401 unauthorized []"), "{body}");
    let (_, body) = send(&app(writer), "POST", "/procedures/get-unparsed").await;
    assert!(body.contains("400 bad_request [id:type]"), "{body}");
}

#[tokio::test]
async fn the_procedures_are_discovered_on_their_paths() {
    // Registered: the procedure refuses a body that is not its JSON,
    // rather than the router answering 404.
    let (status, _) = send(&app(None), "POST", "/_superschematic/fixture-api/tenant/create-tenant").await;
    assert_eq!(status, StatusCode::BAD_REQUEST);
    let (status, _) = send(&app(None), "GET", "/_superschematic/fixture-api/tenant/create-tenant").await;
    assert_eq!(status, StatusCode::METHOD_NOT_ALLOWED);
    let (status, _) = send(&app(None), "POST", "/_superschematic/fixture-api/tenant/no-such-operation").await;
    assert_eq!(status, StatusCode::NOT_FOUND);
}

#[tokio::test]
async fn the_json_api_is_mounted() {
    let (status, body) = send(&app(None), "GET", "/api/openapi.json").await;
    assert_eq!(status, StatusCode::OK, "{body}");
    assert!(body.contains("\"openapi\""), "{body}");
    let (status, body) = send(&app(None), "GET", "/api/tenants?ids=1").await;
    assert_eq!(status, StatusCode::UNAUTHORIZED, "{body}");
}

#[test]
fn a_record_mirrors_a_result_without_its_hidden_field() {
    let record = TenantViewRecord::from(tenant("Acme", "internal"));
    assert_eq!(
        record,
        TenantViewRecord { id: uuid("1").to_string(), name: "Acme".to_string(), user_count: 3.0 }
    );
}
`

// nestedRecordsTest is tests/app.rs of fixture-nested-arrays-api's Topcoat
// crate.
const nestedRecordsTest = `use std::sync::Arc;

use async_trait::async_trait;
use schemas_fixture_nested_arrays_api_topcoat::api::runtime::{ApiError, RequestContext};
use schemas_fixture_nested_arrays_api_topcoat::api::{
    types, GridGetGridArgs, GridGridLabelsArgs, GridImplementation, GridReplaceLabelsArgs, GridSaveGridArgs, Implementations,
};
use schemas_fixture_nested_arrays_api_topcoat::records::{GridViewRecord, PointRecord};
use schemas_fixture_nested_arrays_api_topcoat::{operations, RouterBuilderFixtureNestedArraysApiExt};
use topcoat::context::Cx;
use topcoat::router::{page, to_bytes, Body, Router, StatusCode};
use topcoat::view::{view, View};

struct Grids;

fn grid() -> types::GridView {
    serde_json::from_value(serde_json::json!({
        "id": "1",
        "labels": [["a", "b"], []],
        "shades": [["dark"]],
        "polygons": [[{"x": 1.0, "y": 2.0}], []],
        "weights": null,
    }))
    .unwrap()
}

#[async_trait]
impl GridImplementation for Grids {
    async fn save_grid(&self, _ctx: RequestContext, _args: GridSaveGridArgs) -> Result<types::GridView, ApiError> {
        Ok(grid())
    }
    async fn replace_labels(&self, _ctx: RequestContext, _args: GridReplaceLabelsArgs) -> Result<types::GridView, ApiError> {
        Ok(grid())
    }
    async fn get_grid(&self, ctx: RequestContext, _args: GridGetGridArgs) -> Result<types::GridView, ApiError> {
        assert!(ctx.principal.is_none());
        Ok(grid())
    }
    async fn grid_labels(&self, _ctx: RequestContext, _args: GridGridLabelsArgs) -> Result<Vec<Vec<String>>, ApiError> {
        Ok(vec![])
    }
}

#[page("/grid")]
async fn show_grid(cx: &Cx) -> topcoat::Result<impl View> {
    let args = GridGetGridArgs { id: grid().id };
    let record = GridViewRecord::from(operations::grid_get_grid(cx, args).await?);
    let first = record.labels.first().cloned().unwrap_or_default().join(",");
    Ok(view! { <p>(first)</p> })
}

#[tokio::test]
async fn a_page_calls_an_operation_that_needs_no_caller() {
    let router = Router::builder().page(show_grid).fixture_nested_arrays_api(Implementations { grid: Arc::new(Grids) }).build();
    let request = http::Request::builder().uri("/grid").body(Body::empty()).unwrap();
    let response = router.handle(request).await;
    assert_eq!(response.status(), StatusCode::OK);
    let body = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    assert!(String::from_utf8(body.to_vec()).unwrap().contains("<p>a,b</p>"));
}

#[test]
fn a_record_holds_nested_lists_of_records() {
    let record = GridViewRecord::from(grid());
    assert_eq!(record.labels, vec![vec!["a".to_string(), "b".to_string()], vec![]]);
    assert_eq!(record.shades, vec![vec!["dark".to_string()]]);
    assert_eq!(record.polygons, vec![vec![PointRecord { x: 1.0, y: 2.0 }], vec![]]);
    assert_eq!(record.weights, None);
}
`

// formsAppTest is tests/app.rs of fixture-forms-api's Topcoat crate.
const formsAppTest = `use std::sync::Arc;

use async_trait::async_trait;
use schemas_fixture_forms_api_topcoat::api::runtime::{ApiError, RequestContext};
use schemas_fixture_forms_api_topcoat::api::{types, AccountAnnotateArgs, AccountImplementation, AccountSignUpArgs, Implementations};
use schemas_fixture_forms_api_topcoat::forms::{signup_input_fields, FormErrors, SignupInputForm};
use schemas_fixture_forms_api_topcoat::{operations, RouterBuilderFixtureFormsApiExt};
use serde_json::json;
use topcoat::context::Cx;
use topcoat::router::content::Form;
use topcoat::router::error::see_other;
use topcoat::router::{header, page, to_bytes, Body, Router, StatusCode};
use topcoat::view::{view, View};

struct Accounts;

#[async_trait]
impl AccountImplementation for Accounts {
    async fn sign_up(&self, _ctx: RequestContext, args: AccountSignUpArgs) -> Result<types::AccountView, ApiError> {
        let input = args.input;
        if input.email == "taken@example.com" {
            return Err(ApiError::conflict("That email is taken")
                .with_errors(json!({"email": [{"validator": "unique", "message": "is taken"}]})));
        }
        Ok(types::AccountView {
            id: serde_json::from_value(json!("1")).unwrap(),
            email: input.email,
            display_name: input.display_name,
            plan: input.plan,
            seats: input.seats,
            newsletter: input.newsletter.unwrap_or_default(),
        })
    }
    async fn annotate(&self, _ctx: RequestContext, _args: AccountAnnotateArgs) -> Result<types::AccountView, ApiError> {
        Err(ApiError::not_implemented("annotate"))
    }
}

#[page("/signup")]
async fn signup_form() -> topcoat::Result<impl View> {
    Ok(view! { <form method="post">signup_input_fields()</form> })
}

#[page(POST "/signup")]
async fn sign_up(cx: &Cx, Form(form): Form<SignupInputForm>) -> topcoat::Result<impl View> {
    let errors = match form.parse() {
        Ok(input) => match operations::account_sign_up(cx, AccountSignUpArgs { input }).await {
            Ok(_) => return Err(see_other("/welcome").into()),
            Err(err) => FormErrors::from_api(&err),
        },
        Err(errors) => errors,
    };
    Ok(view! {
        (StatusCode::UNPROCESSABLE_ENTITY)
        <form method="post">signup_input_fields(form: form, errors: errors)</form>
    })
}

fn app() -> Router {
    Router::builder()
        .page(signup_form)
        .page(sign_up)
        .fixture_forms_api(Implementations { account: Arc::new(Accounts) })
        .build()
}

async fn send(request: http::Request<Body>) -> (StatusCode, http::HeaderMap, String) {
    let response = app().handle(request).await;
    let (status, headers) = (response.status(), response.headers().clone());
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    (status, headers, String::from_utf8(bytes.to_vec()).unwrap())
}

async fn post(body: &str) -> (StatusCode, http::HeaderMap, String) {
    send(
        http::Request::builder()
            .method("POST")
            .uri("/signup")
            .header(header::CONTENT_TYPE, "application/x-www-form-urlencoded")
            .body(Body::from(body.to_owned()))
            .unwrap(),
    )
    .await
}

#[tokio::test]
async fn the_form_renders_its_rules_as_attributes() {
    let (status, _, html) = send(http::Request::builder().uri("/signup").body(Body::empty()).unwrap()).await;
    assert_eq!(status, StatusCode::OK);
    for want in [
        r#"name="email" type="email" required="" maxlength="255""#,
        r#"<label for="signup-input-display-name">Display name</label>"#,
        r#"type="text" required="" minlength="2" maxlength="40" placeholder="Ada Lovelace""#,
        r#"pattern="^[a-z0-9]+$""#,
        r#"name="website" type="url""#,
        r#"type="number" step="1" min="1" max="50""#,
        r#"type="number" step="any""#,
        r#"<option value="pro">Pro</option>"#,
        r#"name="newsletter" type="checkbox""#,
    ] {
        assert!(html.contains(want), "missing {want} in {html}");
    }
    assert!(!html.contains("pattern=\"^[a-zA-Z0-9._%+-]"), "an email input carries the scalar's pattern: {html}");
}

#[tokio::test]
async fn a_post_that_breaks_a_rule_renders_again_with_its_errors() {
    let (status, _, html) = post("email=ada%40example.com&displayName=Ada&plan=pro&seats=many&newsletter=on").await;
    assert_eq!(status, StatusCode::UNPROCESSABLE_ENTITY, "{html}");
    assert!(html.contains("must be a whole number"), "{html}");
    assert!(html.contains(r#"value="many""#), "{html}");
    assert!(html.contains(r#"<option value="pro" selected="">"#), "{html}");
    assert!(html.contains(r#"type="checkbox" checked="""#), "{html}");

    let (status, _, html) = post("email=ada%40example.com&displayName=A&plan=pro&seats=3").await;
    assert_eq!(status, StatusCode::UNPROCESSABLE_ENTITY, "{html}");
    assert!(html.contains("at least 2"), "{html}");
    assert!(html.contains(r#"value="A" aria-invalid="true""#), "{html}");

    let (status, _, html) = post("email=taken%40example.com&displayName=Ada&plan=free").await;
    assert_eq!(status, StatusCode::UNPROCESSABLE_ENTITY, "{html}");
    assert!(html.contains("is taken"), "{html}");
}

#[tokio::test]
async fn a_valid_post_signs_up() {
    let (status, headers, _) = post("email=ada%40example.com&displayName=Ada&plan=free&seats=3&budget=12.5").await;
    assert_eq!(status, StatusCode::SEE_OTHER);
    assert_eq!(headers[header::LOCATION], "/welcome");
}

#[test]
fn a_form_parses_into_its_input() {
    let form = SignupInputForm {
        email: Some("ada@example.com".to_string()),
        display_name: Some("Ada".to_string()),
        handle: Some("ada".to_string()),
        plan: Some("pro".to_string()),
        seats: Some(" 4 ".to_string()),
        budget: Some("2.5".to_string()),
        newsletter: Some("on".to_string()),
        ..SignupInputForm::default()
    };
    let input = form.parse().unwrap();
    assert_eq!((input.plan, input.seats, input.budget, input.newsletter), (types::Plan::Pro, Some(4), Some(2.5), Some(true)));
    assert_eq!(input.website, None);

    let errors = SignupInputForm { handle: Some("Not A Handle".to_string()), ..form }.parse().unwrap_err();
    assert!(!errors.of("handle").is_empty(), "{errors:?}");
    assert!(errors.of("email").is_empty(), "{errors:?}");
}
`

// viewsAppTest is tests/app.rs of fixture-views-api's Topcoat crate.
const viewsAppTest = `use schemas_fixture_views_api_topcoat::records::{CommentRecord, OrderViewRecord};
use schemas_fixture_views_api_topcoat::views::{comment_detail, order_status_label, order_view_detail, order_view_table};
use serde_json::json;
use topcoat::router::{page, to_bytes, Body, Router, StatusCode};
use topcoat::view::{view, View};

// An order as the API sends it, its hidden field included.
fn order(reference: &str, status: &str, bill_to: bool) -> OrderViewRecord {
    let bill_to = bill_to.then(|| json!({"recipient": "Accounts", "city": "Leeds"}));
    OrderViewRecord::from_wire(&json!({
        "id": "8d1f6c9e-0000-4000-8000-000000000001",
        "reference": reference,
        "status": status,
        "placedAt": "2026-10-09T08:30:00Z",
        "deliverBy": "2026-10-12",
        "note": "<script>alert(1)</script>",
        "gift": false,
        "shipTo": {"recipient": "Ada Lovelace", "city": "London & Co"},
        "billTo": bill_to,
        "lines": [{"sku": "anvil", "quantity": 2, "price": {"cents": 1999, "currency": "GBP"}}],
        "tags": ["fragile", "heavy"],
        "attributes": {"gate": "B"},
        "metadata": {"source": "<b>web</b>"},
        "internalNote": "do not show"
    }))
}

// An order whose optional fields are absent.
fn bare_order() -> OrderViewRecord {
    OrderViewRecord { deliver_by: None, note: None, metadata: None, ..order("A-1003", "pending", false) }
}

fn thread() -> CommentRecord {
    let reply = |text: &str, replies| CommentRecord { text: text.to_string(), replies };
    reply("First", vec![reply("Second", vec![reply("Third", vec![])])])
}

#[page("/order")]
async fn show_order() -> topcoat::Result<impl View> {
    Ok(view! { order_view_detail(record: order("A-1001", "on_hold", true)) })
}

#[page("/bare-order")]
async fn show_bare_order() -> topcoat::Result<impl View> {
    Ok(view! { order_view_detail(record: bare_order()) })
}

#[page("/orders")]
async fn show_orders() -> topcoat::Result<impl View> {
    let mut forged = order("A-1002", "shipped", false);
    forged.status = "x\" onclick=\"steal()".to_string();
    Ok(view! { order_view_table(rows: vec![order("A-1001", "on_hold", true), forged]) })
}

#[page("/thread")]
async fn show_thread() -> topcoat::Result<impl View> {
    Ok(view! { comment_detail(record: thread()) })
}

async fn render(uri: &str) -> String {
    let router = Router::builder().page(show_order).page(show_bare_order).page(show_orders).page(show_thread).build();
    let response = router.handle(http::Request::builder().uri(uri).body(Body::empty()).unwrap()).await;
    assert_eq!(response.status(), StatusCode::OK);
    let body = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    String::from_utf8(body.to_vec()).unwrap()
}

fn assert_has(html: &str, wants: &[&str]) {
    for want in wants {
        assert!(html.contains(want), "missing {want} in {html}");
    }
}

#[tokio::test]
async fn a_detail_renders_each_kind_of_field() {
    let html = render("/order").await;
    assert_has(&html, &[
        r#"<div class="ss-detail" data-type="OrderView" role="group" aria-label="A-1001"><dl>"#,
        r#"<div data-field="id"><dt>Id</dt><dd>8d1f6c9e-0000-4000-8000-000000000001</dd></div>"#,
        r#"<div data-field="status"><dt>Status</dt><dd><data value="on_hold">On hold</data></dd></div>"#,
        r#"<div data-field="placedAt"><dt>Placed at</dt><dd><time datetime="2026-10-09T08:30:00Z">2026-10-09T08:30:00Z</time></dd></div>"#,
        r#"<dd><time datetime="2026-10-12">2026-10-12</time></dd>"#,
        r#"<div data-field="gift"><dt>Gift</dt><dd>No</dd></div>"#,
        r#"<div data-field="shipTo"><dt>Ship to</dt><dd><div class="ss-detail" data-type="Address" role="group" aria-label="Ada Lovelace"><dl><div data-field="recipient"><dt>Recipient</dt><dd>Ada Lovelace</dd></div><div data-field="city"><dt>City</dt><dd>London &amp; Co</dd></div></dl></div></dd></div>"#,
        r#"<div data-field="billTo"><dt>Bill to</dt><dd><div class="ss-detail" data-type="Address" role="group" aria-label="Accounts">"#,
        r#"<div data-field="lines"><dt>Lines</dt><dd><table class="ss-table" data-type="OrderLine"><thead><tr><th scope="col" data-field="sku">Sku</th><th scope="col" data-field="quantity">Quantity</th><th scope="col" data-field="price">Price</th></tr></thead><tbody><tr><td data-field="sku">anvil</td><td data-field="quantity">2</td><td data-field="price"><div class="ss-detail" data-type="Money" role="group"><dl><div data-field="cents"><dt>Cents</dt><dd>1999</dd></div><div data-field="currency"><dt>Currency</dt><dd>GBP</dd></div></dl></div></td></tr></tbody></table></dd></div>"#,
        r#"<div data-field="tags"><dt>Tags</dt><dd><ul class="ss-list"><li>fragile</li><li>heavy</li></ul></dd></div>"#,
        r#"<div data-field="attributes"><dt>Attributes</dt><dd><dl class="ss-map"><div><dt>gate</dt><dd>B</dd></div></dl></dd></div>"#,
        r#"<div data-field="metadata"><dt>Metadata</dt><dd><pre class="ss-json">{"source":"&lt;b&gt;web&lt;/b&gt;"}</pre></dd></div>"#,
        // A value is text: the note's script is escaped.
        r#"<div data-field="note"><dt>Note</dt><dd>&lt;script&gt;alert(1)&lt;/script&gt;</dd></div>"#,
    ]);
    assert!(!html.contains("<script>") && !html.contains("<b>"), "unescaped markup in {html}");
    assert!(!html.contains("internalNote") && !html.contains("do not show"), "the hidden field is shown: {html}");
}

#[tokio::test]
async fn an_absent_value_leaves_its_entry_empty() {
    let html = render("/bare-order").await;
    assert_has(&html, &[
        r#"<div data-field="deliverBy"><dt>Deliver by</dt><dd></dd></div>"#,
        r#"<div data-field="note"><dt>Note</dt><dd></dd></div>"#,
        r#"<div data-field="billTo"><dt>Bill to</dt><dd></dd></div>"#,
        r#"<div data-field="metadata"><dt>Metadata</dt><dd></dd></div>"#,
    ]);
}

#[tokio::test]
async fn a_table_shows_the_summary_fields_under_its_caption() {
    let html = render("/orders").await;
    assert_has(&html, &[
        r#"<table class="ss-table" data-type="OrderView"><caption>Orders</caption><thead><tr><th scope="col" data-field="reference">Reference</th><th scope="col" data-field="status">Status</th><th scope="col" data-field="placedAt">Placed at</th><th scope="col" data-field="shipTo">Ship to</th></tr></thead><tbody>"#,
        r#"<tr><th scope="row" data-field="reference">A-1001</th><td data-field="status"><data value="on_hold">On hold</data></td><td data-field="placedAt"><time datetime="2026-10-09T08:30:00Z">2026-10-09T08:30:00Z</time></td><td data-field="shipTo">Ada Lovelace</td></tr>"#,
        // A value the enum does not declare is its own label, escaped in
        // the attribute and the text.
        r#"<td data-field="status"><data value="x&quot; onclick=&quot;steal()">x" onclick="steal()</data></td>"#,
    ]);
    assert!(!html.contains(r#"data-field="note""#), "a column the summary fields leave out: {html}");
}

#[tokio::test]
async fn a_record_that_nests_itself_renders_each_level() {
    let html = render("/thread").await;
    assert_has(&html, &[
        r#"<div data-field="text"><dt>Text</dt><dd>First</dd></div><div data-field="replies"><dt>Replies</dt><dd><table class="ss-table" data-type="Comment">"#,
        r#"<tr><td data-field="text">Second</td><td data-field="replies"><table class="ss-table" data-type="Comment">"#,
        r#"<tr><td data-field="text">Third</td><td data-field="replies"><table class="ss-table" data-type="Comment"><thead>"#,
    ]);
}

#[test]
fn an_enum_value_is_labeled_by_its_member() {
    assert_eq!(order_status_label("on_hold"), "On hold");
    assert_eq!(order_status_label("lost"), "lost");
}
`
