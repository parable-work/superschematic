package gosdkgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/verify"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// agentsService is the API that serves fixture-agents-db's Job's, Session's
// and Account's behavior operations, those of the Lease, Retries, Presence
// and Budget they implement among them (D104).
const agentsService = "fixture-agents-api"

// loadAgentsAPI loads fixture-agents-api and its database, and generates
// its API output.
func loadAgentsAPI(t *testing.T) (*ir.Schema, *ir.Schema, *apigen.APIOutput) {
	t.Helper()
	load := func(name string) *ir.Schema {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, name), loader.WithWarnings(func(*verify.Diagnostic) {}))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		return schema
	}
	schema, db := load(agentsService), load("fixture-agents-db")
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   agentsService,
		ModulePath:   "example.com/schemas/api/" + agentsService,
		TypesModule:  "example.com/schemas/types/go/" + agentsService,
		Dependencies: map[string]*ir.Schema{"fixture-agents-db": db},
		Clock:        nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return schema, db, apiOutput
}

// TestAgentsSDKGolden pins every file of the Go SDK for fixture-agents-api:
// the operations of each use Job, Session and Account implement are methods
// of a struct of their own, the field of the namespace named for the use's
// member (JobNamespace.Lease.Heartbeat, AccountNamespace.Budget.Observe),
// beside each status's; every Job operation's input presents
// JobPreconditions, whose member Lease is JobLeasePrecondition. Regenerate
// with:
// go test ./internal/generator/gosdkgen -run TestAgentsSDKGolden -update
func TestAgentsSDKGolden(t *testing.T) {
	_, _, apiOutput := loadAgentsAPI(t)
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk", "go", agentsService)
	typesDir := filepath.Join(root, "types", "go", agentsService)
	if err := os.MkdirAll(typesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sdkOutput, err := Generate(apiOutput, "example.com/schemas/sdk/go/"+agentsService, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	compareGoldenTree(t, sdkDir, filepath.Join("testdata", "golden", agentsService))

	var got []string
	for _, ns := range sdkOutput.Namespaces {
		for _, member := range ns.Members {
			for _, ep := range member.Endpoints {
				got = append(got, ns.StructName+"."+member.FieldName+" *"+member.StructName+"."+ep.MethodName)
			}
		}
	}
	for _, want := range []string{
		"JobNamespace.Lease *JobLeaseOperations.Heartbeat",
		"JobNamespace.Retries *JobRetriesOperations.RecordAttempt",
		"JobNamespace.Status *JobStatusOperations.Transition",
		"SessionNamespace.Presence *SessionPresenceOperations.Beat",
		"AccountNamespace.Budget *AccountBudgetOperations.Observe",
	} {
		if !strings.Contains(strings.Join(got, "; "), want) {
			t.Errorf("members = %v, want %s among them", got, want)
		}
	}
	if len(got) != 22 {
		t.Errorf("members = %d (%v), want 22", len(got), got)
	}
}

// TestAgentsSDKBuilds builds and vets the Go SDK of fixture-agents-api over
// the types modules of the API and its database: JobPreconditions' member
// Lease, an IR field whose name starts with a capital, among them.
func TestAgentsSDKBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	schema, db, apiOutput := loadAgentsAPI(t)
	paths := testpaths.Local(t)
	root := t.TempDir()
	for _, tc := range []struct {
		name   string
		schema *ir.Schema
		deps   map[string]*ir.Schema
	}{
		{"fixture-agents-db", db, nil},
		{agentsService, schema, map[string]*ir.Schema{"fixture-agents-db": db}},
	} {
		modules := map[string]string{}
		for dep := range tc.deps {
			modules[dep] = "example.com/schemas/types/go/" + dep
		}
		output, err := typegen.Generate(tc.schema, typegen.Options{
			SchemaName:        tc.name,
			ModulePath:        "example.com/schemas/types/go/" + tc.name,
			Dependencies:      tc.deps,
			DependencyModules: modules,
			Registry:          registry.New(naming.Default()),
			Clock:             nestedArraysClock,
		})
		if err != nil {
			t.Fatalf("typegen.Generate %s: %v", tc.name, err)
		}
		dir := filepath.Join(root, "types", "go", tc.name)
		if err := typegen.SetReplacePaths(output, paths, dir); err != nil {
			t.Fatal(err)
		}
		if err := typegen.WriteTypes(output, dir); err != nil {
			t.Fatal(err)
		}
	}
	types, err := os.ReadFile(filepath.Join(root, "types", "go", agentsService, "types.go"))
	if err != nil {
		matches, _ := filepath.Glob(filepath.Join(root, "types", "go", agentsService, "*.go"))
		for _, match := range matches {
			if data, readErr := os.ReadFile(match); readErr == nil && strings.Contains(string(data), "type JobPreconditions struct") {
				types, err = data, nil
			}
		}
	}
	if err != nil || !regexp.MustCompile("\tLease InputField\\[\\*JobLeasePrecondition\\] `json:\"Lease,").Match(types) {
		t.Errorf("the API's Go types render JobPreconditions' member Lease as %q, want the field Lease tagged Lease (%v)", excerpt(string(types), "type JobPreconditions struct"), err)
	}
	sdkDir := filepath.Join(root, "sdk", "go", agentsService)
	sdkOutput, err := Generate(apiOutput, "example.com/schemas/sdk/go/"+agentsService, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, filepath.Join(root, "types", "go", agentsService), nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	for _, args := range [][]string{{"mod", "tidy"}, {"build", "./..."}, {"vet", "./..."}} {
		cmd := exec.Command("go", args...)
		cmd.Dir = sdkDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s in the generated SDK: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}

// excerpt is the text of source from the line that holds marker, a few
// lines of it.
func excerpt(source, marker string) string {
	at := strings.Index(source, marker)
	if at < 0 {
		return ""
	}
	lines := strings.SplitN(source[at:], "\n", 6)
	return strings.Join(lines[:len(lines)-1], "\n")
}
