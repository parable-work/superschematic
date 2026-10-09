package topcoat_test

import (
	"bytes"
	"flag"
	"go/parser"
	"go/token"
	"io"
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

// controlsService is the extension's own fixture whose routes do more than
// admit an end user: a signed webhook, @requireService, @allowService, and
// @rateLimit, @bodyLimit and @timeout.
const controlsService = "fixture-controls-api"

// serviceDir is a fixture's directory: the extension's own, or the
// loader's.
func serviceDir(name string) string {
	if name == formsService || name == controlsService {
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
	return buildWithNaming(t, registry.DefaultNaming(), name, outputRoot, outputs, nil)
}

// buildWithNaming is buildService under names, a superschematic.toml,
// writing the build's log to log when it is not nil.
func buildWithNaming(t *testing.T, names registry.Naming, name, outputRoot string, outputs map[string]any, log io.Writer) (*registry.Result, error) {
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
		Log:         log,
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
	result, err := buildWithNaming(t, names, "fixture-api", root, outputs, nil)
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
	if result, err = buildWithNaming(t, unlisted, "fixture-api", testpaths.TempDir(t), outputs, nil); err != nil || result.Outputs[topcoat.OutputKey] != "" {
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
// fixture-forms-api, whose input types make forms, and
// fixture-controls-api, whose routes have a webhook's signature check,
// service clauses and traffic controls, with testdata/golden/<service>;
// -update rewrites them.
func TestGolden(t *testing.T) {
	for _, service := range []string{"fixture-api", "fixture-nested-arrays-api", formsService, controlsService} {
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

// TestWhatHasNoProcedure builds fixture-controls-api. Its signed webhook
// has a guard and neither an in-process call nor a procedure, and its
// result, which no other operation returns, no record. Its @requireService
// operation keeps its in-process call, whose doc says it applies the
// end-user step alone, and has no procedure; its @allowService one has
// both. A procedure whose route has traffic controls gets a layer with
// them, and the build log says why each item is left out.
func TestWhatHasNoProcedure(t *testing.T) {
	root := testpaths.TempDir(t)
	var log bytes.Buffer
	if _, err := buildWithNaming(t, registry.DefaultNaming(), controlsService, root, rustOutputs(), &log); err != nil {
		t.Fatalf("build: %v", err)
	}
	read := func(file string) string {
		data, err := os.ReadFile(filepath.Join(topcoat.Dir(root, controlsService), "src", file))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	operations, procedures, records := read("operations.rs"), read("procedures.rs"), read("records.rs")
	for _, want := range []string{
		"pub async fn can_hook_receive_payment(",
		"/// It has no in-process call: a third party calls it (@webhook), and its route\n/// checks the third party's signature first (@hmacVerified).",
		"pub async fn stock_reindex_stock(",
		"/// It has no procedure: a browser holds no service credential\n/// (@requireService).",
		"This call applies\n/// the end-user step alone",
		"pub async fn stock_release_reservation(",
	} {
		if !strings.Contains(operations, want) {
			t.Errorf("operations.rs lacks %q", want)
		}
	}
	if strings.Contains(operations, "pub async fn hook_receive_payment(") {
		t.Error("operations.rs has an in-process call for the webhook")
	}
	for _, path := range []string{"hook/receive-payment", "stock/reindex-stock"} {
		if strings.Contains(procedures, path) {
			t.Errorf("procedures.rs has a procedure on %s", path)
		}
	}
	for _, want := range []string{
		`#[procedure("/_superschematic/fixture-controls-api/stock/release-reservation")]`,
		`ProcedureControls::new("/_superschematic/fixture-controls-api/order/place-order", refusal::<OrderViewRecord>)
                .rate_limit(2)
                .body_limit_megabytes(3)
                .timeout_seconds(1),`,
	} {
		if !strings.Contains(procedures, want) {
			t.Errorf("procedures.rs lacks %q", want)
		}
	}
	if strings.Contains(records, "ReceiptRecord") {
		t.Error("records.rs mirrors the webhook's result, which no page receives")
	}
	for _, want := range []string{
		"no in-process call for hook.receivePayment: a third party calls it (@webhook)",
		"no procedure for stock.reindexStock: a browser holds no service credential (@requireService)",
	} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("the build log lacks %q:\n%s", want, log.String())
		}
	}
}

// TestProceduresKeepTheirRoutesRules does what TestTheCrateServesATopcoatApp
// does for fixture-controls-api with controlsAppTest: the webhook and the
// @requireService operation have no procedure path, the @allowService one
// has, and a procedure answers its route's rate limit, body limit and
// timeout as a ProblemRecord.
func TestProceduresKeepTheirRoutesRules(t *testing.T) {
	cargoTestCrate(t, controlsService, controlsAppTest, `axum = "0.8.9"`)
}

// cargoTestCrate builds service's crates with the Topcoat crate on, adds
// test as tests/app.rs of the Topcoat crate, with devDeps beside the
// dev-dependencies every test has, and runs cargo clippy with warnings
// denied, then cargo test, on it.
func cargoTestCrate(t *testing.T, service, test string, devDeps ...string) {
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
	for _, dep := range devDeps {
		manifest = append(manifest, dep+"\n"...)
	}
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

// controlsAppTest is tests/app.rs of fixture-controls-api's Topcoat crate.
const controlsAppTest = `use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use axum::extract::Request;
use axum::middleware::Next;
use axum::response::{IntoResponse, Response};
use http::request::Parts;
use schemas_fixture_controls_api_topcoat::api::runtime::{
    ApiError, Authenticator, Principal, RequestContext, ServiceAuthenticator, ServiceCaller,
};
use schemas_fixture_controls_api_topcoat::api::{
    types, HookImplementation, HookReceivePaymentArgs, Implementations, OrderImplementation, StockImplementation,
    StockReleaseReservationArgs, WebhookVerifier,
};
use schemas_fixture_controls_api_topcoat::{operations, PageAuthenticator, RouterBuilderFixtureControlsApiExt};
use topcoat::context::Cx;
use topcoat::router::{header, page, to_bytes, Body, Router, RouterBuilderDiscoverExt, StatusCode};
use topcoat::view::{view, View};

struct NoRequests;

#[async_trait]
impl Authenticator for NoRequests {
    async fn authenticate(&self, _request: &Parts) -> Result<Option<Principal>, ApiError> {
        Ok(None)
    }
}

struct NoServices;

#[async_trait]
impl ServiceAuthenticator for NoServices {
    async fn authenticate(&self, _request: &Parts) -> Result<Option<ServiceCaller>, ApiError> {
        Ok(None)
    }
}

// The provider's signature check, which no request here passes.
struct Unsigned;

#[async_trait]
impl WebhookVerifier for Unsigned {
    async fn verify(&self, _request: Request, _next: Next) -> Response {
        (StatusCode::UNAUTHORIZED, "unsigned").into_response()
    }
}

struct Caller;

#[async_trait]
impl PageAuthenticator for Caller {
    async fn principal(&self, _cx: &Cx) -> Result<Option<Principal>, ApiError> {
        Ok(Some(Principal::new("ada", ["stock"])))
    }
}

// A shop whose orders take longer than their route allows when slow.
struct Shop {
    slow: bool,
}

#[async_trait]
impl HookImplementation for Shop {
    async fn receive_payment(&self, _ctx: RequestContext, args: HookReceivePaymentArgs) -> Result<types::Receipt, ApiError> {
        Ok(types::Receipt { event_id: args.event_id })
    }
}

#[async_trait]
impl StockImplementation for Shop {
    async fn reindex_stock(&self, _ctx: RequestContext) -> Result<types::StockRun, ApiError> {
        Ok(types::StockRun { done: true })
    }
    async fn release_reservation(&self, _ctx: RequestContext, args: StockReleaseReservationArgs) -> Result<types::Reservation, ApiError> {
        Ok(types::Reservation { id: args.id, held: false })
    }
}

#[async_trait]
impl OrderImplementation for Shop {
    async fn place_order(&self, _ctx: RequestContext) -> Result<types::OrderView, ApiError> {
        if self.slow {
            tokio::time::sleep(Duration::from_secs(5)).await;
        }
        Ok(types::OrderView { id: "1".to_string() })
    }
}

fn app(slow: bool) -> Router {
    let shop = Arc::new(Shop { slow });
    let verifier: Arc<dyn WebhookVerifier> = Arc::new(Unsigned);
    let implementations = Implementations {
        hook: shop.clone(),
        order: shop.clone(),
        stock: shop,
        authenticator: Arc::new(NoRequests),
        service_authenticator: Arc::new(NoServices),
        webhook_verifiers: HashMap::from([("stripe".to_string(), verifier)]),
    };
    Router::builder().discover().fixture_controls_api(implementations, Caller).build()
}

async fn post(router: &Router, uri: &str, body: String, length: Option<usize>) -> (StatusCode, http::HeaderMap, String) {
    let mut request = http::Request::builder().method("POST").uri(uri).header(header::CONTENT_TYPE, "application/json");
    if let Some(length) = length {
        request = request.header(header::CONTENT_LENGTH, length);
    }
    let response = router.handle(request.body(Body::from(body)).unwrap()).await;
    let (status, headers) = (response.status(), response.headers().clone());
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    (status, headers, String::from_utf8(bytes.to_vec()).unwrap())
}

// The webhook keeps its guard, and the @requireService operation its
// in-process call, which applies the end-user step alone (D37, amended).
#[page("/guards")]
async fn guards(cx: &Cx) -> topcoat::Result<impl View> {
    let hook = operations::can_hook_receive_payment(cx).await.is_ok();
    let run = operations::stock_reindex_stock(cx).await?;
    let said = format!("webhook guard {hook}, reindexed {}", run.done);
    Ok(view! { <p>(said)</p> })
}

const PLACE_ORDER: &str = "/_superschematic/fixture-controls-api/order/place-order";

// A procedure's arguments, none, with as much space between them as given.
fn no_arguments(spaces: usize) -> String {
    format!("[{}]", " ".repeat(spaces))
}

#[tokio::test]
async fn a_webhook_has_no_procedure_and_its_route_checks_the_signature() {
    let router = app(false);
    let (status, _, _) = post(&router, "/_superschematic/fixture-controls-api/hook/receive-payment", no_arguments(0), None).await;
    assert_eq!(status, StatusCode::NOT_FOUND);
    let (status, _, body) = post(&router, "/api/hooks/payment", r#"{"eventId":"1"}"#.to_string(), None).await;
    assert_eq!(status, StatusCode::UNAUTHORIZED, "{body}");
    assert_eq!(body, "unsigned");
    let request = http::Request::builder().uri("/guards").body(Body::empty()).unwrap();
    let response = router.handle(request).await;
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    let body = String::from_utf8(bytes.to_vec()).unwrap();
    assert!(body.contains("webhook guard true, reindexed true"), "{body}");
}

#[tokio::test]
async fn an_operation_only_services_call_has_no_procedure() {
    let router = app(false);
    let (status, _, _) = post(&router, "/_superschematic/fixture-controls-api/stock/reindex-stock", no_arguments(0), None).await;
    assert_eq!(status, StatusCode::NOT_FOUND);
    // @allowService admits an end user, so its procedure is registered: it
    // refuses a body that is not its JSON rather than answering 404.
    let (status, _, _) = post(&router, "/_superschematic/fixture-controls-api/stock/release-reservation", "{".to_string(), None).await;
    assert_eq!(status, StatusCode::BAD_REQUEST);
}

#[tokio::test]
async fn a_procedure_answers_its_routes_rate_limit_as_a_problem() {
    let router = app(false);
    for _ in 0..2 {
        let (status, _, body) = post(&router, PLACE_ORDER, no_arguments(0), None).await;
        assert_eq!(status, StatusCode::OK, "{body}");
        assert!(body.contains(r#""ok""#), "{body}");
    }
    let (status, headers, body) = post(&router, PLACE_ORDER, no_arguments(0), None).await;
    assert_eq!(status, StatusCode::OK, "{body}");
    assert!(body.contains(r#""err""#) && body.contains(r#""code":"too_many_requests""#) && body.contains(r#""v":"429""#), "{body}");
    assert!(headers.contains_key(header::RETRY_AFTER), "{headers:?}");
}

#[tokio::test]
async fn a_procedure_answers_its_routes_body_limit_as_a_problem() {
    const MEBIBYTE: usize = 1024 * 1024;
    // Past Topcoat's own 2 MiB and within the route's 3: the arguments are
    // read and the order placed.
    let (_, _, body) = post(&app(false), PLACE_ORDER, no_arguments(5 * MEBIBYTE / 2), None).await;
    assert!(body.contains(r#""ok""#), "{body}");
    // Past the route's 3, sent or declared.
    let (status, _, body) = post(&app(false), PLACE_ORDER, no_arguments(3 * MEBIBYTE), None).await;
    assert_eq!(status, StatusCode::OK, "{body}");
    assert!(body.contains(r#""err""#) && body.contains(r#""code":"payload_too_large""#) && body.contains(r#""v":"413""#), "{body}");
    let (_, _, body) = post(&app(false), PLACE_ORDER, no_arguments(0), Some(4 * MEBIBYTE)).await;
    assert!(body.contains(r#""code":"payload_too_large""#), "{body}");
}

#[tokio::test]
async fn a_procedure_answers_its_routes_timeout_as_a_problem() {
    let (status, _, body) = post(&app(true), PLACE_ORDER, no_arguments(0), None).await;
    assert_eq!(status, StatusCode::OK, "{body}");
    assert!(body.contains(r#""err""#) && body.contains(r#""code":"gateway_timeout""#) && body.contains(r#""v":"504""#), "{body}");
}
`
