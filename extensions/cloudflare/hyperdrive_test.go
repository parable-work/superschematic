package cloudflare_test

import (
	"reflect"
	"slices"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/cloudflare"
)

// The Hyperdrive and drainer platforms (D92), over ledger-stack, a
// finance-like stack: finances-api, a TypeScript API on Workers with a job
// and two workers, over finances-db, a database that lists postgres alone,
// with a vector column and two keyed queues, on a Postgres outside
// Cloudflare that each reaches through a Hyperdrive config.

// The Hyperdrive configs a person made for finances-db in each environment.
const (
	stagingHyperdrive    = "a76a99bc342644deb02c38d66082262a"
	productionHyperdrive = "0f1e2d3c4b5a69788796a5b4c3d2e1f0"
)

var (
	financesAPI = ir.ServiceRef{Name: "finances-api", Kind: ir.SchemaKindAPI}
	financesDB  = ir.ServiceRef{Name: "finances-db", Kind: ir.SchemaKindDB}
)

// ledgerServices are the fixture's services: finances-db, Postgres alone,
// with the queues EmbedLineItem, which a trigger feeds and replaces, and
// PlaidSyncRequested, keyed and serialized, and a vector column;
// finances-api, a TypeScript API over it, with a secret in its config, a
// job every six hours and a worker of each queue.
func ledgerServices() []stack.Service {
	return []stack.Service{
		{
			Name: "finances-db", Kind: ir.SchemaKindDB,
			Queues:    []string{"EmbedLineItem", "PlaidSyncRequested"},
			QueueKeys: map[string]string{"EmbedLineItem": "line_item_id", "PlaidSyncRequested": "plaid_item_id"},
			Vectors:   true,
		},
		{
			Name: "finances-api", Kind: ir.SchemaKindAPI, Language: registry.APILanguageTypeScript,
			Dependencies: []ir.ServiceRef{financesDB},
			Operations:   []stack.Operation{{Name: "LineItems.listLineItems"}},
			Config:       &stack.Config{Type: "FinancesConfig", Fields: []stack.ConfigField{{Name: "PLAID_SECRET", Required: true, Secret: true}}},
			Jobs:         []ir.Job{{Name: "SyncAllPlaidItems", Schedule: "0 */6 * * *", Timeout: "10m"}},
			Workers: []ir.Worker{
				{Name: "SyncPlaidItem", Queue: "PlaidSyncRequested", Concurrency: 2},
				{Name: "EmbedLineItems", Queue: "EmbedLineItem", Concurrency: 4},
			},
		},
	}
}

// worker names a worker of finances-api in a settings element.
func worker(name string) ir.DeployableRef {
	api := financesAPI
	return ir.DeployableRef{Service: &api, Worker: name}
}

// ledgerStack deploys finances-api, exposed at workers.dev, and
// finances-db on the cloudflare target: Staging's finances-db is on the
// Hyperdrive config its settings name, and its embedding worker claims for
// 40 seconds a firing; Production's config may cache reads, its server's
// compatibility date is one before Node's modules came with no flag, and
// its sync worker handles one item at a time; Preview extends Staging with
// a parameter.
func ledgerStack() *ir.Stack {
	db := financesDB
	values := map[string]any{"accountId": accountID, "workersSubdomain": "acme", "secretsStoreId": storeID}
	one := 1
	return &ir.Stack{
		Name:   "ledger-stack",
		Deploy: []ir.ServiceRef{financesAPI, financesDB},
		Expose: []ir.DeployableRef{stacktest.Of(financesAPI)},
		Environments: []*ir.Environment{
			{
				Name: "Staging", Target: cloudflare.Target, Values: values,
				Settings: []*ir.DeployableSettings{
					{Of: ir.DeployableRef{Service: &db}, Values: map[string]any{"hyperdrive": stagingHyperdrive}},
					{Of: worker("EmbedLineItems"), Values: map[string]any{"drainSeconds": float64(40)}},
				},
			},
			{
				Name: "Production", Target: cloudflare.Target, Values: values,
				Settings: []*ir.DeployableSettings{
					{Of: ir.DeployableRef{Service: &db}, Values: map[string]any{"hyperdrive": productionHyperdrive, "caching": true}},
					{Of: stacktest.Of(financesAPI), Values: map[string]any{"compatibilityDate": "2026-06-01"}},
					{Of: worker("SyncPlaidItem"), Concurrency: &one},
				},
			},
			{Name: "Preview", Extends: "Staging", Parameters: []string{"pr"}},
		},
	}
}

// TestHyperdriveGolden resolves the ledger fixture in Staging and
// Production and checks each environment.json against its golden file,
// every node against its pinned type: finances-db is on
// cloudflare.hyperdrive in postgres, with no node of its own; the server,
// the job and each worker are Workers whose versions bind its Hyperdrive
// config; each worker's Worker fires every minute.
func TestHyperdriveGolden(t *testing.T) {
	reg := assemble(t)
	for _, name := range []string{"Staging", "Production"} {
		t.Run(name, func(t *testing.T) {
			env, errs := resolveOver(t, reg, ledgerStack(), ledgerServices(), name)
			if errs != nil {
				t.Fatal(errs)
			}
			checkEnvironmentGolden(t, reg, env)
			for _, d := range env.Deployables {
				want := map[ir.DeployableKind]string{
					ir.DeployableServer:   cloudflare.Workers,
					ir.DeployableDatabase: cloudflare.HyperdrivePlatform,
					ir.DeployableJob:      cloudflare.Cron,
					ir.DeployableWorker:   cloudflare.Drainer,
				}[d.Kind]
				if d.Platform != want {
					t.Errorf("%s %s is on %s, want %s", d.Kind, d.Name, d.Platform, want)
				}
				if d.Kind == ir.DeployableDatabase && d.Dialect != registry.SQLDialectPostgres {
					t.Errorf("database %s is in %s, want postgres", d.Name, d.Dialect)
				}
			}
			for _, res := range env.Resources.Resources {
				if slices.Contains(res.Owners, "finances-db") {
					t.Errorf("finances-db has the node %s; its origin is the person's", res.ID)
				}
			}
		})
	}
}

// versionBindings are the bindings of a version, by name.
func versionBindings(t *testing.T, version *ir.Resource) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	bindings, _ := version.Properties["bindings"].([]any)
	for _, b := range bindings {
		m := b.(map[string]any)
		out[m["name"].(string)] = m
	}
	return out
}

// TestWorkersBindTheirHyperdriveConfig: each sql edge to finances-db goes
// through the Hyperdrive connector of its caller's platform, which derives
// `hyperdrive: {binding, config}`, the binding named after the field and
// the config the database's settings name; each version holds a
// hyperdrive binding of that name and id, and the variables loadDatabase
// reads as plain text. A worker's version also holds WORKER_CONCURRENCY and
// WORKER_DRAIN_SECONDS, its Worker has workers.dev off, and its Cron
// Trigger fires every minute once its deployment is there. A version whose
// compatibility date is before Node's modules came with no flag names
// nodejs_compat beside nodejs_als.
func TestWorkersBindTheirHyperdriveConfig(t *testing.T) {
	reg := assemble(t)
	env, errs := resolveOver(t, reg, ledgerStack(), ledgerServices(), "Staging")
	if errs != nil {
		t.Fatal(errs)
	}
	connectors := map[string]string{
		"finances-api":                      cloudflare.HyperdriveConnector,
		"finances-api-sync-all-plaid-items": cloudflare.CronHyperdriveConnector,
		"finances-api-sync-plaid-item":      cloudflare.DrainerHyperdriveConnector,
		"finances-api-embed-line-items":     cloudflare.DrainerHyperdriveConnector,
	}
	for from, connector := range connectors {
		var edge *ir.Edge
		for _, e := range env.Edges {
			if e.ID == ir.EdgeID(ir.EdgeSQL, from, "finances-db") {
				edge = e
			}
		}
		if edge == nil || edge.Connector != connector || edge.Field != "FINANCES_DB_DATABASE" {
			t.Errorf("the sql edge of %s = %+v, want it through %s filling FINANCES_DB_DATABASE", from, edge, connector)
			continue
		}
		bindings := versionBindings(t, node(t, env, from+".version"))
		for name, want := range map[string]map[string]any{
			"FINANCES_DB_DATABASE":                    {"type": "hyperdrive", "name": "FINANCES_DB_DATABASE", "id": stagingHyperdrive},
			"FINANCES_DB_DATABASE_HYPERDRIVE_BINDING": {"type": "plain_text", "name": "FINANCES_DB_DATABASE_HYPERDRIVE_BINDING", "text": "FINANCES_DB_DATABASE"},
			"FINANCES_DB_DATABASE_HYPERDRIVE_CONFIG":  {"type": "plain_text", "name": "FINANCES_DB_DATABASE_HYPERDRIVE_CONFIG", "text": stagingHyperdrive},
		} {
			if !reflect.DeepEqual(bindings[name], want) {
				t.Errorf("%s's binding %s = %v, want %v", from, name, bindings[name], want)
			}
		}
		if flags := node(t, env, from+".version").Properties["compatibilityFlags"]; !reflect.DeepEqual(flags, []any{"nodejs_als"}) && !reflect.DeepEqual(flags, []string{"nodejs_als"}) {
			t.Errorf("%s's version names %v, want nodejs_als alone at %s", from, flags, cloudflare.CompatibilityDate)
		}
	}
	for worker, want := range map[string][2]string{
		"finances-api-sync-plaid-item":  {"2", "50"},
		"finances-api-embed-line-items": {"4", "40"},
	} {
		bindings := versionBindings(t, node(t, env, worker+".version"))
		if bindings[ir.WorkerConcurrencyVariable]["text"] != want[0] || bindings[ir.WorkerDrainSecondsVariable]["text"] != want[1] {
			t.Errorf("%s's version binds concurrency %v and window %v, want %s and %s", worker, bindings[ir.WorkerConcurrencyVariable], bindings[ir.WorkerDrainSecondsVariable], want[0], want[1])
		}
		if sub := node(t, env, worker+".worker").Properties["subdomain"].(map[string]any); sub["enabled"] != false {
			t.Errorf("%s's Worker turns workers.dev on: %v", worker, sub)
		}
		schedule := node(t, env, worker+".schedule")
		if schedule.Type != cloudflare.TypeWorkersCronTrigger || !reflect.DeepEqual(schedule.Properties["schedules"], []any{map[string]any{"cron": cloudflare.DrainSchedule}}) ||
			!slices.Contains(schedule.DependsOn, worker+".deployment") {
			t.Errorf("%s's Cron Trigger = %+v", worker, schedule)
		}
	}

	production, errs := resolveOver(t, reg, ledgerStack(), ledgerServices(), "Production")
	if errs != nil {
		t.Fatal(errs)
	}
	version := node(t, production, "finances-api.version")
	if got := version.Properties["compatibilityFlags"]; !reflect.DeepEqual(got, []any{"nodejs_als", "nodejs_compat"}) && !reflect.DeepEqual(got, []string{"nodejs_als", "nodejs_compat"}) {
		t.Errorf("Production's server, at compatibility date 2026-06-01, names %v, want nodejs_als and nodejs_compat", got)
	}
	if got := versionBindings(t, version)["FINANCES_DB_DATABASE"]["id"]; got != productionHyperdrive {
		t.Errorf("Production's server binds Hyperdrive config %v, want %s", got, productionHyperdrive)
	}
	if got := versionBindings(t, node(t, production, "finances-api-sync-plaid-item.version"))[ir.WorkerConcurrencyVariable]["text"]; got != "1" {
		t.Errorf("Production's sync worker handles %v at a time, want 1", got)
	}
}

// TestHyperdriveRefusals: a database on Hyperdrive whose settings name no
// config, one in a parameterized environment, whose members would share
// its origin's database, and one that hosts two DB services are refused,
// saying why; a worker on the drainer with more than one instance, with an
// instance range, or whose window and grace outlast a Cron Trigger's run is
// refused; a worker whose queue is on D1 is refused, saying it claims on
// Postgres through Hyperdrive; and a setting Hyperdrive takes no value of is
// refused by the settings' schema.
func TestHyperdriveRefusals(t *testing.T) {
	reg := assemble(t)
	two := 2
	for name, tc := range map[string]struct {
		env      string
		edit     func(*ir.Stack, []stack.Service) []stack.Service
		code     stack.Code
		want     string
		services []stack.Service
	}{
		"no config": {
			env: "Staging",
			edit: func(s *ir.Stack, services []stack.Service) []stack.Service {
				s.Environments[0].Settings[0].Values = map[string]any{}
				return services
			},
			code: stack.CodeLowering,
			want: "database finances-db is on cloudflare.hyperdrive, a Postgres outside Cloudflare that Workers reach through a Hyperdrive config, and environment Staging's settings of it name none: create one for the origin's database, with caching off, in the dashboard (Storage & databases, Hyperdrive) or with `wrangler hyperdrive create ledger-stack-staging-finances-db --connection-string=\"$HYPERDRIVE_ORIGIN_FINANCES_DB\" --caching-disabled`, and set its id in the settings, hyperdrive: \"<id>\"",
		},
		"a parameterized environment": {
			env:  "Preview",
			edit: func(_ *ir.Stack, services []stack.Service) []stack.Service { return services },
			code: stack.CodeUnrealizable,
			want: "database finances-db is on cloudflare.hyperdrive in environment Preview, whose members, one per value of pr, would share its origin's database and each migrate it",
		},
		"two DB services": {
			env: "Staging",
			edit: func(s *ir.Stack, services []stack.Service) []stack.Service {
				s.Deployables = append(s.Deployables, &ir.DeployableDecl{Name: "Ledger", Kind: ir.DeployableDatabase, Hosts: []ir.ServiceRef{financesDB, {Name: "audit-db", Kind: ir.SchemaKindDB}}})
				s.Deploy = append(s.Deploy, ir.ServiceRef{Name: "audit-db", Kind: ir.SchemaKindDB})
				s.Environments[0].Settings[0].Of = ir.DeployableRef{Deployable: "Ledger"}
				return append(services, stack.Service{Name: "audit-db", Kind: ir.SchemaKindDB})
			},
			code: stack.CodeUnrealizable,
			want: "database Ledger hosts audit-db, finances-db, and a Hyperdrive config reaches one database of its origin; give each DB service a database of its own",
		},
		"two instances": {
			env: "Staging",
			edit: func(s *ir.Stack, services []stack.Service) []stack.Service {
				s.Environments[0].Settings = append(s.Environments[0].Settings, &ir.DeployableSettings{Of: worker("SyncPlaidItem"), Instances: &two})
				return services
			},
			code: stack.CodeUnrealizable,
			want: "worker finances-api-sync-plaid-item runs 2 instances in environment Staging, and on Workers each firing of its Cron Trigger, once a minute, runs one claim loop; raise its concurrency, 2, which a firing handles at a time, and set instances to 1",
		},
		"an instance range": {
			env: "Staging",
			edit: func(s *ir.Stack, services []stack.Service) []stack.Service {
				s.Environments[0].Settings = append(s.Environments[0].Settings, &ir.DeployableSettings{Of: worker("SyncPlaidItem"), MaxInstances: &two})
				return services
			},
			code: stack.CodeUnrealizable,
			want: "worker finances-api-sync-plaid-item may run up to 2 instances (maxInstances) in environment Staging, and no target scales a worker with its queue's depth yet (D53, amended); leave maxInstances out",
		},
		"a window past a Cron Trigger's run": {
			env: "Staging",
			edit: func(s *ir.Stack, services []stack.Service) []stack.Service {
				s.Environments[0].Settings[1].Values["drainSeconds"] = float64(900)
				return services
			},
			code: stack.CodeUnrealizable,
			want: "worker finances-api-embed-line-items claims for 15m and lets its handlers finish for 8s at each firing, 15m8s in all, and Cloudflare stops a scheduled run after 15m; shorten drainSeconds or its @worker's grace",
		},
		"a queue on D1": {
			env: "Staging",
			edit: func(s *ir.Stack, services []stack.Service) []stack.Service {
				services[0].Dialects = []string{registry.SQLDialectSQLite, registry.SQLDialectPostgres}
				services[0].Vectors = false
				s.Environments[0].Settings = s.Environments[0].Settings[1:]
				return services
			},
			code: stack.CodeUnrealizable,
			want: "a worker on Workers, cloudflare.drainer, claims on Postgres through Hyperdrive",
		},
		"an unknown setting": {
			env: "Staging",
			edit: func(s *ir.Stack, services []stack.Service) []stack.Service {
				s.Environments[0].Settings[0].Values["origin"] = "postgres://db.example.com/finances"
				return services
			},
			code: stack.CodeInvalidSettings,
			want: "settings of finances-db on platform cloudflare.hyperdrive",
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := ledgerStack()
			services := tc.edit(s, ledgerServices())
			_, errs := resolveOver(t, reg, s, services, tc.env)
			refusedWith(t, errs, tc.code, tc.want)
		})
	}
}
