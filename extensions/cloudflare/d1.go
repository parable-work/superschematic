package cloudflare

import (
	"encoding/json"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// The D1 platform (docs/stack-model.md, sections 18.2 and 18.6, D64): a
// database deployable is a D1 database per DB schema it hosts, in the
// environment's account, in the sqlite dialect. Resolution places a
// database whose DB services list sqlite here; a TypeScript API whose DB
// service lists sqlite first gets Deps.db as the runtime's SqlDatabase,
// which a D1 binding provides on Workers. A member of a parameterized
// environment gets databases of its own, named with its parameters'
// values (section 5.4). The target's migration runner migrates each
// database from the deploying machine (migrations.go).

// The platform's name and the type it emits.
const (
	// D1Platform places a database on D1.
	D1Platform = "cloudflare.d1"

	// TypeD1Database is pulumi-cloudflare's D1 database, Terraform's
	// cloudflare_d1_database. Its uuid output is what a Worker's d1
	// binding and the runner's d1:// URL name.
	TypeD1Database = "cloudflare:index/d1Database:D1Database"
)

// d1Settings is the schema of a database's settings on D1: where its
// primary lives, and the jurisdiction its data stays in, which become the
// node's primaryLocationHint and jurisdiction. D1 ignores the location hint
// under a jurisdiction, so the lowering refuses both together.
const d1Settings = `{
  "type": "object",
  "properties": {
    "locationHint": {"enum": ["wnam", "enam", "weur", "eeur", "apac", "oc"]},
    "jurisdiction": {"enum": ["eu", "fedramp", "us"]}
  },
  "additionalProperties": false
}`

// d1Platform is the D1 platform's spec.
func d1Platform() registry.PlatformSpec {
	return registry.PlatformSpec{
		Name:     D1Platform,
		Kind:     ir.DeployableDatabase,
		Dialects: []string{registry.SQLDialectSQLite},
		Settings: json.RawMessage(d1Settings),
		NameOf: func(ctx registry.PlatformContext) any {
			return d1Name(ctx.Environment, ctx.Deployable.Name)
		},
		// A database deployable may host several DB schemas, each a D1
		// database of its own: each sql edge derives the uuid of its
		// schema's.
		AddressOf: func(registry.PlatformContext) any { return nil },
		Lower:     lowerD1,
	}
}

// D1Name names the D1 database of a DB schema of a stack: the stack's
// name, a hyphen and the schema's, in kebab case (`acme-shop-reviews-db`).
// A member of a parameterized environment appends each parameter's name
// and value (d1Name).
func D1Name(stack, service string) string {
	return kebab(stack) + "-" + kebab(service)
}

// d1Name is D1Name with each parameter's name and value appended, so each
// member of a preview gets databases of its own:
// `acme-shop-reviews-db-pr123` when pr is 123.
func d1Name(env registry.StackEnvironment, service string) any {
	return suffixed(env, D1Name(env.Stack, service), "-")
}

// d1ID is the ID of the D1 database node of a DB schema a database
// deployable hosts.
func d1ID(database, service string) string { return database + ".d1." + service }

// D1Connector connects a server on Workers to a database on D1: the sql
// edge of a TypeScript API whose DB service lists sqlite first (section
// 18.2).
const D1Connector = "cloudflare.workers-d1"

// connectD1 derives the D1 binding a Worker reaches its database through
// (section 18.2): `d1: {binding, database}`, the binding named after the
// edge's field and the database's uuid, an output of its node. The Workers
// platform writes it as the version's `{type: "d1", name, id}` binding,
// and the binding is the grant: D1 has no roles, so the Worker reads and
// writes all of the database, the runner's state tables included. The
// edge needs no resource.
func connectD1(ctx registry.ConnectorContext) (registry.Connected, error) {
	return registry.Connected{Value: ir.DatabaseConnection{D1: &ir.D1Connection{
		Binding:  ctx.Edge.Field,
		Database: ir.Output{Resource: d1ID(ctx.To.Name, ctx.Edge.Service.Name), Name: "uuid"},
	}}}, nil
}

// lowerD1 lowers a database deployable to a D1 database per hosted DB
// schema, in the environment's account, with the settings' location hint
// or jurisdiction.
func lowerD1(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	account := targetValuesOf(ctx.Environment.Values).accountID
	hint, _ := d.Settings["locationHint"].(string)
	jurisdiction, _ := d.Settings["jurisdiction"].(string)
	if hint != "" && jurisdiction != "" {
		return registry.Lowered{}, registry.Unrealizable("database %s sets locationHint %s and jurisdiction %s; D1 places a database under a jurisdiction by the jurisdiction alone and ignores the hint, so set one", d.Name, hint, jurisdiction)
	}
	var out registry.Lowered
	for _, svc := range d.Services {
		props := map[string]any{
			"accountId": account,
			"name":      d1Name(ctx.Environment, svc.Name),
		}
		if hint != "" {
			props["primaryLocationHint"] = hint
		}
		if jurisdiction != "" {
			props["jurisdiction"] = jurisdiction
		}
		out.Resources = append(out.Resources, &ir.Resource{
			ID:         d1ID(d.Name, svc.Name),
			Type:       TypeD1Database,
			Properties: props,
			Phase:      ir.PhaseInfrastructure,
		})
	}
	return out, nil
}
