package gcp

import (
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// The Cloud SQL platform (section 7.2): a database is a Cloud SQL Postgres
// instance with a database per hosted schema. Servers connect through the
// Cloud SQL connector with IAM database authentication (section 7.4), so
// the instance turns IAM authentication on, refuses connections that do
// not come through a connector, and has no password anyone enters.
//
// A member of a parameterized environment shares its parent's instance and
// creates its own databases on it, named with the parameter's value
// (section 5.4).

// instanceName names a database's instance after the deployable. Under a
// parameter it is still the parent's instance.
func instanceName(ctx registry.PlatformContext) any { return kebab(ctx.Deployable.Name) }

// instanceAddress is the instance connection name, `project:region:name`,
// which the Cloud SQL connector dials.
func instanceAddress(ctx registry.PlatformContext) any {
	return ir.Output{Resource: ctx.Deployable.Name + ".instance", Name: "connectionName"}
}

// lowerDatabase lowers a database to its instance and a database per
// hosted schema. Settings choose the tier, high availability, the
// Postgres version, the disk and deletion protection, which is on by
// default in production (section 7.5).
func lowerDatabase(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	env := ctx.Environment
	v := valuesOf(env)
	instance := d.Name + ".instance"
	availability := "ZONAL"
	if d.Settings["highAvailability"] == true {
		availability = "REGIONAL"
	}
	protected := v.production
	if p, ok := d.Settings["deletionProtection"].(bool); ok {
		protected = p
	}
	settings := map[string]any{
		"tier":                      settingString(d.Settings, "tier", defaultTier),
		"edition":                   "ENTERPRISE",
		"availabilityType":          availability,
		"deletionProtectionEnabled": protected,
		"connectorEnforcement":      "REQUIRED",
		"ipConfiguration":           map[string]any{"ipv4Enabled": true},
		"databaseFlags": []any{
			map[string]any{"name": "cloudsql.iam_authentication", "value": "on"},
		},
		"backupConfiguration": map[string]any{
			"enabled":                    true,
			"pointInTimeRecoveryEnabled": v.production,
		},
	}
	if n, ok := d.Settings["diskSize"]; ok {
		settings["diskSize"] = n
	}
	out := registry.Lowered{Resources: []*ir.Resource{{
		ID:        instance,
		Type:      TypeInstance,
		Inherited: len(env.Parameters) > 0,
		Properties: map[string]any{
			"project":            v.project,
			"region":             v.region,
			"name":               d.ResourceName,
			"databaseVersion":    settingString(d.Settings, "version", defaultPostgres),
			"deletionProtection": protected,
			"settings":           settings,
		},
	}}}
	for _, svc := range d.Services {
		name := suffixed(env, snake(svc.Name), "_")
		if err := checkLength("the database name of "+svc.Name, name, 1, 63); err != nil {
			return registry.Lowered{}, err
		}
		out.Resources = append(out.Resources, &ir.Resource{
			ID:   d.Name + ".database." + svc.Name,
			Type: TypeDatabase,
			Properties: map[string]any{
				"project":  v.project,
				"name":     name,
				"instance": ir.Output{Resource: instance, Name: "name"},
			},
		})
	}
	return out, nil
}
