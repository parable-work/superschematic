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
// The job platform (cloudrunjob.go) lowers a job the same way, to a Cloud
// Run job (D52).

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
//   - what a server and a job share (lowerWorkload): a service account,
//     the trace agent role for it, since the entrypoint exports traces to
//     Cloud Trace (section 7.5), a Secret Manager secret and an accessor
//     grant per secret binding, and the Cloud SQL volume and VPC egress
//     its edges need;
//   - the Cloud Run service, whose CPU is allocated only while it handles
//     a request unless the server's settings keep it allocated
//     (cpuAlwaysAllocated). An internal server takes internal traffic
//     only and keeps Cloud Run's invoker check, which admits the callers
//     its edges grant. An exposed one turns the check off, since browsers
//     call it, and with a domain takes traffic from the load balancer
//     only; without one, the run.app URL is its public address. Every
//     service lists its full resource name as a custom audience, the
//     audience of its callers' ID tokens (serviceAudience).
//   - for an exposed server under a domain, a load balancer and a
//     certificate for its host, and the DNS records they need
//     (exposureNodes).
func lowerService(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	env := ctx.Environment
	v := valuesOf(env)
	out, w, err := lowerWorkload(ctx)
	if err != nil {
		return registry.Lowered{}, err
	}
	add := func(res ...*ir.Resource) { out.Resources = append(out.Resources, res...) }

	container, template := w.container, w.template
	container["ports"] = map[string]any{"containerPort": containerPort}
	// The entrypoint's health checks (section 8.1): an instance takes
	// traffic once /readyz answers, so a revision whose databases do not
	// answer never serves, and one whose process stops answering /healthz
	// is restarted. /healthz answers while the instance drains, so a
	// drain is never cut short.
	container["startupProbe"] = map[string]any{
		"httpGet":          map[string]any{"path": readinessPath, "port": containerPort},
		"periodSeconds":    startupPeriod,
		"timeoutSeconds":   startupTimeout,
		"failureThreshold": startupFailures,
	}
	container["livenessProbe"] = map[string]any{
		"httpGet":          map[string]any{"path": livenessPath, "port": containerPort},
		"periodSeconds":    livenessPeriod,
		"timeoutSeconds":   livenessTimeout,
		"failureThreshold": livenessFailures,
	}
	// Cloud Run allocates a service's CPU only while it handles a request,
	// and bills its instances for that time and their starts and stops,
	// unless the service sets its resources, as every one here does
	// (lowerWorkload): then cpuIdle must say so, or each instance keeps its
	// CPU and is billed for its whole life. A server that works between
	// requests keeps its CPU with cpuAlwaysAllocated. Cloud Run returns no
	// cpuIdle for false, so that server leaves it out, as a service without
	// a minimum leaves out minInstanceCount. A job's task always has its
	// CPU and takes no cpuIdle.
	if always, _ := d.Settings["cpuAlwaysAllocated"].(bool); !always {
		container["resources"].(map[string]any)["cpuIdle"] = true
	}
	// Cloud Run returns no minInstanceCount for a service without a
	// minimum, so a 0 in the template would differ from the service on
	// every preview: a minimum goes in only when it is above 0.
	scaling := map[string]any{}
	if n, ok := d.Settings["minInstances"]; ok && fmt.Sprint(n) != "0" {
		scaling["minInstanceCount"] = n
	}
	if n, ok := d.Settings["maxInstances"]; ok {
		scaling["maxInstanceCount"] = n
	}
	if len(scaling) > 0 {
		template["scaling"] = scaling
	}
	if n, ok := d.Settings["concurrency"]; ok {
		template["maxInstanceRequestConcurrency"] = n
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

// workload is what lowerWorkload makes of a server or a job for the Cloud
// Run resource that runs it: its one container, and the template that
// holds the container. A service's template is a revision's and a job's a
// task's, which take the members here under the same names.
type workload struct {
	// container has the image, the resource limits, the environment and
	// the Cloud SQL mount; the platform adds what only its resource takes.
	container map[string]any

	// template has the account the container runs as, the Cloud SQL
	// volume and the VPC egress; the platform adds the container.
	template map[string]any
}

// lowerWorkload lowers what a server and a job share (sections 7.2 and
// 8.7, D52):
//
//   - a service account named after the deployable, and the trace agent
//     role for it, since the entrypoint exports traces to Cloud Trace
//     (section 7.5);
//   - a Secret Manager secret and an accessor grant per secret binding
//     (secretNodes);
//   - its config as the container's environment variables: a literal as
//     its value, a parameter as a reference to it, a secret as a reference
//     to its Secret Manager secret, a derived field as one variable per
//     member of the value its edge's connector derived;
//   - the image's repository path, which the deploy pins to a digest
//     (section 11.2), and the cpu and memory settings;
//   - the Cloud SQL volume, holding the instances its connections name,
//     mounted where the Cloud SQL connector's sockets go (section 7.4);
//   - for a deployable that calls a server other than itself, Direct VPC
//     egress through the environment's network (networkNodes): a call to
//     the callee's run.app URL from the VPC counts as internal, which an
//     internal server's ingress requires.
func lowerWorkload(ctx registry.PlatformContext) (registry.Lowered, workload, error) {
	d := ctx.Deployable
	env := ctx.Environment
	v := valuesOf(env)
	if err := checkLength("the service account id of "+d.Name, d.ResourceName, 6, 30, renameOf(d)); err != nil {
		return registry.Lowered{}, workload{}, err
	}
	account := d.Name + ".account"
	member := ir.Output{Resource: account, Name: "member"}
	var out registry.Lowered
	add := func(res ...*ir.Resource) { out.Resources = append(out.Resources, res...) }
	add(
		&ir.Resource{ID: account, Type: TypeAccount, Phase: ir.PhaseInfrastructure, Properties: map[string]any{
			"project":     v.project,
			"accountId":   d.ResourceName,
			"displayName": fmt.Sprintf("%s %s %s %s", env.Stack, env.Name, d.Kind, d.Name),
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
				return registry.Lowered{}, workload{}, fmt.Errorf("binding %s: %w", b.Field, err)
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
				return registry.Lowered{}, workload{}, fmt.Errorf("binding %s: %w", b.Field, err)
			}
			for _, variable := range vars {
				envs = append(envs, map[string]any{"name": variable.Name, "value": variable.Value})
			}
			if instance := cloudSQLInstance(b.Value); instance != nil && !containsValue(instances, instance) {
				instances = append(instances, instance)
			}
		default:
			return registry.Lowered{}, workload{}, fmt.Errorf("binding %s has source %q", b.Field, b.Source)
		}
	}

	w := workload{
		container: map[string]any{
			"image": imageRepository(v, env.Stack, d.Name),
			"resources": map[string]any{"limits": map[string]any{
				"cpu":    settingString(d.Settings, "cpu", defaultCPU),
				"memory": settingString(d.Settings, "memory", defaultMemory),
			}},
		},
		template: map[string]any{
			"serviceAccount": ir.Output{Resource: account, Name: "email"},
		},
	}
	if len(envs) > 0 {
		w.container["envs"] = envs
	}
	if len(instances) > 0 {
		w.template["volumes"] = []any{map[string]any{
			"name":             "cloudsql",
			"cloudSqlInstance": map[string]any{"instances": instances},
		}}
		w.container["volumeMounts"] = []any{map[string]any{"name": "cloudsql", "mountPath": "/cloudsql"}}
	}
	if callsAnother(d) {
		add(networkNodes(env, v)...)
		w.template["vpcAccess"] = map[string]any{
			"egress": "ALL_TRAFFIC",
			"networkInterfaces": []any{map[string]any{
				"network":    ir.Output{Resource: networkID, Name: "name"},
				"subnetwork": ir.Output{Resource: subnetID, Name: "name"},
			}},
		}
	}
	return out, w, nil
}

// callsAnother reports whether a server calls an API another server
// serves. A call to an API it serves itself stays on loopback. A job
// serves its API alone and runs apart from every server (D52), so it
// reaches each API its API calls over the network, the server of its own
// API's siblings included.
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
