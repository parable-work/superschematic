package stackdeploy_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/sqlmigrate"
	"github.com/parable-work/superschematic/internal/stackdeploy"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// fixture is the shop stack of stack/stacktest on its fake target, with
// the fake's deploy seams.
type fixture struct {
	reg *registry.Registry
	ext *stacktest.Extension
	log *bytes.Buffer
}

// dnsToken is the credential the fake DNS platform needs, as a DNS
// platform's resolution names it in environment.json (`dns.credentials`);
// Env is the variable a run hands it in.
var dnsToken = stackdeploy.Credential{
	Secret:      "shop-stack-fake-dns-acme_dev",
	Env:         "FAKE_DNS_API_TOKEN",
	Description: "An API token with DNS Edit on zone acme.dev",
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ext := &stacktest.Extension{}
	reg, err := registry.Assemble(registry.DefaultNaming(), ext)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{reg: reg, ext: ext, log: &bytes.Buffer{}}
}

// env resolves an environment of the shop stack.
func (f *fixture) env(t *testing.T, name string) *ir.ResolvedEnvironment {
	t.Helper()
	env, err := stack.Resolve(f.reg, stack.Input{Stack: stacktest.WithoutJobSettings(stacktest.Shop()), Services: stacktest.WithoutJobs(stacktest.AcmeShop()), Environment: name})
	if err != nil {
		t.Fatal(err)
	}
	// The fake DNS platform names no credential, so the environment gets
	// its token here, as a DNS platform's resolution writes one into
	// dns.credentials, where CredentialsOf reads it.
	if env.DNS != nil && env.DNS.Platform == stacktest.DNSPlatform {
		env.DNS.Credentials = []*ir.DNSCredential{{Secret: dnsToken.Secret, Env: dnsToken.Env, Description: dnsToken.Description}}
	}
	return env
}

func (f *fixture) options(t *testing.T, env *ir.ResolvedEnvironment, params map[string]string) stackdeploy.Options {
	return stackdeploy.Options{
		Registry: f.reg,
		Run:      registry.Run{Environment: env, Parameters: params},
		Dir:      t.TempDir(),
		Log:      f.log,
	}
}

// setSecret stores a value in the fake secret store.
func (f *fixture) setSecret(t *testing.T, env *ir.ResolvedEnvironment, id, value string) {
	t.Helper()
	if err := f.ext.Secrets.Set(context.Background(), env, id, []byte(value)); err != nil {
		t.Fatal(err)
	}
}

// ready stores every value a deploy of env reads: its secret and the fake
// DNS platform's token.
func (f *fixture) ready(t *testing.T, env *ir.ResolvedEnvironment) {
	t.Helper()
	f.setSecret(t, env, "PaymentsSecrets.STRIPE_KEY", "sk_test_value")
	f.setSecret(t, env, dnsToken.Secret, "dns-token-value")
}

// calls returns the target's call log from index from on.
func (f *fixture) calls(from int) []string {
	return f.ext.Provisioner.Calls()[from:]
}

func digest(n int) string {
	return "sha256:" + strings.Repeat(fmt.Sprintf("%x", n%16), 64)
}

// images are images of both shop servers, by the repository the fake
// platform writes (the deployable's name in kebab case).
func images(n int) map[string]string {
	return map[string]string{
		"shop-api": "shop-api@" + digest(n),
		"Orders":   "orders@" + digest(n),
	}
}

// Models of shop-db: v1 has a product table with a name; v2 renames it
// to title, which expands with the new column and contracts with the
// drop of the old one.
func model(version int) *sqlmigrate.Model {
	columns := []*sqlmigrate.Column{{Name: "id", Type: "UUID"}}
	switch version {
	case 1:
		columns = append(columns, &sqlmigrate.Column{Name: "name", Type: "TEXT", Nullable: true, Origin: "Product.name"})
	case 2:
		columns = append(columns, &sqlmigrate.Column{Name: "title", Type: "TEXT", Nullable: true, Origin: "Product.title"})
	}
	return &sqlmigrate.Model{
		Version: sqlmigrate.ModelVersion,
		Dialect: sqlmigrate.Postgres,
		Service: "shop-db",
		Tables: []*sqlmigrate.Table{{
			Name: "product", Kind: sqlmigrate.TableEntity, Origin: "Product",
			Columns:    columns,
			PrimaryKey: &sqlmigrate.Constraint{Name: "product_pkey", Columns: []string{"id"}},
		}},
	}
}

// planner plans shop-db to the model of version *to with sqlmigrate, and
// records the hash of each model it planned from ("" for none).
type planner struct {
	mu   sync.Mutex
	to   int
	from []string
}

func (p *planner) plan(service, dialect string, from json.RawMessage) (*stackdeploy.DatabasePlan, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var fromModel *sqlmigrate.Model
	label := ""
	if len(from) > 0 {
		fromModel = &sqlmigrate.Model{}
		if err := json.Unmarshal(from, fromModel); err != nil {
			return nil, err
		}
		hash, err := fromModel.Hash()
		if err != nil {
			return nil, err
		}
		label = hash
	}
	p.from = append(p.from, label)
	if dialect != "postgres" {
		return nil, fmt.Errorf("dialect %s", dialect)
	}
	toModel := model(p.to)
	toModel.Service = service
	plan, err := sqlmigrate.Diff(fromModel, toModel, sqlmigrate.Options{})
	if err != nil {
		return nil, err
	}
	return stackdeploy.PlanOf(plan)
}

func modelHash(t *testing.T, version int) string {
	t.Helper()
	hash, err := model(version).Hash()
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

// terminal is a fake terminal: it answers each prompt with the next
// value, and records the prompts.
type terminal struct {
	answers []string
	prompts []string
}

func (t *terminal) Secret(prompt string) ([]byte, error) {
	t.prompts = append(t.prompts, prompt)
	if len(t.answers) == 0 {
		return nil, fmt.Errorf("the terminal has no more answers")
	}
	answer := t.answers[0]
	t.answers = t.answers[1:]
	return []byte(answer), nil
}

func fixedNow() time.Time { return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC) }
