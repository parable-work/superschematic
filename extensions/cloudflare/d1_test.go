package cloudflare_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"

	"github.com/parable-work/superschematic/extensions/cloudflare"
)

// The D1 platform (docs/stack-model.md, sections 18.2 and 18.6, D64).

// The services of the D1 fixture: reviews, a TypeScript API on
// reviews-db; reviews-db and notes-db list sqlite first, and ledger-db
// lists postgres alone.
var (
	reviewsAPI = ir.ServiceRef{Name: "reviews", Kind: ir.SchemaKindAPI}
	reviewsDB  = ir.ServiceRef{Name: "reviews-db", Kind: ir.SchemaKindDB}
	notesDB    = ir.ServiceRef{Name: "notes-db", Kind: ir.SchemaKindDB}
)

func d1Services() []stack.Service {
	return []stack.Service{
		{Name: "reviews-db", Kind: ir.SchemaKindDB, Dialects: []string{"sqlite", "postgres"}},
		{Name: "notes-db", Kind: ir.SchemaKindDB, Dialects: []string{"sqlite", "postgres"}},
		{Name: "ledger-db", Kind: ir.SchemaKindDB},
		{
			Name: "reviews", Kind: ir.SchemaKindAPI, Language: registry.APILanguageTypeScript,
			Dependencies: []ir.ServiceRef{reviewsDB},
			Operations:   []stack.Operation{{Name: "Reviews.listReviews"}, {Name: "Reviews.writeReview"}},
		},
	}
}

// d1Stack deploys reviews, on Workers and exposed at workers.dev, on
// reviews-db, and a database Archive hosting notes-db, on the cloudflare
// target: Staging places reviews-db's primary in eastern North America,
// Production keeps both in the EU, and Preview extends Staging with a
// parameter, so each member gets a Worker and databases of its own.
func d1Stack() *ir.Stack {
	of := func(ref ir.ServiceRef) ir.DeployableRef { return ir.DeployableRef{Service: &ref} }
	return &ir.Stack{
		Name:   "reviews-stack",
		Deploy: []ir.ServiceRef{reviewsAPI, reviewsDB},
		Expose: []ir.DeployableRef{of(reviewsAPI)},
		Deployables: []*ir.DeployableDecl{{
			Name:  "Archive",
			Kind:  ir.DeployableDatabase,
			Hosts: []ir.ServiceRef{notesDB},
		}},
		Environments: []*ir.Environment{
			{
				Name:     "Staging",
				Target:   cloudflare.Target,
				Values:   map[string]any{"accountId": accountID, "workersSubdomain": "acme"},
				Settings: []*ir.DeployableSettings{{Of: of(reviewsDB), Values: map[string]any{"locationHint": "enam"}}},
			},
			{
				Name:   "Production",
				Target: cloudflare.Target,
				Values: map[string]any{"accountId": accountID, "workersSubdomain": "acme"},
				Settings: []*ir.DeployableSettings{
					{Of: of(reviewsDB), Values: map[string]any{"jurisdiction": "eu"}},
					{Of: ir.DeployableRef{Deployable: "Archive"}, Values: map[string]any{"jurisdiction": "eu"}},
				},
			},
			{Name: "Preview", Extends: "Staging", Parameters: []string{"pr"}},
		},
	}
}

// TestD1Golden resolves the D1 fixture in each environment and checks its
// environment.json against the golden file: a d1Database per hosted DB
// schema, named <stack>-<env>-<db> with the parameter's value appended in
// Preview, in sqlite, with the settings' location hint or jurisdiction.
// Resolution validated each node against the pinned d1Database.
func TestD1Golden(t *testing.T) {
	reg := assemble(t)
	s := d1Stack()
	for _, env := range s.Environments {
		t.Run(env.Name, func(t *testing.T) {
			resolved, err := stack.Resolve(reg, stack.Input{Stack: s, Services: d1Services(), Environment: env.Name})
			if err != nil {
				t.Fatal(err)
			}
			got, err := stack.Marshal(resolved)
			if err != nil {
				t.Fatal(err)
			}
			golden := stack.EnvironmentPath(goldenRoot, s.Name, env.Name)
			if *update {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run with -update to write it)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("environment.json differs from %s; run with -update and review the diff", golden)
			}
			for _, d := range resolved.Deployables {
				if d.Kind == ir.DeployableDatabase && (d.Platform != cloudflare.D1Platform || d.Dialect != "sqlite") {
					t.Errorf("database %s is on %s in %s, want cloudflare.d1 in sqlite", d.Name, d.Platform, d.Dialect)
				}
			}
		})
	}
}

// TestAWorkerBindsItsD1Database: the sql edge from reviews' Worker to
// reviews-db goes through cloudflare.workers-d1, which derives
// `d1: {binding, database}`, the binding named after the field and the
// database the uuid output of its node; the Worker's version holds a d1
// binding of that name and id, and the variables the runtime's
// loadDatabase reads as plain text (section 18.2). No other binding or
// node grants the Worker the database: D1 has no roles.
func TestAWorkerBindsItsD1Database(t *testing.T) {
	reg := assemble(t)
	env, err := stack.Resolve(reg, stack.Input{Stack: d1Stack(), Services: d1Services(), Environment: "Staging"})
	if err != nil {
		t.Fatal(err)
	}
	var edge *ir.Edge
	for _, e := range env.Edges {
		if e.ID == ir.EdgeID(ir.EdgeSQL, "reviews", "reviews-db") {
			edge = e
		}
	}
	if edge == nil || edge.Connector != cloudflare.D1Connector || edge.Field != "REVIEWS_DB_DATABASE" {
		t.Fatalf("the sql edge = %+v, want it through %s filling REVIEWS_DB_DATABASE", edge, cloudflare.D1Connector)
	}
	version := env.Resources.Resource("reviews.version")
	if version == nil {
		t.Fatal("reviews has no Worker version")
	}
	uuid := ir.Output{Resource: "reviews-db.d1.reviews-db", Name: "uuid"}
	want := map[string]map[string]any{
		"REVIEWS_DB_DATABASE":             {"type": "d1", "name": "REVIEWS_DB_DATABASE", "id": uuid},
		"REVIEWS_DB_DATABASE_D1_BINDING":  {"type": "plain_text", "name": "REVIEWS_DB_DATABASE_D1_BINDING", "text": "REVIEWS_DB_DATABASE"},
		"REVIEWS_DB_DATABASE_D1_DATABASE": {"type": "plain_text", "name": "REVIEWS_DB_DATABASE_D1_DATABASE", "text": uuid},
	}
	bindings, _ := version.Properties["bindings"].([]any)
	got := map[string]map[string]any{}
	for _, b := range bindings {
		m := b.(map[string]any)
		got[m["name"].(string)] = m
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the version's bindings = %v, want %v", got, want)
	}
}

// TestD1Refusals: D1 runs SQLite alone, so a database a setting places on
// it whose DB service lists no sqlite is refused, while one that lists
// postgres first goes on Hyperdrive by default (D92); D1 ignores a location
// hint under a
// jurisdiction, so a database that sets both is refused; and a setting D1
// has no value for is refused by the settings' schema.
func TestD1Refusals(t *testing.T) {
	reg := assemble(t)
	for name, tc := range map[string]struct {
		edit func(*ir.Stack)
		want string
	}{
		"postgres alone": {
			edit: func(s *ir.Stack) {
				ledger := ir.ServiceRef{Name: "ledger-db", Kind: ir.SchemaKindDB}
				s.Deploy = append(s.Deploy, ledger)
				s.Environments[0].Settings = append(s.Environments[0].Settings, &ir.DeployableSettings{
					Of: ir.DeployableRef{Service: &ledger}, Platform: cloudflare.D1Platform,
				})
			},
			want: "database ledger-db hosts ledger-db (postgres), and platform cloudflare.d1 runs only sqlite",
		},
		"a hint under a jurisdiction": {
			edit: func(s *ir.Stack) {
				s.Environments[0].Settings[0].Values["jurisdiction"] = "eu"
			},
			want: "database reviews-db sets locationHint enam and jurisdiction eu; D1 places a database under a jurisdiction by the jurisdiction alone and ignores the hint, so set one",
		},
		"an unknown hint": {
			edit: func(s *ir.Stack) { s.Environments[0].Settings[0].Values["locationHint"] = "mars" },
			want: "settings of reviews-db on platform cloudflare.d1",
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := d1Stack()
			tc.edit(s)
			_, err := stack.Resolve(reg, stack.Input{Stack: s, Services: d1Services(), Environment: "Staging"})
			var errs *stack.Errors
			if !errors.As(err, &errs) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Resolve = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

// TestD1Name: a D1 database is named after its stack, its environment and
// its DB schema, in kebab case with single hyphens.
func TestD1Name(t *testing.T) {
	for _, tc := range []struct{ stack, env, service, want string }{
		{"AcmeReviews", "Staging", "reviews-db", "acme-reviews-staging-reviews-db"},
		{"acme_reviews", "StagingEu", "reviews_db", "acme-reviews-staging-eu-reviews-db"},
		{"acme--reviews-", "Prod2", "reviews-db", "acme-reviews-prod2-reviews-db"},
	} {
		if got := cloudflare.D1Name(tc.stack, tc.env, tc.service); got != tc.want {
			t.Errorf("D1Name(%s, %s, %s) = %q, want %q", tc.stack, tc.env, tc.service, got, tc.want)
		}
	}
}

// TestD1CIGolden renders the D1 fixture's workflow, its Staging and its
// Preview, with the core's GitHub Actions renderer, and checks it against
// the golden file, which -update rewrites: the cloudflare target migrates
// D1 from the deploying machine, so each deploy job installs
// superschematic-migrate from the release beside superschematic, checked
// against SHA256SUMS, and no plan job does.
func TestD1CIGolden(t *testing.T) {
	reg := assemble(t)
	renderer, ok := reg.CIRenderer("github")
	if !ok {
		t.Fatal("the core registers no github CI renderer")
	}
	s := d1Stack()
	var envs []registry.CIEnvironment
	for _, name := range []string{"Staging", "Preview"} {
		resolved, err := stack.Resolve(reg, stack.Input{Stack: s, Services: d1Services(), Environment: name})
		if err != nil {
			t.Fatal(err)
		}
		env := reg.CIEnvironment(resolved)
		if !env.Migrate {
			t.Errorf("%s's CI does not install the migration runner", name)
		}
		envs = append(envs, env)
	}
	files, err := renderer.Render(registry.CIRequest{
		Stack: registry.CIStack{
			Name:           s.Name,
			Dir:            "schemas/services/" + s.Name,
			ServicesRoot:   "schemas/services",
			SchemasRoot:    "schemas",
			OutputRoot:     "schemas/dist",
			PackageManager: registry.PackageManagerBun,
		},
		Environments: envs,
		Options:      registry.CIOptions{}.WithDefaults(renderer),
		Version:      "1.2.3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %+v", files)
	}
	golden := filepath.Join(goldenRoot, "ci", files[0].Path)
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, files[0].Data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if !bytes.Equal(files[0].Data, want) {
		t.Errorf("the workflow differs from %s; run with -update and review the diff", golden)
	}
	if n := strings.Count(string(want), "name: Install superschematic-migrate 1.2.3"); n != 2 {
		t.Errorf("%d jobs install the migration runner, want the two deploy jobs", n)
	}
	if bin, err := exec.LookPath("actionlint"); err == nil {
		if out, err := exec.Command(bin, "-no-color", golden).CombinedOutput(); err != nil {
			t.Errorf("actionlint %s: %v\n%s", golden, err, out)
		}
	}
}
