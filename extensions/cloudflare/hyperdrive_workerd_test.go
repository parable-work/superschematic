package cloudflare_test

import (
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"

	"github.com/parable-work/superschematic/extensions/cloudflare"
)

// workerdLedgerFixture is the server generator's workerd fixture that runs
// ts-ledger-stack's server and two workers over a hyperdrive binding to a
// local Postgres (D92), and the site's Worker whose origin serves the
// server (D91).
var workerdLedgerFixture = filepath.Join("..", "..", "internal", "generator", "servergen", "testdata", "workerd", "ledger", "ledger.capnp")

// tsLedgerHyperdrive is the Hyperdrive config the workerd fixture names.
const tsLedgerHyperdrive = "a76a99bc342644deb02c38d66082262a"

// tsLedgerFixtureServices are the workerd fixture's services a version's
// service binding names, by the deployable whose Worker each runs.
var tsLedgerFixtureServices = map[string]string{"ledger": "ts-ledger"}

// tsLedgerStack is ts-ledger-stack on the cloudflare target: ts-ledger, a
// TypeScript API over the user model with two workers, over ts-ledger-db on
// Hyperdrive, with the windows the workerd fixture's workers claim for;
// and ts-ledger-web, a site whose origin serves ts-ledger (D91), which no
// expose names.
func tsLedgerStack() (*ir.Stack, []stack.Service) {
	api := ir.ServiceRef{Name: "ts-ledger", Kind: ir.SchemaKindAPI}
	db := ir.ServiceRef{Name: "ts-ledger-db", Kind: ir.SchemaKindDB}
	web := ir.ServiceRef{Name: "ts-ledger-web", Kind: ir.SchemaKindSite}
	worker := func(name string) ir.DeployableRef { return ir.DeployableRef{Service: &api, Worker: name} }
	s := &ir.Stack{
		Name:    "ts-ledger-stack",
		Deploy:  []ir.ServiceRef{api, db, web},
		Origins: []*ir.Origin{{Site: web, APIs: []ir.ServiceRef{api}}},
		Environments: []*ir.Environment{{
			Name: "Edge", Target: cloudflare.Target,
			Values: map[string]any{"accountId": accountID, "workersSubdomain": "acme"},
			Settings: []*ir.DeployableSettings{
				{Of: ir.DeployableRef{Service: &db}, Values: map[string]any{"hyperdrive": tsLedgerHyperdrive}},
				{Of: worker("EmbedLineItems"), Values: map[string]any{"drainSeconds": float64(3)}},
				{Of: worker("SyncItems"), Values: map[string]any{"drainSeconds": float64(6)}},
			},
		}},
	}
	services := []stack.Service{
		{Name: "ts-ledger-db", Kind: ir.SchemaKindDB, Queues: []string{"EmbedLineItem", "SyncRequested"}, Vectors: true},
		{
			Name: "ts-ledger", Kind: ir.SchemaKindAPI, Language: registry.APILanguageTypeScript,
			Dependencies: []ir.ServiceRef{db},
			Identity:     true,
			AuthDB:       &db,
			Operations:   []stack.Operation{{Name: "Ledger.state"}},
			Workers: []ir.Worker{
				{Name: "EmbedLineItems", Queue: "EmbedLineItem", Concurrency: 2, Grace: "2s"},
				{Name: "SyncItems", Queue: "SyncRequested", Concurrency: 2, Grace: "2s"},
			},
		},
		{
			Name:  "ts-ledger-web",
			Kind:  ir.SchemaKindSite,
			Calls: []ir.ServiceRef{api},
			Site:  &ir.ResolvedSite{Dir: "web/ts-ledger-web", Build: "build", Output: "dist", Fallback: "index.html"},
		},
	}
	return s, services
}

// TestWorkerdLedgerFixtureHoldsTheGoldenBindings holds the workerd fixture
// that runs ts-ledger-stack's Workers against Postgres to the bindings the
// platforms give their versions (D92): each binding of each version by its
// name and kind, the database's hyperdrive binding as workerd's
// hyperdrive binding, and each plain text with its value, the window and
// the concurrency among them. workerd's hyperdrive binding names the
// database itself, as Hyperdrive's config does. The site's Worker in front
// of the server (D91) holds its version's bindings too: SITE_CONFIG,
// SITE_FALLBACK and SITE_ORIGIN with their texts, ASSETS as the fixture's
// stand-in for the static assets, and SITE_API_TS_LEDGER as a service
// binding of the fixture's server. The server, which only the site's
// origin serves, is not exposed, and binds no CORS origins and no identity
// config: no trusted origin and no cookie domain.
func TestWorkerdLedgerFixtureHoldsTheGoldenBindings(t *testing.T) {
	reg := assemble(t)
	s, services := tsLedgerStack()
	env, errs := resolveOver(t, reg, s, services, "Edge")
	if errs != nil {
		t.Fatal(errs)
	}
	fixture := workerdBindings(t, workerdLedgerFixture)
	for worker, deployable := range map[string]string{
		"ledgerWorker": "ts-ledger",
		"embedWorker":  "ts-ledger-embed-line-items",
		"syncWorker":   "ts-ledger-sync-items",
		"siteWorker":   "ts-ledger-web",
	} {
		got := fixture[worker]
		if got == nil {
			t.Errorf("the workerd fixture has no Worker %s", worker)
			continue
		}
		want := map[string]bool{}
		for _, b := range node(t, env, deployable+".version").Properties["bindings"].([]any) {
			b := b.(map[string]any)
			name := b["name"].(string)
			want[name] = true
			have, ok := got[name]
			if !ok {
				t.Errorf("the workerd fixture's %s has no binding %s", worker, name)
				continue
			}
			switch b["type"] {
			case "plain_text":
				if have.kind != "text" || have.value != b["text"] {
					t.Errorf("%s's %s is a %s of %q, want text %v", worker, name, have.kind, have.value, b["text"])
				}
			case "hyperdrive":
				if have.kind != "hyperdrive" || b["id"] != tsLedgerHyperdrive {
					t.Errorf("%s's %s is a %s, and the version binds %v; want workerd's hyperdrive binding of config %s", worker, name, have.kind, b, tsLedgerHyperdrive)
				}
			case "service":
				out, ok := b["service"].(ir.Output)
				callee := strings.TrimSuffix(out.Resource, ".worker")
				if !ok || have.kind != "service" || tsLedgerFixtureServices[have.value] != callee {
					t.Errorf("%s's %s is a %s of %q, want a service of the fixture's Worker for %v", worker, name, have.kind, have.value, b["service"])
				}
			case "assets":
				if have.kind != "service" || have.value != "assets" {
					t.Errorf("%s's %s is a %s of %q, want the fixture's assets stand-in", worker, name, have.kind, have.value)
				}
			default:
				t.Errorf("%s's %s is of type %v, which the fixture does not stand in for", worker, name, b["type"])
			}
		}
		for name := range got {
			if !want[name] {
				t.Errorf("the workerd fixture's %s has binding %s, which %s's version does not", worker, name, deployable)
			}
		}
	}
	server := env.Deployable("ts-ledger")
	if server.Exposed || len(server.Origins) != 1 || server.Origins[0] != "ts-ledger-web" {
		t.Errorf("ts-ledger is exposed %v and on the origins %v, want the site's origin alone", server.Exposed, server.Origins)
	}
	for _, b := range server.Bindings {
		if b.IdentityOf != "" || b.CORSOf != "" {
			t.Errorf("ts-ledger binds %+v, want no identity config and no CORS origins", b)
		}
	}
}
