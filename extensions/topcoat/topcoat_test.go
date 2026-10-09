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

// formsService is the extension's own fixture, whose input types hold a
// field of every kind a form renders: values of each scalar, enums, nested
// objects, rows of objects and of values, groups and JSON text.
const formsService = "fixture-forms-api"

// controlsService is the extension's own fixture whose routes do more than
// admit an end user: a signed webhook, @requireService, @allowService, and
// @rateLimit, @bodyLimit and @timeout.
const controlsService = "fixture-controls-api"

// viewsService is the extension's own fixture whose result type has a
// field of every kind a display component renders, and a @display.
const viewsService = "fixture-views-api"

// serviceDir is a fixture's directory: the extension's own, or the
// loader's.
func serviceDir(name string) string {
	if own := filepath.Join("testdata", "services", name); isDir(own) {
		return own
	}
	return filepath.Join(fixtures, name)
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
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
// fixture-forms-api, whose input types make forms, fixture-controls-api,
// whose routes have a webhook's signature check, service clauses and
// traffic controls, fixture-views-api, whose result's fields are of every
// kind a display component renders, fixture-user-routes-api, whose users
// are the core user model's (D50), and fixture-args-api, whose arguments
// make argument forms, with testdata/golden/<service>; -update rewrites
// them.
func TestGolden(t *testing.T) {
	for _, service := range []string{"fixture-api", "fixture-nested-arrays-api", formsService, controlsService, viewsService, userRoutesService, argsService} {
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
// field's errors; a valid post signs up and redirects. The booking form
// renders its nested objects, rows and typed controls at their names; a
// valid post books with the input's JSON; a refused field, nested or in a
// row, renders at its control with the values as sent and the secret
// blank; row buttons add and remove rows within the list's bounds without
// calling the operation; and the app's choices render a select.
func TestFormsServeATopcoatApp(t *testing.T) {
	cargoTestCrate(t, formsService, formsAppTest)
}

// TestEveryInputHasAForm builds fixture-forms-api: every input type its
// calls take has a form, NoteInput's list of strings and BookingInput's
// nested objects, rows, map and JSON value included, and each nested type
// a struct; with forms off the crate has no forms module.
func TestEveryInputHasAForm(t *testing.T) {
	root := testpaths.TempDir(t)
	var log strings.Builder
	if _, err := buildWithNaming(t, registry.DefaultNaming(), formsService, root, rustOutputs(), &log); err != nil {
		t.Fatalf("build: %v", err)
	}
	forms, err := os.ReadFile(filepath.Join(topcoat.Dir(root, formsService), "src", "forms.rs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"pub struct SignupInputForm",
		"pub struct NoteInputForm",
		"pub struct BookingInputForm",
		"pub struct GuestForm",
		"pub struct RoomRequestForm",
		"pub async fn booking_input_fields(",
	} {
		if !strings.Contains(string(forms), want) {
			t.Errorf("forms.rs: want %s", want)
		}
	}
	if strings.Contains(log.String(), "no form") {
		t.Errorf("build log: an input has no form:\n%s", log.String())
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

// TestWhatHasNoProcedure builds fixture-controls-api. Its signed webhook
// has a guard and neither an in-process call nor a procedure, its input
// no form, and its result, which no other
// operation returns, no record and so no display components. Its
// @requireService operation keeps its in-process call, whose doc says it
// applies the end-user step alone, and has no procedure; its result keeps
// its record and components. Its @allowService one has both. A procedure
// whose route has traffic controls gets a layer with them, and the build
// log says why each item is left out.
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
	operations, procedures, records, views := read("operations.rs"), read("procedures.rs"), read("records.rs"), read("views.rs")
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
	if strings.Contains(read("forms.rs"), "PaymentEventForm") {
		t.Error("forms.rs has a form for the webhook's input, which no page submits")
	}
	if strings.Contains(views, "pub async fn receipt_") {
		t.Error("views.rs renders the webhook's result, which has no record")
	}
	if !strings.Contains(views, "pub async fn stock_run_detail(") {
		t.Error("views.rs lacks stock_run_detail, the @requireService operation's result, which a page calls")
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
// has, a procedure answers its route's rate limit, body limit and timeout
// as a ProblemRecord, and the procedure's rate limit and the mounted JSON
// API's each count each client by the address Topcoat records for it.
func TestProceduresKeepTheirRoutesRules(t *testing.T) {
	cargoTestCrate(t, controlsService, controlsAppTest, `axum = "0.8.9"`)
}

// cargoTestCrate builds service's crates with the Topcoat crate on, adds
// test as tests/app.rs of the Topcoat crate, with devDeps beside the
// dev-dependencies every test has, and runs cargo clippy with warnings
// denied, then cargo test, on it.
func cargoTestCrate(t *testing.T, service, test string, devDeps ...string) {
	t.Helper()
	var options crateOptions
	for _, dep := range devDeps {
		options.devDependencies += dep + "\n"
	}
	cargoTestCrateWith(t, service, test, options)
}

// crateOptions are what a test of the Topcoat crate adds to its build: the
// crate's features, which clippy and cargo test turn on, lines of the
// manifest's [dev-dependencies], and files of tests/ beside app.rs.
type crateOptions struct {
	features        []string
	devDependencies string
	files           map[string]string
}

// cargoTestCrateWith is cargoTestCrate with options.
func cargoTestCrateWith(t *testing.T, service, test string, options crateOptions) {
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
`+options.devDependencies)...)
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tests", "app.rs"), []byte(test), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, content := range options.files {
		if err := os.WriteFile(filepath.Join(dir, "tests", name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	target := os.Getenv("CARGO_TARGET_DIR")
	if target == "" {
		target = filepath.Join(t.TempDir(), "target")
	}
	var features []string
	if len(options.features) > 0 {
		features = []string{"--features", strings.Join(options.features, ",")}
	}
	for _, args := range [][]string{
		append(append([]string{"clippy", "--quiet", "--all-targets"}, features...), "--", "-D", "warnings"),
		append([]string{"test", "--quiet"}, features...),
	} {
		cmd := exec.Command(cargo, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("cargo %s on the Topcoat crate of %s: %v\n%s", args[0], service, err, out)
		}
	}
}

// userRoutesService is the loader's fixture whose users are the core user
// model's (D50): its authDb, fixture-user-model-db, has the User and
// UserRole tables, and it serves the session and administration routes.
const userRoutesService = "fixture-user-routes-api"

// TestIdentityPagesShareTheAPISession builds fixture-user-routes-api's
// crates over the authDb's SQLite DDL and runs identityAppTest: a user
// signs in through the mounted JSON API, with the session cookie or a
// bearer token, and an IdentityPageAuthenticator page then admits them as
// the API does; a page refuses another site's cookie request, and a page
// whose cookie the API's logout ended clears it.
func TestIdentityPagesShareTheAPISession(t *testing.T) {
	ddl, err := os.ReadFile("../../runtime/http/testdata/identity/sqlite/create.sql")
	if err != nil {
		t.Fatal(err)
	}
	cargoTestCrateWith(t, userRoutesService, identityAppTest, crateOptions{
		features:        []string{"identity-sqlite"},
		devDependencies: `rusqlite = { version = "0.40.2", features = ["bundled"] }` + "\n",
		files:           map[string]string{"identity.sql": string(ddl)},
	})
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
const formsAppTest = `use std::sync::{Arc, Mutex};

use async_trait::async_trait;
use schemas_fixture_forms_api_topcoat::api::runtime::{ApiError, RequestContext};
use schemas_fixture_forms_api_topcoat::api::{
    types, AccountAnnotateArgs, AccountImplementation, AccountSignUpArgs, BookingBookArgs, BookingImplementation, Implementations,
};
use schemas_fixture_forms_api_topcoat::forms::{
    booking_input_fields, signup_input_fields, BookingInputForm, Choices, FormErrors, NoteInputForm, RoomRequestForm, SignupInputForm,
};
use schemas_fixture_forms_api_topcoat::{operations, RouterBuilderFixtureFormsApiExt};
use serde_json::json;
use topcoat::context::Cx;
use topcoat::router::content::Form;
use topcoat::router::error::see_other;
use topcoat::router::{header, page, to_bytes, Body, Router, StatusCode};
use topcoat::view::{view, View};

const GARDEN: &str = "8d1f6c9e-0000-4000-8000-000000000001";
const ATTIC: &str = "8d1f6c9e-0000-4000-8000-000000000002";
const BOOKED: &str = "8d1f6c9e-0000-4000-8000-000000000003";

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

/// Bookings keeps each input the operation is called with, and refuses a
/// booked room at its row.
#[derive(Clone, Default)]
struct Bookings {
    calls: Arc<Mutex<Vec<types::BookingInput>>>,
}

#[async_trait]
impl BookingImplementation for Bookings {
    async fn book(&self, _ctx: RequestContext, args: BookingBookArgs) -> Result<types::BookingView, ApiError> {
        let input = args.input;
        self.calls.lock().unwrap().push(input.clone());
        if let Some(index) = input.rooms.iter().position(|room| room.room_id == uuid(BOOKED)) {
            let mut errors = serde_json::Map::new();
            errors.insert(format!("rooms[{index}]"), json!({"roomId": [{"validator": "available", "message": "is booked"}]}));
            return Err(ApiError::conflict("That room is booked").with_errors(serde_json::Value::Object(errors)));
        }
        Ok(types::BookingView { id: uuid(GARDEN), rooms: input.rooms.len() as f64 })
    }
}

fn uuid(text: &str) -> types::IdentityUUID {
    serde_json::from_value(json!(text)).unwrap()
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

#[page("/book")]
async fn booking_form() -> topcoat::Result<impl View> {
    Ok(view! { <form method="post">booking_input_fields()<button type="submit">"Book"</button></form> })
}

/// The booking form with the rooms a page offers: every row's, and the
/// second row's own, and a row holding a room no choice is.
#[page("/book/choices")]
async fn booking_form_with_choices() -> topcoat::Result<impl View> {
    let mut form = BookingInputForm::new();
    form.rooms.push(RoomRequestForm::new());
    form.rooms.push(RoomRequestForm { room_id: Some(BOOKED.to_owned()), ..RoomRequestForm::new() });
    let choices = Choices::new()
        .with("rooms.roomId", [(GARDEN, "Garden room"), (ATTIC, "Attic")])
        .with("rooms[1].roomId", [(ATTIC, "Attic, the last one")]);
    Ok(view! { <form method="post">booking_input_fields(form: form, choices: choices)</form> })
}

/// A row button renders the form again with its rows; any other post
/// books, and a refusal renders the form again, 422, as sent.
#[page(POST "/book")]
async fn book(cx: &Cx, Form(form): Form<BookingInputForm>) -> topcoat::Result<impl View> {
    let mut form = form;
    let (status, errors) = if form.apply_action() {
        (StatusCode::OK, FormErrors::default())
    } else {
        match form.parse() {
            Ok(input) => match operations::booking_book(cx, BookingBookArgs { input }).await {
                Ok(_) => return Err(see_other("/booked").into()),
                Err(err) => (StatusCode::UNPROCESSABLE_ENTITY, FormErrors::from_api(&err)),
            },
            Err(errors) => (StatusCode::UNPROCESSABLE_ENTITY, errors),
        }
    };
    Ok(view! {
        (status)
        <form method="post">booking_input_fields(form: form, errors: errors)</form>
    })
}

fn app(bookings: Bookings) -> Router {
    Router::builder()
        .page(signup_form)
        .page(sign_up)
        .page(booking_form)
        .page(booking_form_with_choices)
        .page(book)
        .fixture_forms_api(Implementations { account: Arc::new(Accounts), booking: Arc::new(bookings) })
        .build()
}

async fn send_to(router: &Router, request: http::Request<Body>) -> (StatusCode, http::HeaderMap, String) {
    let response = router.handle(request).await;
    let (status, headers) = (response.status(), response.headers().clone());
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    (status, headers, String::from_utf8(bytes.to_vec()).unwrap())
}

async fn send(request: http::Request<Body>) -> (StatusCode, http::HeaderMap, String) {
    send_to(&app(Bookings::default()), request).await
}

fn post_request(path: &str, body: &str) -> http::Request<Body> {
    http::Request::builder()
        .method("POST")
        .uri(path)
        .header(header::CONTENT_TYPE, "application/x-www-form-urlencoded")
        .body(Body::from(body.to_owned()))
        .unwrap()
}

async fn post(body: &str) -> (StatusCode, http::HeaderMap, String) {
    send(post_request("/signup", body)).await
}

/// Pairs as a browser encodes them, brackets included.
fn encode(pairs: &[(&str, &str)]) -> String {
    let escape = |text: &str| {
        text.bytes()
            .map(|byte| match byte {
                b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => (byte as char).to_string(),
                b' ' => "+".to_string(),
                _ => format!("%{byte:02X}"),
            })
            .collect::<String>()
    };
    pairs.iter().map(|(name, value)| format!("{}={}", escape(name), escape(value))).collect::<Vec<_>>().join("&")
}

/// Posts a booking form to a fresh app, and the inputs the operation was
/// called with.
async fn book_with(pairs: &[(&str, &str)]) -> (StatusCode, http::HeaderMap, String, Vec<types::BookingInput>) {
    let bookings = Bookings::default();
    let (status, headers, html) = send_to(&app(bookings.clone()), post_request("/book", &encode(pairs))).await;
    let calls = bookings.calls.lock().unwrap().clone();
    (status, headers, html, calls)
}

/// The markup of the control called name, from its label to its last
/// error: the field's div.
fn control<'a>(html: &'a str, name: &str) -> &'a str {
    let at = html.find(&format!(r#"name="{name}""#)).unwrap_or_else(|| panic!("no control {name} in {html}"));
    let start = html[..at].rfind(r#"<div class="field"#).unwrap();
    let end = at + html[at..].find("</div>").unwrap();
    &html[start..end]
}

/// A valid booking: a guest, two rooms, the amenities, a date, a time, a
/// date-time, a secret, JSON values, and a blank optional billing address.
fn booking() -> Vec<(&'static str, &'static str)> {
    vec![
        ("guest.name", "Ada Lovelace"),
        ("guest.email", "ada@example.com"),
        ("guest.phone", ""),
        ("billing.line1", ""),
        ("billing.city", ""),
        ("billing.postcode", ""),
        ("rooms[0]", ""),
        ("rooms[0].roomId", GARDEN),
        ("rooms[0].adults", "2"),
        ("rooms[0].extras", "wifi"),
        ("rooms[1]", ""),
        ("rooms[1].roomId", ATTIC),
        ("rooms[1].adults", "1"),
        ("amenities", "wifi"),
        ("amenities", "late_checkout"),
        ("arrival", "2026-10-12"),
        ("checkIn", "15:30"),
        ("holdUntil", "2026-10-09T14:30"),
        ("doorCode", "4321"),
        ("currency", "GBP"),
        ("preferences", r#"{"quiet": true}"#),
        ("labels", r#"{"vip": "yes"}"#),
    ]
}

/// booking() with changes: each name's value replaced, or added.
fn booking_with(changes: &[(&'static str, &'static str)]) -> Vec<(&'static str, &'static str)> {
    let mut pairs = booking();
    for (name, value) in changes {
        match pairs.iter_mut().find(|(sent, _)| sent == name) {
            Some(pair) => pair.1 = value,
            None => pairs.push((name, value)),
        }
    }
    pairs
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
    assert!(!html.contains("_action"), "a form without rows has row buttons: {html}");
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

#[tokio::test]
async fn the_booking_form_renders_nested_objects_rows_and_typed_controls() {
    let (status, _, html) = send(http::Request::builder().uri("/book").body(Body::empty()).unwrap()).await;
    assert_eq!(status, StatusCode::OK, "{html}");
    for want in [
        // Enter submits the form without a row action.
        r#"<form method="post"><button type="submit" class="ss-submit" hidden=""></button>"#,
        // A nested object, named by path; an optional one requires nothing.
        r#"<fieldset class="ss-object" data-field="guest"><legend>Guest</legend>"#,
        r#"<input id="booking-input-guest-name" name="guest.name" type="text" required="" minlength="2""#,
        r#"<legend>Billing address</legend>"#,
        r#"<label for="booking-input-billing-line1">Address line 1</label><input id="booking-input-billing-line1" name="billing.line1" type="text">"#,
        // A new form's one room, which listMin keeps, and the button that
        // adds a second.
        r#"<fieldset class="ss-row"><legend>Room 1</legend><input type="hidden" name="rooms[0]" value="">"#,
        r#"<label for="booking-input-rooms-0-room-id">Room</label><input id="booking-input-rooms-0-room-id" name="rooms[0].roomId" type="text" required="""#,
        r#"name="rooms[0].adults" type="number" required="" step="1" min="1" max="4""#,
        r#"<input type="checkbox" name="rooms[0].extras" value="late_checkout">Late checkout</label>"#,
        r#"<button type="submit" class="ss-add" name="_action" value="add:rooms" formnovalidate="">Add a room</button>"#,
        // A list of enums is a group of checkboxes.
        r#"<fieldset class="ss-choices" data-field="amenities"><legend>Amenities</legend>"#,
        r#"<input type="checkbox" name="amenities" value="wifi">Wifi</label>"#,
        // A date, a time, a date-time read as UTC, a secret, a default and
        // JSON values.
        r#"name="arrival" type="date" required="""#,
        r#"name="checkIn" type="time""#,
        r#"<label for="booking-input-hold-until">Hold until (UTC)</label><input id="booking-input-hold-until" name="holdUntil" type="datetime-local""#,
        r#"name="doorCode" type="password" minlength="4" autocomplete="off""#,
        r#"name="currency" type="text" pattern="^[A-Z]{3}$" value="GBP""#,
        r#"<label for="booking-input-preferences">Preferences (JSON)</label><textarea id="booking-input-preferences" name="preferences" rows="4" spellcheck="false"></textarea>"#,
        r#"name="labels" rows="4""#,
    ] {
        assert!(html.contains(want), "missing {want} in {html}");
    }
    assert!(!html.contains("billing.line1\" type=\"text\" required"), "an optional object's field is required: {html}");
    assert!(!html.contains("remove:"), "a row past listMin can be removed: {html}");
    assert!(!html.contains("rooms[1]"), "{html}");
}

#[tokio::test]
async fn a_valid_nested_post_books_with_the_inputs_json() {
    let (status, headers, html, calls) = book_with(&booking()).await;
    assert_eq!(status, StatusCode::SEE_OTHER, "{html}");
    assert_eq!(headers[header::LOCATION], "/booked");
    let [input] = &calls[..] else { panic!("{calls:?}") };
    assert_eq!(input.rooms[0].room_id, uuid(GARDEN));
    assert_eq!(input.rooms[1].room_id, uuid(ATTIC));
    let mut json = serde_json::to_value(input).unwrap();
    for room in json["rooms"].as_array_mut().unwrap() {
        room.as_object_mut().unwrap().remove("roomId");
    }
    assert_eq!(
        json,
        json!({
            "guest": {"name": "Ada Lovelace", "email": "ada@example.com"},
            "rooms": [{"adults": 2, "extras": ["wifi"]}, {"adults": 1}],
            "amenities": ["wifi", "late_checkout"],
            "arrival": "2026-10-12",
            "checkIn": "15:30",
            "holdUntil": "2026-10-09T14:30:00Z",
            "doorCode": "4321",
            "currency": "GBP",
            "preferences": {"quiet": true},
            "labels": {"vip": "yes"},
        })
    );

    // The currency's default fills in when the form sends none, and an
    // optional object with a field sent is sent.
    let (status, _, html, calls) = book_with(&booking_with(&[
        ("currency", ""),
        ("billing.line1", "1 Analytical Row"),
        ("billing.city", "London"),
    ]))
    .await;
    assert_eq!(status, StatusCode::SEE_OTHER, "{html}");
    assert_eq!(calls[0].currency.as_deref(), Some("GBP"));
    let billing = serde_json::to_value(&calls[0].billing).unwrap();
    assert_eq!(billing, json!({"line1": "1 Analytical Row", "city": "London"}));
}

#[tokio::test]
async fn a_refused_nested_field_renders_at_its_control_as_sent() {
    let (status, _, html, calls) = book_with(&booking_with(&[
        ("guest.name", "A"),
        ("rooms[1].adults", "9"),
        ("billing.line1", "1 Analytical Row"),
    ]))
    .await;
    assert_eq!(status, StatusCode::UNPROCESSABLE_ENTITY, "{html}");
    assert!(calls.is_empty(), "{calls:?}");
    let name = control(&html, "guest.name");
    assert!(name.contains(r#"value="A" aria-invalid="true""#) && name.contains("must be at least 2 characters"), "{name}");
    let adults = control(&html, "rooms[1].adults");
    assert!(adults.contains(r#"value="9" aria-invalid="true""#) && adults.contains("must be at most 4"), "{adults}");
    // The rows below the refused one, and their values, are as sent.
    assert!(control(&html, "rooms[0].adults").contains(r#"value="2""#), "{html}");
    assert!(!control(&html, "rooms[0].adults").contains("aria-invalid"), "{html}");
    assert!(control(&html, "rooms[1].roomId").contains(&format!(r#"value="{ATTIC}""#)), "{html}");
    // A billing address with a field sent needs its others.
    let city = control(&html, "billing.city");
    assert!(city.contains(r#"aria-invalid="true""#) && city.contains("required field"), "{city}");
    assert!(html.contains(r#"<input type="checkbox" name="amenities" value="late_checkout" checked="">"#), "{html}");
    assert!(html.contains(r#"<input type="checkbox" name="rooms[0].extras" value="wifi" checked="">"#), "{html}");
    assert!(html.contains(r#"value="2026-10-09T14:30""#), "{html}");
    assert!(html.contains("{&quot;quiet&quot;: true}") || html.contains(r#"{"quiet": true}"#), "{html}");
    // A secret is never rendered back.
    assert!(!html.contains("4321"), "{html}");

    // A value a control cannot hold is refused at the control, before the
    // input's rules.
    let (status, _, html, _) = book_with(&booking_with(&[
        ("holdUntil", "2026-02-30T10:00"),
        ("preferences", "{quiet"),
        ("rooms[0].adults", "two"),
    ]))
    .await;
    assert_eq!(status, StatusCode::UNPROCESSABLE_ENTITY, "{html}");
    assert!(control(&html, "holdUntil").contains("must be a date and time"), "{html}");
    assert!(control(&html, "preferences").contains("must be JSON"), "{html}");
    assert!(control(&html, "rooms[0].adults").contains("must be a whole number"), "{html}");

    // The operation's refusal of a row's field renders at the field.
    let (status, _, html, calls) = book_with(&booking_with(&[("rooms[1].roomId", BOOKED)])).await;
    assert_eq!((status, calls.len()), (StatusCode::UNPROCESSABLE_ENTITY, 1), "{html}");
    let room = control(&html, "rooms[1].roomId");
    assert!(room.contains(r#"aria-invalid="true""#) && room.contains("is booked"), "{room}");
    assert!(!control(&html, "rooms[0].roomId").contains("is booked"), "{html}");
}

#[tokio::test]
async fn row_buttons_add_and_remove_rows_within_the_lists_bounds() {
    // Adding a room keeps the rooms as sent and calls nothing.
    let (status, _, html, calls) = book_with(&booking_with(&[("_action", "add:rooms")])).await;
    assert_eq!(status, StatusCode::OK, "{html}");
    assert!(calls.is_empty(), "{calls:?}");
    assert!(html.contains("<legend>Room 3</legend>"), "{html}");
    assert!(control(&html, "rooms[0].roomId").contains(&format!(r#"value="{GARDEN}""#)), "{html}");
    assert!(control(&html, "rooms[1].roomId").contains(&format!(r#"value="{ATTIC}""#)), "{html}");
    assert!(html.contains(r#"name="rooms[2].roomId""#), "{html}");
    assert!(html.contains(r#"value="remove:rooms[2]" formnovalidate="" aria-label="Remove Room 3""#), "{html}");
    assert!(!html.contains("aria-invalid"), "a row button renders no errors: {html}");
    assert!(!html.contains("4321"), "{html}");
    // At listMax there is no add button, and adding adds nothing.
    assert!(!html.contains("add:rooms"), "{html}");
    let three = booking_with(&[("rooms[2]", ""), ("rooms[2].roomId", BOOKED), ("rooms[2].adults", "3"), ("_action", "add:rooms")]);
    let (status, _, html, calls) = book_with(&three).await;
    assert_eq!((status, calls.len()), (StatusCode::OK, 0), "{html}");
    assert!(html.contains(r#"name="rooms[2].roomId""#) && !html.contains("rooms[3]"), "{html}");

    // Removing the first room numbers the second as the first.
    let (status, _, html, calls) = book_with(&booking_with(&[("_action", "remove:rooms[0]")])).await;
    assert_eq!((status, calls.len()), (StatusCode::OK, 0), "{html}");
    assert!(control(&html, "rooms[0].roomId").contains(&format!(r#"value="{ATTIC}""#)), "{html}");
    assert!(control(&html, "rooms[0].adults").contains(r#"value="1""#), "{html}");
    assert!(!html.contains("rooms[1]"), "{html}");
    // At listMin there is no remove button, and removing removes nothing.
    assert!(!html.contains("remove:"), "{html}");
    let one: Vec<_> = booking().into_iter().filter(|(name, _)| !name.starts_with("rooms[1]")).chain([("_action", "remove:rooms[0]")]).collect();
    let (status, _, html, calls) = book_with(&one).await;
    assert_eq!((status, calls.len()), (StatusCode::OK, 0), "{html}");
    assert!(control(&html, "rooms[0].roomId").contains(&format!(r#"value="{GARDEN}""#)), "{html}");
}

#[tokio::test]
async fn the_apps_choices_render_a_select() {
    let (status, _, html) = send(http::Request::builder().uri("/book/choices").body(Body::empty()).unwrap()).await;
    assert_eq!(status, StatusCode::OK, "{html}");
    let first = control(&html, "rooms[0].roomId");
    assert!(first.contains(r#"<select id="booking-input-rooms-0-room-id" name="rooms[0].roomId" required="">"#), "{first}");
    assert!(first.contains(&format!(r#"<option value="{GARDEN}">Garden room</option><option value="{ATTIC}">Attic</option>"#)), "{first}");
    // A row's own choices win over the list's.
    let second = control(&html, "rooms[1].roomId");
    assert!(second.contains(&format!(r#"<option value="{ATTIC}">Attic, the last one</option>"#)) && !second.contains("Garden"), "{second}");
    // A value no choice is stays, selected, as sent.
    let third = control(&html, "rooms[2].roomId");
    assert!(third.contains(&format!(r#"<option value="{BOOKED}" selected="">{BOOKED}</option>"#)), "{third}");
    // A field the app names no choices for is the input.
    assert!(control(&html, "guest.name").contains(r#"<input id="booking-input-guest-name""#), "{html}");
}

#[test]
fn a_form_decodes_rows_by_their_indexes() {
    let pairs = [
        ("rooms[7].adults", "1"),
        ("rooms[2].adults", "2"),
        ("rooms[x].adults", "9"),
        ("[0]", "9"),
        ("guest..name", "9"),
        ("guest.name", "Ada"),
        ("amenities", "wifi"),
        ("amenities", ""),
        ("_action", "add:rooms"),
    ]
    .map(|(name, value)| (name.to_owned(), value.to_owned()));
    let form = BookingInputForm::from_pairs(pairs);
    let adults: Vec<_> = form.rooms.iter().map(|room| room.adults.as_deref()).collect();
    assert_eq!(adults, [Some("2"), Some("1")]);
    assert_eq!(form.guest.name.as_deref(), Some("Ada"));
    assert_eq!(form.amenities, ["wifi"]);
    assert_eq!(form.row_action.as_deref(), Some("add:rooms"));
    assert_eq!(form.currency, None, "a post is what it sent, not a new form");
    assert_eq!(BookingInputForm::new().currency.as_deref(), Some("GBP"));

    // A list of values: a blank row is refused at the row, and a row
    // button adds none past listMax.
    let mut note = NoteInputForm::from_pairs([("text", "Hi"), ("tags[0]", "a"), ("tags[1]", "")].map(|(n, v)| (n.to_owned(), v.to_owned())));
    assert_eq!(note.tags, [Some("a".to_owned()), None]);
    let errors = note.parse().unwrap_err();
    assert!(!errors.of("tags[1]").is_empty(), "{errors:?}");
    for _ in 0..3 {
        note.row_action = Some("add:tags".to_owned());
        assert!(note.apply_action());
    }
    assert_eq!(note.tags.len(), 3);
    note.row_action = Some("remove:tags[0]".to_owned());
    assert!(note.apply_action());
    assert_eq!(note.tags, [None, None]);
    assert!(!note.apply_action(), "an action applies once");
}

#[test]
fn a_date_time_is_read_as_utc() {
    let form = |value: &str| BookingInputForm { hold_until: Some(value.to_owned()), ..BookingInputForm::default() };
    let hold = |value: &str| form(value).parse().map(|input| input.hold_until.map(|at| at.to_string())).map_err(|errors| errors.of("holdUntil"));
    // parse refuses the rest of the empty form, so only holdUntil's errors
    // count here.
    for (sent, want) in [
        ("2026-10-09T14:30", "2026-10-09T14:30:00Z"),
        ("2026-10-09T14:30:15.5", "2026-10-09T14:30:15.5Z"),
        ("2026-10-09T14:30:00+01:00", "2026-10-09T13:30:00Z"),
        ("2024-02-29T00:00Z", "2024-02-29T00:00:00Z"),
    ] {
        let mut booking = BookingInputForm::from_pairs(booking().into_iter().map(|(n, v)| (n.to_owned(), v.to_owned())));
        booking.hold_until = Some(sent.to_owned());
        let input = booking.parse().unwrap_or_else(|errors| panic!("{sent}: {errors:?}"));
        assert_eq!(input.hold_until.map(|at| at.to_string()).as_deref(), Some(want), "{sent}");
    }
    for sent in ["2026-02-29T10:00", "2026-10-09 14:30", "2026-10-09T24:00", "2026-10-09T14:30+1", "0999-01-01T00:00", "2026-1０-09T14:30"] {
        assert_eq!(hold(sent).unwrap_err(), ["must be a date and time"], "{sent}");
    }
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

// controlsAppTest is tests/app.rs of fixture-controls-api's Topcoat crate.
const controlsAppTest = `use std::collections::HashMap;
use std::net::SocketAddr;
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
use topcoat::router::{header, page, to_bytes, Body, RemoteAddr, Router, RouterBuilderDiscoverExt, StatusCode};
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
        Ok(types::Receipt { event_id: args.input.event_id })
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
    post_as(router, uri, body, length, None).await
}

// A post from a client at ip, as Topcoat's server records the connection's
// address; none when ip is.
async fn post_as(
    router: &Router,
    uri: &str,
    body: String,
    length: Option<usize>,
    ip: Option<[u8; 4]>,
) -> (StatusCode, http::HeaderMap, String) {
    let mut request = http::Request::builder().method("POST").uri(uri).header(header::CONTENT_TYPE, "application/json");
    if let Some(length) = length {
        request = request.header(header::CONTENT_LENGTH, length);
    }
    if let Some(ip) = ip {
        request = request.extension(RemoteAddr(SocketAddr::from((ip, 40000))));
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
async fn each_client_of_a_procedure_has_its_own_budget() {
    let router = app(false);
    let (ada, bob) = (Some([10, 0, 0, 1]), Some([10, 0, 0, 2]));
    for _ in 0..2 {
        let (_, _, body) = post_as(&router, PLACE_ORDER, no_arguments(0), None, ada).await;
        assert!(body.contains(r#""ok""#), "{body}");
    }
    let (status, headers, body) = post_as(&router, PLACE_ORDER, no_arguments(0), None, ada).await;
    assert_eq!(status, StatusCode::OK, "{body}");
    assert!(body.contains(r#""err""#) && body.contains(r#""code":"too_many_requests""#) && body.contains(r#""v":"429""#), "{body}");
    assert!(headers.contains_key(header::RETRY_AFTER), "{headers:?}");
    // Another client's budget is its own.
    let (_, _, body) = post_as(&router, PLACE_ORDER, no_arguments(0), None, bob).await;
    assert!(body.contains(r#""ok""#), "{body}");
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

// A post to the JSON API from a client at ip, as Topcoat's server records
// the connection's address; none when ip is.
async fn post_from(router: &Router, uri: &str, ip: Option<[u8; 4]>) -> StatusCode {
    let mut request = http::Request::builder().method("POST").uri(uri);
    if let Some(ip) = ip {
        request = request.extension(RemoteAddr(SocketAddr::from((ip, 40000))));
    }
    router.handle(request.body(Body::empty()).unwrap()).await.status()
}

#[tokio::test]
async fn each_client_of_a_json_api_route_has_its_own_budget() {
    let router = app(false);
    let (ada, bob) = (Some([10, 0, 0, 1]), Some([10, 0, 0, 2]));
    for _ in 0..2 {
        assert_eq!(post_from(&router, "/api/orders", ada).await, StatusCode::OK);
    }
    assert_eq!(post_from(&router, "/api/orders", ada).await, StatusCode::TOO_MANY_REQUESTS);
    assert_eq!(post_from(&router, "/api/orders", bob).await, StatusCode::OK);
    // Clients Topcoat has no address for share the runtime's fallback
    // bucket, apart from the clients it knows.
    for _ in 0..2 {
        assert_eq!(post_from(&router, "/api/orders", None).await, StatusCode::OK);
    }
    assert_eq!(post_from(&router, "/api/orders", None).await, StatusCode::TOO_MANY_REQUESTS);
    assert_eq!(post_from(&router, "/api/orders", bob).await, StatusCode::OK);
}

#[tokio::test]
async fn a_procedure_answers_its_routes_timeout_as_a_problem() {
    let (status, _, body) = post(&app(true), PLACE_ORDER, no_arguments(0), None).await;
    assert_eq!(status, StatusCode::OK, "{body}");
    assert!(body.contains(r#""err""#) && body.contains(r#""code":"gateway_timeout""#) && body.contains(r#""v":"504""#), "{body}");
}
`

// identityAppTest is tests/app.rs of fixture-user-routes-api's Topcoat
// crate, beside tests/identity.sql, the DDL of fixture-user-model-db that
// the identity runtimes' stores run against (runtime/http/testdata).
const identityAppTest = `use std::sync::Arc;
use std::time::SystemTime;

use async_trait::async_trait;
use schemas_fixture_user_routes_api_topcoat::api::runtime::identity::{
    hash_password, Config, IdentityAuthenticator, NewUser, Rusqlite, SqlStore, Store, HOST_COOKIE_NAME,
};
use schemas_fixture_user_routes_api_topcoat::api::runtime::{ApiError, Principal, RequestContext};
use schemas_fixture_user_routes_api_topcoat::api::{identity, types, GreetingImplementation, Implementations};
use schemas_fixture_user_routes_api_topcoat::{operations, IdentityPageAuthenticator, RouterBuilderFixtureUserRoutesApiExt};
use serde_json::{json, Value};
use topcoat::context::Cx;
use topcoat::router::{page, to_bytes, Body, OriginPolicy, Router, RouterBuilderDiscoverExt, StatusCode};
use topcoat::view::{view, View};

const CONFIG: &str = r#"{"password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}"#;
const PASSWORD: &str = "correct horse";

struct Greetings;

fn name_of(principal: &Principal) -> String {
    principal.claims.get("name").and_then(Value::as_str).unwrap_or_default().to_string()
}

#[async_trait]
impl GreetingImplementation for Greetings {
    async fn greet(&self, ctx: RequestContext) -> Result<types::Greeting, ApiError> {
        Ok(types::Greeting { message: format!("Hello, {}", ctx.principal.as_ref().map(name_of).unwrap_or_default()) })
    }
}

// The API over a SQLite database of the authDb's tables: an administrator
// holding the identity permissions, and a member holding none.
async fn implementations() -> Implementations {
    let connection = rusqlite::Connection::open_in_memory().unwrap();
    connection.execute_batch(include_str!("identity.sql")).unwrap();
    let store: Arc<SqlStore> = Arc::new(identity::store(Rusqlite::new(connection).unwrap()).unwrap());
    let config = Config::parse(CONFIG.as_bytes()).unwrap();
    let hash = hash_password(PASSWORD, config.argon2_params()).unwrap();
    let user = |login: &str, name: &str| NewUser {
        login: login.to_string(),
        name: name.to_string(),
        password_hash: hash.clone(),
        at: SystemTime::now(),
    };
    let admin = store.create_user(user("admin@example.com", "Admin")).await.unwrap();
    store.create_user(user("member@example.com", "Member")).await.unwrap();
    let role = store.create_role("admin", &["identity".to_string()]).await.unwrap();
    store.grant_role(&admin.id, &role.id, SystemTime::now()).await.unwrap();
    let service = Arc::new(identity::service(store, config).unwrap());
    Implementations { greeting: Arc::new(Greetings), authenticator: Arc::new(IdentityAuthenticator::new(service)) }
}

async fn greet(cx: &Cx) -> String {
    match operations::greeting_greet(cx).await {
        Ok(greeting) => greeting.message,
        Err(err) => err.to_string(),
    }
}

#[page("/greeting")]
async fn greeting_page(cx: &Cx) -> topcoat::Result<impl View> {
    let said = greet(cx).await;
    Ok(view! { <p>(said)</p> })
}

#[page(POST "/greet")]
async fn greet_page(cx: &Cx) -> topcoat::Result<impl View> {
    let said = greet(cx).await;
    Ok(view! { <p>(said)</p> })
}

#[page("/users")]
async fn users_page(cx: &Cx) -> topcoat::Result<impl View> {
    let said = match operations::can_account_admin_list_users(cx).await {
        Ok(caller) => format!("users readable by {}", caller.as_ref().map(name_of).unwrap_or_default()),
        Err(err) => err.to_string(),
    };
    Ok(view! { <p>(said)</p> })
}

// The app's pages read a caller from the identity service the JSON API
// authenticates with. Topcoat's origin policy trusts the origins given.
async fn app(trusted: &[&str]) -> Router {
    let implementations = implementations().await;
    let pages = IdentityPageAuthenticator::of(&implementations);
    Router::builder()
        .origin_policy(OriginPolicy::new().trust_origins(trusted.iter().copied()))
        .discover()
        .fixture_user_routes_api(implementations, pages)
        .build()
}

struct Reply {
    status: StatusCode,
    cookies: Vec<String>,
    body: String,
}

async fn send(router: &Router, method: &str, uri: &str, headers: &[(&str, &str)], body: Option<Value>) -> Reply {
    let mut request = http::Request::builder().method(method).uri(uri).header("host", "app.example.com");
    for (name, value) in headers {
        request = request.header(*name, *value);
    }
    let body = body.map_or_else(Body::empty, |body| Body::from(body.to_string()));
    let response = router.handle(request.body(body).unwrap()).await;
    let status = response.status();
    let cookies = response
        .headers()
        .get_all("set-cookie")
        .iter()
        .map(|value| value.to_str().unwrap().to_string())
        .collect();
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    Reply { status, cookies, body: String::from_utf8(bytes.to_vec()).unwrap() }
}

// Signs in through the mounted JSON API with a cookie session, and answers
// the Cookie header that carries it.
async fn sign_in(router: &Router, login: &str) -> String {
    let credentials = json!({"login": login, "password": PASSWORD, "session": "cookie"});
    let reply = send(router, "POST", "/api/auth/login", &[("sec-fetch-site", "same-origin")], Some(credentials)).await;
    assert_eq!(reply.status, StatusCode::OK, "{}", reply.body);
    let cookie = reply.cookies.first().expect("the session cookie").split(';').next().unwrap().to_string();
    assert!(cookie.starts_with(&format!("{HOST_COOKIE_NAME}=")), "{cookie}");
    cookie
}

#[tokio::test]
async fn a_page_admits_the_user_the_api_signed_in() {
    let router = app(&[]).await;
    let body = send(&router, "GET", "/greeting", &[], None).await.body;
    assert!(body.contains("401 unauthorized: Authentication required"), "{body}");

    let admin = sign_in(&router, "admin@example.com").await;
    let reply = send(&router, "GET", "/greeting", &[("cookie", &admin)], None).await;
    assert_eq!(reply.status, StatusCode::OK);
    assert!(reply.body.contains("Hello, Admin"), "{}", reply.body);
    assert!(send(&router, "GET", "/users", &[("cookie", &admin)], None).await.body.contains("users readable by Admin"));

    let member = sign_in(&router, "member@example.com").await;
    let body = send(&router, "GET", "/users", &[("cookie", &member)], None).await.body;
    assert!(body.contains("403 forbidden: Insufficient permissions"), "{body}");

    // A bearer session the API's login answered signs a page in too.
    let credentials = json!({"login": "member@example.com", "password": PASSWORD});
    let reply = send(&router, "POST", "/api/auth/login", &[], Some(credentials)).await;
    let token: Value = serde_json::from_str(&reply.body).unwrap();
    let bearer = format!("Bearer {}", token["data"]["token"].as_str().unwrap());
    let body = send(&router, "GET", "/greeting", &[("authorization", &bearer)], None).await.body;
    assert!(body.contains("Hello, Member"), "{body}");
}

#[tokio::test]
async fn a_page_refuses_another_sites_cookie_request() {
    // Topcoat's origin policy refuses another site's POST before a page
    // runs.
    let router = app(&[]).await;
    let admin = sign_in(&router, "admin@example.com").await;
    let cross_site = [("cookie", admin.as_str()), ("origin", "https://evil.example.com"), ("sec-fetch-site", "cross-site")];
    assert_eq!(send(&router, "POST", "/greet", &cross_site, None).await.status, StatusCode::FORBIDDEN);

    // An origin the app's policy trusts and the identity config does not:
    // the page's cookie is refused as the JSON API refuses it.
    let router = app(&["https://partner.example.com"]).await;
    let admin = sign_in(&router, "admin@example.com").await;
    let partner = [("cookie", admin.as_str()), ("origin", "https://partner.example.com"), ("sec-fetch-site", "cross-site")];
    let body = send(&router, "POST", "/greet", &partner, None).await.body;
    assert!(body.contains("403 cross_origin: Cross-origin request refused"), "{body}");
    let reply = send(&router, "POST", "/api/auth/logout", &partner, None).await;
    assert_eq!(reply.status, StatusCode::FORBIDDEN, "{}", reply.body);
    assert!(reply.body.contains(r#""code":"cross_origin""#), "{}", reply.body);

    let same_site = [("cookie", admin.as_str()), ("sec-fetch-site", "same-origin")];
    let body = send(&router, "POST", "/greet", &same_site, None).await.body;
    assert!(body.contains("Hello, Admin"), "{body}");
}

#[tokio::test]
async fn a_page_clears_the_cookie_of_an_ended_session() {
    let router = app(&[]).await;
    let admin = sign_in(&router, "admin@example.com").await;
    let reply = send(&router, "POST", "/api/auth/logout", &[("cookie", &admin), ("sec-fetch-site", "same-origin")], None).await;
    assert_eq!(reply.status, StatusCode::OK, "{}", reply.body);

    let reply = send(&router, "GET", "/greeting", &[("cookie", &admin)], None).await;
    assert!(reply.body.contains("401 unauthorized: Authentication required"), "{}", reply.body);
    let cleared = format!("{HOST_COOKIE_NAME}=; Path=/; Max-Age=0");
    assert!(reply.cookies.iter().any(|cookie| cookie.starts_with(&cleared)), "{:?}", reply.cookies);
}
`
