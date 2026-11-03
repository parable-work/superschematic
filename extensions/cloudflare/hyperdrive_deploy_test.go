package cloudflare_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/cloudflare"
)

// TestDeployTheLedgerOnWorkers deploys the ledger fixture's Staging (D92)
// over a fake Cloudflare that holds finances-db's Hyperdrive config, a fake
// provisioner, the bundles' builds stubbed and a stand-in for
// superschematic-migrate: the deploy readies the migrations, which need the
// origin the shell holds, bundles and keeps the server, the job and both
// workers, applies each step, and migrates finances-db's origin between the
// infrastructure step and the rollout with `superschematic-migrate apply`,
// the origin in DATABASE_URL; the manifest records each bundle, and the
// log never holds the origin's password.
func TestDeployTheLedgerOnWorkers(t *testing.T) {
	cloud := newFakeCloud()
	config := &cloudflare.HyperdriveConfig{ID: stagingHyperdrive, Name: "ledger-stack-staging-finances-db"}
	config.Origin.Host, config.Origin.Port, config.Origin.Database, config.Origin.User = "db.example.com", 5432, "finances", "finances"
	config.Caching.Disabled = true
	cloud.hyperdrive[stagingHyperdrive] = config
	sh := shell{
		cloudflare.TokenEnv:                 "the-deployers-token",
		cloudflare.PassphraseEnv:            "the-passphrase",
		cloudflare.MigrateEnv:               runnerPath,
		cloudflare.OriginEnv("finances-db"): stagingOrigin,
	}
	var migrations []originRun
	ext := cloudflare.Extension{Cloud: cloud, Getenv: sh.getenv, Exec: func(cmd *exec.Cmd) error {
		run := originRun{args: slices.Clone(cmd.Args)}
		for _, kv := range cmd.Env {
			if v, ok := strings.CutPrefix(kv, "DATABASE_URL="); ok {
				run.databaseURL = v
			}
		}
		migrations = append(migrations, run)
		return nil
	}}
	prov := &fakeProvisioner{}
	reg, err := registry.Assemble(registry.DefaultNaming(), &stacktest.Extension{}, ext, pulumiStub{prov: prov})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := cloud.EnsureBucket(ctx, accountID, cloudflare.StateBucket); err != nil {
		t.Fatal(err)
	}
	env, errs := resolveOver(t, reg, ledgerStack(), ledgerServices(), "Staging")
	if errs != nil {
		t.Fatal(errs)
	}
	// finances-api's secret, in the Secrets Store bootstrap recorded.
	cloud.stores = []cloudflare.SecretsStore{{ID: storeID, Name: "default_secrets_store"}}
	target, _ := reg.Target(cloudflare.Target)
	if err := target.Secrets.Set(ctx, env, "FinancesConfig.PLAID_SECRET", []byte("plaid-secret")); err != nil {
		t.Fatal(err)
	}
	bundled := []string{"finances-api", "finances-api-sync-all-plaid-items", "finances-api-sync-plaid-item", "finances-api-embed-line-items"}
	root := t.TempDir()
	for _, deployable := range bundled {
		pkg := filepath.Join(root, "dist", "server", "ledger-stack", deployable)
		if err := os.MkdirAll(pkg, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pkg, "bundle.ts"), []byte("// written by the server generator\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	build := &siteBuild{version: "v1"}
	log := &bytes.Buffer{}
	m, err := stack.Deploy(ctx, stack.DeployOptions{
		Options: stack.Options{Registry: reg, Run: registry.Run{Environment: env}, Dir: t.TempDir(), Log: log},
		Sources: &stack.Sources{OutputRoot: filepath.Join(root, "dist"), RepositoryRoot: root, ImplementationRoot: root, Run: build.run},
		Planner: func(service, dialect string, _ json.RawMessage) (*stack.DatabasePlan, error) {
			return &stack.DatabasePlan{
				Service: service, Dialect: dialect,
				To: "v1", ToModel: json.RawMessage(`{"v":1}`),
				ExpandSteps: 3, Hash: "plan", Document: json.RawMessage(ledgerPlan),
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, log)
	}
	for _, deployable := range bundled {
		if b, ok := m.Bundles[deployable]; !ok || !strings.HasPrefix(b.Digest, "sha256:") {
			t.Errorf("the manifest records bundle %+v of %s", b, deployable)
		}
	}
	if len(migrations) != 1 || migrations[0].args[1] != "apply" || migrations[0].args[5] != "expand" || migrations[0].databaseURL != stagingOrigin {
		t.Errorf("the deploy migrated %+v, want one expand phase on the origin", migrations)
	}
	for _, want := range []string{
		"the migrations run " + runnerPath + " on this machine",
		"finances-db: 3 expand steps on database finances of db.example.com:5432 as finances, through Hyperdrive config " + stagingHyperdrive,
	} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("the deploy's log lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(log.String(), "s3cret-password") {
		t.Errorf("the deploy's log holds the origin's password:\n%s", log)
	}
	for _, id := range []string{"finances-api.version", "finances-api-embed-line-items.schedule", "finances-api-sync-plaid-item.schedule", "finances-api-sync-all-plaid-items.schedule"} {
		if !prov.applied[id] {
			t.Errorf("the deploy applied no %s", id)
		}
	}

	// A deploy whose shell holds no origin stops before it changes
	// anything.
	delete(sh, cloudflare.OriginEnv("finances-db"))
	applied := len(prov.applied)
	if _, err := stack.Deploy(ctx, stack.DeployOptions{
		Options: stack.Options{Registry: reg, Run: registry.Run{Environment: env}, Dir: t.TempDir(), Log: log},
		Sources: &stack.Sources{OutputRoot: filepath.Join(root, "dist"), RepositoryRoot: root, ImplementationRoot: root, Run: build.run},
		Planner: func(service, dialect string, _ json.RawMessage) (*stack.DatabasePlan, error) {
			return &stack.DatabasePlan{Service: service, Dialect: dialect, To: "v2", ToModel: json.RawMessage(`{"v":2}`), ExpandSteps: 1, Hash: "plan2", Document: json.RawMessage(ledgerPlan)}, nil
		},
	}); err == nil || !strings.Contains(err.Error(), "set HYPERDRIVE_ORIGIN_FINANCES_DB") {
		t.Errorf("a deploy with no origin = %v", err)
	}
	if len(prov.applied) != applied {
		t.Errorf("a deploy with no origin applied %d more nodes", len(prov.applied)-applied)
	}
}
