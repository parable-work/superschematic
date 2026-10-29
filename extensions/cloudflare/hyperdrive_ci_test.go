package cloudflare_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"

	"github.com/parable-work/superschematic/extensions/cloudflare"
)

// The generated CI of a stack with a database on Hyperdrive (D92): the
// deploy job reads the database's origin connection string from a GitHub
// secret into the variable its migrations read, and bootstrap's --github
// sets that secret from the shell.

// TestHyperdriveCIGolden renders the ledger fixture's workflow, its
// Staging, with the core's GitHub Actions renderer, and checks it against
// the golden file, which -update rewrites: the deploy job's stack commands
// read HYPERDRIVE_ORIGIN_FINANCES_DB from the secret
// HYPERDRIVE_ORIGIN_FINANCES_DB_LEDGER_STACK_STAGING, in its GitHub
// environment, beside the deployer's token, and install
// superschematic-migrate; the plan job reads no origin; the check job
// bundles the server, the job and both workers.
func TestHyperdriveCIGolden(t *testing.T) {
	reg := assemble(t)
	renderer, ok := reg.CIRenderer("github")
	if !ok {
		t.Fatal("the core registers no github CI renderer")
	}
	s := ledgerStack()
	resolved, err := stack.Resolve(reg, stack.Input{Stack: s, Services: ledgerServices(), Environment: "Staging"})
	if err != nil {
		t.Fatal(err)
	}
	env := reg.CIEnvironment(resolved)
	if !env.Migrate {
		t.Error("Staging's CI does not install the migration runner")
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
		Environments: []registry.CIEnvironment{env},
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
	origin := "HYPERDRIVE_ORIGIN_FINANCES_DB: ${{ secrets.HYPERDRIVE_ORIGIN_FINANCES_DB_LEDGER_STACK_STAGING }}"
	if n := strings.Count(string(want), origin); n != 1 {
		t.Errorf("%d steps read the origin, want the deploy job's stack command alone", n)
	}
	if bin, err := exec.LookPath("actionlint"); err == nil {
		if out, err := exec.Command(bin, "-no-color", golden).CombinedOutput(); err != nil {
			t.Errorf("actionlint %s: %v\n%s", golden, err, out)
		}
	}
	for field := range env.Planner.Fields {
		if strings.HasPrefix(field, "env.") {
			t.Errorf("the planner's identity reads %s", field)
		}
	}
	if got := env.Deployer.Fields["env.HYPERDRIVE_ORIGIN_FINANCES_DB"]; got != "HYPERDRIVE_ORIGIN_FINANCES_DB_LEDGER_STACK_STAGING" {
		t.Errorf("the deployer's identity reads the origin from %q", got)
	}
}

// hyperdriveEnv is Staging of the edge stack with finances-db on
// Hyperdrive, as resolution writes it.
func hyperdriveEnv() *ir.ResolvedEnvironment {
	env := cloudflareEnv("Staging", map[string]any{"accountId": accountID})
	env.Deployables = []*ir.ResolvedDeployable{{
		Name: "finances-db", Kind: ir.DeployableDatabase, Platform: cloudflare.HyperdrivePlatform, Dialect: registry.SQLDialectPostgres,
		Services: []ir.ServiceRef{financesDB},
		Settings: map[string]any{"hyperdrive": stagingHyperdrive},
	}}
	return env
}

// TestBootstrapGitHubSetsTheOriginSecrets: --github sets the deploy job's
// origin secret of each database on Hyperdrive in its GitHub environment,
// from the variable the shell holds, beside the deployer's token, and never
// prints its value; with no such variable in the shell it sets none and
// says how to; without gh it prints the command that sets it.
func TestBootstrapGitHubSetsTheOriginSecrets(t *testing.T) {
	f := newFixture(t)
	f.givePassphrases("mine", "mine-too")
	f.shell[cloudflare.OriginEnv("finances-db")] = stagingOrigin
	target := f.target(t)
	ctx := context.Background()
	reveal, log := &bytes.Buffer{}, &bytes.Buffer{}
	req := f.request(t, hyperdriveEnv(), reveal, log)
	req.GitHub = true
	if _, err := target.Bootstrap.Bootstrap(ctx, req); err != nil {
		t.Fatal(err)
	}
	if got := f.gh.secrets["Staging/HYPERDRIVE_ORIGIN_FINANCES_DB_EDGE_STACK_STAGING"]; got != stagingOrigin {
		t.Errorf("GitHub holds the origin secret %q, want the shell's; it holds %v", got, f.gh.secrets)
	}
	if !strings.Contains(log.String(), "set HYPERDRIVE_ORIGIN_FINANCES_DB_EDGE_STACK_STAGING, a secret of GitHub environment Staging of acme/edge") {
		t.Errorf("bootstrap did not say it set the origin:\n%s", log)
	}
	for _, out := range []string{reveal.String(), log.String()} {
		if strings.Contains(out, "s3cret-password") {
			t.Errorf("the origin was printed:\n%s", out)
		}
	}

	delete(f.shell, cloudflare.OriginEnv("finances-db"))
	f.gh.secrets = map[string]string{}
	log.Reset()
	if _, err := target.Bootstrap.Bootstrap(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.gh.secrets["Staging/HYPERDRIVE_ORIGIN_FINANCES_DB_EDGE_STACK_STAGING"]; ok {
		t.Error("bootstrap set an origin the shell does not hold")
	}
	if !strings.Contains(log.String(), "HYPERDRIVE_ORIGIN_FINANCES_DB is not set in this shell, so HYPERDRIVE_ORIGIN_FINANCES_DB_EDGE_STACK_STAGING is left as it is: set it to the origin's connection string with gh secret set HYPERDRIVE_ORIGIN_FINANCES_DB_EDGE_STACK_STAGING --repo acme/edge --env Staging") {
		t.Errorf("bootstrap did not say how to set the origin:\n%s", log)
	}

	f.gh.unready = errors.New("gh, the GitHub CLI, is not on PATH")
	reveal.Reset()
	if _, err := target.Bootstrap.Bootstrap(ctx, req); err != nil {
		t.Fatal(err)
	}
	if want := "The origin connection string in HYPERDRIVE_ORIGIN_FINANCES_DB, which the deploy migrates its database with:\n\n  gh secret set HYPERDRIVE_ORIGIN_FINANCES_DB_EDGE_STACK_STAGING --repo acme/edge --env Staging\n"; !strings.Contains(reveal.String(), want) {
		t.Errorf("bootstrap printed no %q:\n%s", want, reveal)
	}
}
