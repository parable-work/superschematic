package gcp

import (
	"encoding/json"
	"fmt"
	"slices"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// The Cloud Run platform (section 7.2): a server is a Cloud Run service
// with a service account of its own, which every grant names. Its config
// reaches it as environment variables: a literal as its value, a secret as
// a reference to its Secret Manager secret, a derived field as one
// variable per member of the value its edge's connector derived.

// The ingress settings a service takes.
const (
	ingressInternal     = "INGRESS_TRAFFIC_INTERNAL_ONLY"
	ingressLoadBalancer = "INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER"
	ingressAll          = "INGRESS_TRAFFIC_ALL"
)

// serviceName names a server's service, and its service account, after
// the deployable: `shop-api`, or `shop-api-pr123` under a parameter.
func serviceName(ctx registry.PlatformContext) any {
	return suffixed(ctx.Environment, kebab(ctx.Deployable.Name), "-")
}

// serviceAddress is the service's run.app URL. Every caller reaches a
// server there, through the VPC (see lowerService), exposed or not.
func serviceAddress(ctx registry.PlatformContext) any {
	return ir.Output{Resource: ctx.Deployable.Name + ".service", Name: "uri"}
}

// lowerService lowers a server to:
//
//   - a service account, and the trace agent role for it, since the
//     entrypoint exports traces to Cloud Trace (section 7.5);
//   - a Secret Manager secret and an accessor grant per secret binding
//     (secretNodes);
//   - the Cloud Run service. An internal server takes internal traffic
//     only and keeps Cloud Run's invoker check, which admits the callers
//     its edges grant. An exposed one turns the check off, since browsers
//     call it, and with a domain takes traffic from the load balancer
//     only; without one, the run.app URL is its public address. Every
//     service lists its full resource name as a custom audience, the
//     audience of its callers' ID tokens (serviceAudience).
//   - for a server that calls another, Direct VPC egress through the
//     environment's network (networkNodes): a call to the callee's run.app
//     URL from the VPC counts as internal, which an internal server's
//     ingress requires;
//   - for an exposed server under a domain, a load balancer and a
//     certificate for its host, and the DNS records they need
//     (exposureNodes).
func lowerService(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	env := ctx.Environment
	v := valuesOf(env)
	if err := checkLength("the service account id of "+d.Name, d.ResourceName, 6, 30); err != nil {
		return registry.Lowered{}, err
	}
	account := d.Name + ".account"
	member := ir.Output{Resource: account, Name: "member"}
	var out registry.Lowered
	add := func(res ...*ir.Resource) { out.Resources = append(out.Resources, res...) }
	add(
		&ir.Resource{ID: account, Type: TypeAccount, Phase: ir.PhaseInfrastructure, Properties: map[string]any{
			"project":     v.project,
			"accountId":   d.ResourceName,
			"displayName": fmt.Sprintf("%s %s server %s", env.Stack, env.Name, d.Name),
		}},
		&ir.Resource{ID: d.Name + ".trace-agent", Type: TypeProjectIAMMember, Phase: ir.PhaseInfrastructure, Properties: map[string]any{
			"project": v.project,
			"role":    "roles/cloudtrace.agent",
			"member":  member,
		}},
	)

	var envs, instances []any
	for _, b := range d.Bindings {
		switch b.Source {
		case ir.BindingLiteral:
			value, err := envString(b.Value)
			if err != nil {
				return registry.Lowered{}, fmt.Errorf("binding %s: %w", b.Field, err)
			}
			envs = append(envs, map[string]any{"name": b.Field, "value": value})
		case ir.BindingParameter:
			envs = append(envs, map[string]any{"name": b.Field, "value": ir.Parameter(b.Parameter)})
		case ir.BindingSecret:
			nodes, ref := secretNodes(env, v, d.Name, b.Secret, member)
			add(nodes...)
			envs = append(envs, map[string]any{"name": b.Field, "valueSource": map[string]any{
				"secretKeyRef": map[string]any{"secret": ref, "version": "latest"},
			}})
		case ir.BindingDerived:
			vars, err := ir.DerivedVariables(b.Field, b.Value)
			if err != nil {
				return registry.Lowered{}, fmt.Errorf("binding %s: %w", b.Field, err)
			}
			for _, variable := range vars {
				envs = append(envs, map[string]any{"name": variable.Name, "value": variable.Value})
			}
			if instance := cloudSQLInstance(b.Value); instance != nil && !containsValue(instances, instance) {
				instances = append(instances, instance)
			}
		default:
			return registry.Lowered{}, fmt.Errorf("binding %s has source %q", b.Field, b.Source)
		}
	}

	container := map[string]any{
		"image": join(v.region, "-docker.pkg.dev/", v.project, "/", kebab(env.Stack), "/", kebab(d.Name)),
		"ports": map[string]any{"containerPort": containerPort},
		"resources": map[string]any{"limits": map[string]any{
			"cpu":    settingString(d.Settings, "cpu", defaultCPU),
			"memory": settingString(d.Settings, "memory", defaultMemory),
		}},
		// The entrypoint's health checks (section 8.1): an instance takes
		// traffic once /readyz answers, so a revision whose databases do
		// not answer never serves, and one whose process stops answering
		// /healthz is restarted. /healthz answers while the instance
		// drains, so a drain is never cut short.
		"startupProbe": map[string]any{
			"httpGet":          map[string]any{"path": readinessPath, "port": containerPort},
			"periodSeconds":    startupPeriod,
			"timeoutSeconds":   startupTimeout,
			"failureThreshold": startupFailures,
		},
		"livenessProbe": map[string]any{
			"httpGet":          map[string]any{"path": livenessPath, "port": containerPort},
			"periodSeconds":    livenessPeriod,
			"timeoutSeconds":   livenessTimeout,
			"failureThreshold": livenessFailures,
		},
	}
	if len(envs) > 0 {
		container["envs"] = envs
	}
	scaling := map[string]any{"minInstanceCount": settingNumber(d.Settings, "minInstances", 0)}
	if n, ok := d.Settings["maxInstances"]; ok {
		scaling["maxInstanceCount"] = n
	}
	template := map[string]any{
		"serviceAccount": ir.Output{Resource: account, Name: "email"},
		"scaling":        scaling,
	}
	if n, ok := d.Settings["concurrency"]; ok {
		template["maxInstanceRequestConcurrency"] = n
	}
	if len(instances) > 0 {
		// The Cloud SQL connection on the service: the instances its
		// connections name, mounted where the Cloud SQL connector's
		// sockets go (section 7.4).
		template["volumes"] = []any{map[string]any{
			"name":             "cloudsql",
			"cloudSqlInstance": map[string]any{"instances": instances},
		}}
		container["volumeMounts"] = []any{map[string]any{"name": "cloudsql", "mountPath": "/cloudsql"}}
	}
	if callsAnother(d) {
		add(networkNodes(env, v)...)
		template["vpcAccess"] = map[string]any{
			"egress": "ALL_TRAFFIC",
			"networkInterfaces": []any{map[string]any{
				"network":    ir.Output{Resource: networkID, Name: "name"},
				"subnetwork": ir.Output{Resource: subnetID, Name: "name"},
			}},
		}
	}
	template["containers"] = []any{container}

	ingress := ingressInternal
	switch {
	case d.Exposed && env.Domain != "":
		ingress = ingressLoadBalancer
	case d.Exposed:
		ingress = ingressAll
	}
	add(&ir.Resource{ID: d.Name + ".service", Type: TypeService, Properties: map[string]any{
		"project":            v.project,
		"location":           v.region,
		"name":               d.ResourceName,
		"ingress":            ingress,
		"invokerIamDisabled": d.Exposed,
		"customAudiences":    []any{serviceAudience(v, d.ResourceName)},
		"deletionProtection": false,
		"template":           template,
	}})
	if d.Exposed && env.Domain != "" {
		nodes, records := exposureNodes(env, v, d)
		add(nodes...)
		out.Records = records
	}
	return out, nil
}

// callsAnother reports whether a server calls an API another server
// serves. A call to an API it serves itself stays on loopback.
func callsAnother(d ir.ResolvedDeployable) bool {
	for _, call := range d.Calls {
		if !slices.ContainsFunc(d.Services, func(s ir.ServiceRef) bool { return s.Name == call.Name }) {
			return true
		}
	}
	return false
}

// cloudSQLInstance returns the instance connection name a derived
// database connection names, or nil for any other value.
func cloudSQLInstance(value any) any {
	m, _ := value.(map[string]any)
	c, _ := m["cloudSql"].(map[string]any)
	return c["instance"]
}

func containsValue(list []any, v any) bool {
	want, _ := json.Marshal(v)
	return slices.ContainsFunc(list, func(item any) bool {
		got, _ := json.Marshal(item)
		return string(got) == string(want)
	})
}

// envString is a literal binding's value as an environment variable's: a
// string as it is, a number or a boolean in its JSON form.
func envString(v any) (string, error) {
	if s, ok := v.(string); ok {
		return s, nil
	}
	data, err := json.Marshal(v)
	return string(data), err
}

func settingString(settings map[string]any, key, def string) string {
	if s, ok := settings[key].(string); ok {
		return s
	}
	return def
}

func settingNumber(settings map[string]any, key string, def any) any {
	if n, ok := settings[key]; ok {
		return n
	}
	return def
}
