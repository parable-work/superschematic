package cloudflare_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/stack"

	"github.com/parable-work/superschematic/extensions/cloudflare"
)

// The D1 platform (docs/stack-model.md, sections 18.2 and 18.6, D64).

// The DB services of the D1 fixture: reviews-db and notes-db list sqlite
// first, and ledger-db lists postgres alone.
var (
	reviewsDB = ir.ServiceRef{Name: "reviews-db", Kind: ir.SchemaKindDB}
	notesDB   = ir.ServiceRef{Name: "notes-db", Kind: ir.SchemaKindDB}
)

func d1Services() []stack.Service {
	return []stack.Service{
		{Name: "reviews-db", Kind: ir.SchemaKindDB, Dialects: []string{"sqlite", "postgres"}},
		{Name: "notes-db", Kind: ir.SchemaKindDB, Dialects: []string{"sqlite", "postgres"}},
		{Name: "ledger-db", Kind: ir.SchemaKindDB},
	}
}

// d1Stack deploys reviews-db, and a database Archive hosting notes-db, on
// the cloudflare target: Staging places reviews-db's primary in eastern
// North America, Production keeps both in the EU, and Preview extends
// Staging with a parameter, so each member gets databases of its own.
func d1Stack() *ir.Stack {
	of := func(ref ir.ServiceRef) ir.DeployableRef { return ir.DeployableRef{Service: &ref} }
	return &ir.Stack{
		Name:   "reviews-stack",
		Deploy: []ir.ServiceRef{reviewsDB},
		Deployables: []*ir.DeployableDecl{{
			Name:  "Archive",
			Kind:  ir.DeployableDatabase,
			Hosts: []ir.ServiceRef{notesDB},
		}},
		Environments: []*ir.Environment{
			{
				Name:     "Staging",
				Target:   cloudflare.Target,
				Values:   map[string]any{"accountId": accountID},
				Settings: []*ir.DeployableSettings{{Of: of(reviewsDB), Values: map[string]any{"locationHint": "enam"}}},
			},
			{
				Name:   "Production",
				Target: cloudflare.Target,
				Values: map[string]any{"accountId": accountID},
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
// schema, named <stack>-<db> with the parameter's value appended in
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

// TestD1Refusals: D1 runs SQLite alone, so a database whose DB service
// lists no sqlite is refused; D1 ignores a location hint under a
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
				s.Deploy = append(s.Deploy, ir.ServiceRef{Name: "ledger-db", Kind: ir.SchemaKindDB})
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

// TestD1Name: a D1 database is named after its stack and its DB schema.
func TestD1Name(t *testing.T) {
	if got := cloudflare.D1Name("AcmeReviews", "reviews-db"); got != "acme-reviews-reviews-db" {
		t.Errorf("D1Name = %q", got)
	}
}
