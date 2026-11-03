package cloudflare

import (
	"encoding/json"
	"fmt"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// The Hyperdrive platform (D92): a database deployable whose DB service
// lists postgres first is a database of a Postgres outside Cloudflare, such
// as Neon, Supabase or Cloud SQL, which the target's Workers reach through a
// Hyperdrive config: a pool Cloudflare keeps near the database, and a
// connection string it hands each Worker that binds it. The config holds
// the origin's address and its user's password, which Cloudflare never
// gives back, so it lives outside the graph, as a Secrets Store secret does
// (section 18.4): the person creates it once, in the dashboard or with
// wrangler, and names its id in the database's settings, `hyperdrive`. No
// node is the database's: the origin is the person's, and so are its
// backups and its size.
//
// A sql edge from a server, a job or a worker on Workers derives the
// config's binding, `hyperdrive: {binding, config}`, which the Workers
// platform writes as the version's hyperdrive binding beside the field's
// variables; the Worker connects as the config's user, who reads and writes
// the database. The migration runner migrates the origin from the deploying
// machine with its connection string, which the shell holds in
// OriginEnv(<db>), after checking that the config names the same database
// and does not cache reads unless the settings say it may (migrations.go).
// The origin's DDL creates pgvector's extension when a table has a vector
// column (D84), which Neon, Supabase and Cloud SQL ship.

// HyperdrivePlatform places a database on a Postgres outside Cloudflare,
// reached through a Hyperdrive config.
const HyperdrivePlatform = "cloudflare.hyperdrive"

// The connectors of a sql edge to a database on Hyperdrive, from a server
// on Workers, a job on Cron Triggers and a worker on the drainer platform:
// each derives the config's binding.
const (
	HyperdriveConnector        = "cloudflare.workers-hyperdrive"
	CronHyperdriveConnector    = "cloudflare.cron-hyperdrive"
	DrainerHyperdriveConnector = "cloudflare.drainer-hyperdrive"
)

// OriginEnvPrefix begins the variable of the deploying machine's shell
// that holds a database's origin connection string, which the migration
// runner migrates the database with.
const OriginEnvPrefix = "HYPERDRIVE_ORIGIN_"

// OriginEnv is the variable that holds the origin connection string of the
// DB service db, in upper snake case: `HYPERDRIVE_ORIGIN_FINANCES_DB`. A
// generated CI's deploy job reads it from the GitHub secret of that name
// with the stack's and the environment's after it (ci.go).
func OriginEnv(db string) string { return OriginEnvPrefix + screaming(db) }

// NodeModulesDate is the compatibility date from which workerd gives a
// Worker Node's modules with no flag (nodejs_compat's default date). A
// version that binds a Hyperdrive config runs node-postgres, which imports
// them, so one whose date is earlier names nodejs_compat.
const NodeModulesDate = "2026-08-04"

// hyperdriveSettings is the schema of a database's settings on Hyperdrive:
// the id of its Hyperdrive config, 32 hexadecimal digits from the
// dashboard or wrangler, and caching, whether the config may serve a read
// from its cache, off unless it says so.
const hyperdriveSettings = `{
  "type": "object",
  "properties": {
    "hyperdrive": {"type": "string", "pattern": "^[0-9a-f]{32}$"},
    "caching": {"type": "boolean"}
  },
  "additionalProperties": false
}`

// hyperdrivePlatform is the Hyperdrive platform's spec.
func hyperdrivePlatform() registry.PlatformSpec {
	return registry.PlatformSpec{
		Name:     HyperdrivePlatform,
		Kind:     ir.DeployableDatabase,
		Dialects: []string{registry.SQLDialectPostgres},
		Settings: json.RawMessage(hyperdriveSettings),
		// The name a person gives the database's Hyperdrive config, as
		// every name in the account is the stack's and the environment's.
		NameOf: func(ctx registry.PlatformContext) any {
			return accountName(ctx.Environment, ctx.Deployable.Name)
		},
		AddressOf: func(registry.PlatformContext) any { return nil },
		Lower:     lowerHyperdrive,
	}
}

// hyperdriveConfigOf is the id of the Hyperdrive config a database's
// settings name, or "".
func hyperdriveConfigOf(d ir.ResolvedDeployable) string {
	id, _ := d.Settings["hyperdrive"].(string)
	return id
}

// lowerHyperdrive lowers a database on Hyperdrive to no node, and refuses,
// as unrealizable, a database whose settings name no Hyperdrive config, one
// that hosts more than one DB service, which one config cannot reach, and
// one in a parameterized environment, whose members would share the
// origin's database and migrate it under each other.
func lowerHyperdrive(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	env := ctx.Environment
	if len(env.Parameters) > 0 {
		return registry.Lowered{}, registry.Unrealizable("database %s is on %s in environment %s, whose members, one per value of %s, would share its origin's database and each migrate it: a Hyperdrive config reaches one database the person made; give a parameterized environment a database of its own per member, on D1 with sqlite listed first, or leave the database out of it",
			d.Name, HyperdrivePlatform, env.Name, strings.Join(env.Parameters, ", "))
	}
	if len(d.Services) != 1 {
		var hosted []string
		for _, s := range d.Services {
			hosted = append(hosted, s.Name)
		}
		return registry.Lowered{}, registry.Unrealizable("database %s hosts %s, and a Hyperdrive config reaches one database of its origin; give each DB service a database of its own", d.Name, strings.Join(hosted, ", "))
	}
	if hyperdriveConfigOf(d) == "" {
		return registry.Lowered{}, registry.Unrealizable("%s", noHyperdriveConfig(env, d))
	}
	return registry.Lowered{}, nil
}

// noHyperdriveConfig says that database d of env names no Hyperdrive
// config, and how to make one: what the platform's lowering and each
// connector to it refuse.
func noHyperdriveConfig(env registry.StackEnvironment, d ir.ResolvedDeployable) string {
	db := d.Name
	if len(d.Services) == 1 {
		db = d.Services[0].Name
	}
	return fmt.Sprintf("database %s is on %s, a Postgres outside Cloudflare that Workers reach through a Hyperdrive config, and environment %s's settings of it name none: create one for the origin's database, with caching off, in the dashboard (Storage & databases, Hyperdrive) or with `wrangler hyperdrive create %v --connection-string=\"$%s\" --caching-disabled`, and set its id in the settings, hyperdrive: \"<id>\"",
		d.Name, HyperdrivePlatform, env.Name, literalText(d.ResourceName), OriginEnv(db))
}

// connectHyperdrive derives the Hyperdrive binding a Worker reaches its
// database through (D92): `hyperdrive: {binding, config}`, the binding
// named after the edge's field and the config the database's settings
// name. The Workers platform writes it as the version's `{type:
// "hyperdrive", name, id}` binding. The edge needs no resource: the
// Worker connects as the config's user.
func connectHyperdrive(ctx registry.ConnectorContext) (registry.Connected, error) {
	id := hyperdriveConfigOf(ctx.To)
	if id == "" {
		return registry.Connected{}, registry.Unrealizable("%s", noHyperdriveConfig(ctx.Environment, ctx.To))
	}
	return registry.Connected{Value: ir.DatabaseConnection{Hyperdrive: &ir.HyperdriveConnection{
		Binding: ctx.Edge.Field,
		Config:  id,
	}}}, nil
}
