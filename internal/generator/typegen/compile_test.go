package typegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/behaviortypes/behaviortypestest"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/identitydesc/identitytest"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// TestGeneratedModulesCompile generates the Go type modules for the fixture
// services into a temp tree wired against the real superscalar module and
// runs `go build` on each. This is the cheap end-to-end compile check for
// the typegen port.
func TestGeneratedModulesCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}

	paths := testpaths.Local(t)

	tempRoot := t.TempDir()
	typesGoDir := filepath.Join(tempRoot, "types", "go")

	fixedClock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

	dbSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatalf("load fixture-db: %v", err)
	}
	apiSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-api"))
	if err != nil {
		t.Fatalf("load fixture-api: %v", err)
	}
	userModelSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-user-model-db"))
	if err != nil {
		t.Fatalf("load fixture-user-model-db: %v", err)
	}

	behaviorsSchema, behaviorsRegistry := loadFixture(t, behaviortypestest.Service)

	cases := []struct {
		name     string
		schema   *ir.Schema
		deps     map[string]*ir.Schema
		registry *registry.Registry
	}{
		{name: "fixture-db", schema: dbSchema},
		{name: "fixture-api", schema: apiSchema, deps: map[string]*ir.Schema{"fixture-db": dbSchema}},
		// A User table adds identity.go, which declares IdentityDescriptor.
		{name: identitytest.Service, schema: identitytest.Schema(t, identitytest.Options{Roles: true, Name: "displayName"})},
		{name: "fixture-user-model-db", schema: userModelSchema},
		// behaviors.go: each behavior's fields (D58).
		{name: behaviortypestest.Service, schema: behaviorsSchema, registry: behaviorsRegistry},
	}

	for _, tc := range cases {
		depModules := map[string]string{}
		for depName := range tc.deps {
			depModules[depName] = "example.com/schemas/types/go/" + depName
		}

		output, err := Generate(tc.schema, Options{
			SchemaName:        tc.name,
			ModulePath:        "example.com/schemas/types/go/" + tc.name,
			Dependencies:      tc.deps,
			DependencyModules: depModules,
			Registry:          tc.registry,
			Clock:             fixedClock,
		})
		if err != nil {
			t.Fatalf("generate %s: %v", tc.name, err)
		}

		outDir := filepath.Join(typesGoDir, tc.name)
		if err := SetReplacePaths(output, paths, outDir); err != nil {
			t.Fatalf("set replace paths for %s: %v", tc.name, err)
		}
		if err := WriteTypes(output, outDir); err != nil {
			t.Fatalf("write %s: %v", tc.name, err)
		}
	}

	for _, tc := range cases {
		outDir := filepath.Join(typesGoDir, tc.name)

		tidy := exec.Command("go", "mod", "tidy")
		tidy.Dir = outDir
		if out, err := tidy.CombinedOutput(); err != nil {
			t.Skipf("go mod tidy failed for %s (likely offline): %v\n%s", tc.name, err, out)
		}

		build := exec.Command("go", "build", "./...")
		build.Dir = outDir
		if out, err := build.CombinedOutput(); err != nil {
			t.Errorf("generated module %s does not compile: %v\n%s", tc.name, err, out)
		}
	}

	// TicketBehaviors decodes a read's behaviors and writes them back
	// unchanged, each behavior under its exact name.
	outDir := filepath.Join(typesGoDir, behaviortypestest.Service)
	if err := os.WriteFile(filepath.Join(outDir, "behaviors_test.go"), []byte(goBehaviorsRoundTrip), 0o644); err != nil {
		t.Fatal(err)
	}
	test := exec.Command("go", "test", "./...")
	test.Dir = outDir
	if out, err := test.CombinedOutput(); err != nil {
		t.Errorf("TicketBehaviors does not round-trip a read: %v\n%s", err, out)
	}
}

// goBehaviorsRoundTrip is a test of the generated module: TicketBehaviors
// holds behaviortypestest.TicketBehaviors, typed, and writes it back.
var goBehaviorsRoundTrip = `package types

import (
	"encoding/json"
	"reflect"
	"testing"
)

const read = ` + "`" + behaviortypestest.TicketBehaviors + "`" + `

func TestTicketBehaviorsRoundTrip(t *testing.T) {
	var b TicketBehaviors
	if err := json.Unmarshal([]byte(read), &b); err != nil {
		t.Fatal(err)
	}
	if b.Workflow.Status != TicketStateInProgress || b.Comments.Messages != 2 || b.Comments.Threads.Open != 1 || *b.Revisions.Revision != 3 {
		t.Fatalf("decoded %+v", b)
	}
	if b.Links.Targets.Project.Id != "launch" || len(b.Links.Targets.Reviewers) != 2 || !*b.Links.Targets.Spec.Stale {
		t.Fatalf("decoded links %+v", b.Links)
	}
	if b.Timers.Deadlines.Close != nil || *b.Timers.Deadlines.Respond.Outcome != "moved" {
		t.Fatalf("decoded timers %+v", b.Timers)
	}
	if string(b.AcmeRating.RatingCount) != "4" || b.Approvals.Gates.Done.SignedBy[0] != "ana" {
		t.Fatalf("decoded %+v", b)
	}
	encoded, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal([]byte(read), &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("wrote %s", encoded)
	}
}
`
