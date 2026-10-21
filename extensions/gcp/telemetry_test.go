package gcp_test

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/gcp"
)

// otelEnv returns the OTEL_ variables of the container of node id, by
// name, each value as its JSON.
func otelEnv(t *testing.T, env *ir.ResolvedEnvironment, id string) map[string]string {
	t.Helper()
	res := node(t, env, id)
	template, _ := res.Properties["template"].(map[string]any)
	if inner, ok := template["template"].(map[string]any); ok {
		// A job's template holds its task's.
		template = inner
	}
	containers, _ := template["containers"].([]any)
	if len(containers) != 1 {
		t.Fatalf("%s has containers %v", id, template["containers"])
	}
	envs, _ := containers[0].(map[string]any)["envs"].([]any)
	out := map[string]string{}
	for _, e := range envs {
		m := e.(map[string]any)
		name := m["name"].(string)
		if !strings.HasPrefix(name, "OTEL_") {
			continue
		}
		value, err := json.Marshal(m["value"])
		if err != nil {
			t.Fatal(err)
		}
		out[name] = string(value)
	}
	return out
}

// hasTraceAgent reports whether the graph grants the deployable's account
// the trace agent role.
func hasTraceAgent(t *testing.T, env *ir.ResolvedEnvironment, deployable string) bool {
	t.Helper()
	res := env.Resources.Resource(deployable + ".trace-agent")
	if res == nil {
		return false
	}
	if res.Properties["role"] != "roles/cloudtrace.agent" || res.Type != gcp.TypeProjectIAMMember {
		t.Errorf("%s.trace-agent is %+v", deployable, res)
	}
	return true
}

// TestTelemetryTurnsExportOn (D67, amended): an environment's telemetry
// turns trace export on for each server, job and worker. To Cloud Trace,
// the default, each gets the endpoint, its name, its resource attributes,
// the image a part of their own, the sampler its sample ratio asks for,
// and the trace agent role for its account; a deployable whose settings
// turn it off gets none of them. Without telemetry, nothing exports and
// nothing holds the role.
func TestTelemetryTurnsExportOn(t *testing.T) {
	reg := assemble(t)
	prod := resolve(t, reg, shop(), stacktest.AcmeShop(), "Production")
	for id, deployable := range map[string]string{
		"shop-api.service":            "shop-api",
		"Orders.service":              "Orders",
		fulfilOrders + ".worker-pool": fulfilOrders,
	} {
		got := otelEnv(t, prod, id)
		want := map[string]string{
			"OTEL_EXPORTER_OTLP_ENDPOINT": `"https://telemetry.googleapis.com"`,
			"OTEL_SERVICE_NAME":           `"` + deployable + `"`,
			"OTEL_RESOURCE_ATTRIBUTES": `{"$concat":["deployment.environment.name=Production,gcp.project_id=acme-prod,cloud.provider=gcp,cloud.region=us-east1,service.version=",` +
				`"us-east1-docker.pkg.dev/acme-prod/shop/` + strings.ToLower(deployable) + `"]}`,
			"OTEL_TRACES_SAMPLER":     `"parentbased_traceidratio"`,
			"OTEL_TRACES_SAMPLER_ARG": `"0.25"`,
		}
		if !maps.Equal(got, want) {
			t.Errorf("%s sets\n%v\nwant\n%v", id, got, want)
		}
		if !hasTraceAgent(t, prod, deployable) {
			t.Errorf("%s's account holds no trace agent role", deployable)
		}
	}
	if got := otelEnv(t, prod, shipOrders+".job"); len(got) != 0 || hasTraceAgent(t, prod, shipOrders) {
		t.Errorf("ShipOrders, whose telemetry is off, sets %v", got)
	}

	staging := resolve(t, reg, shop(), stacktest.AcmeShop(), "Staging")
	for _, d := range staging.Deployables {
		if hasTraceAgent(t, staging, d.Name) {
			t.Errorf("%s holds the trace agent role in Staging, which exports nothing", d.Name)
		}
	}
	if got := otelEnv(t, staging, "shop-api.service"); len(got) != 0 {
		t.Errorf("shop-api sets %v in Staging, which exports nothing", got)
	}
}

// TestTelemetryToACollector (D67, amended): an endpoint in the
// environment's telemetry is a collector's, which takes no Google token:
// each deployable exports there, with no sampler unless one is asked for,
// and no account holds the trace agent role. A preview member names
// itself with its parameter.
func TestTelemetryToACollector(t *testing.T) {
	s := shop()
	staging := s.Environment("Staging")
	staging.Values = map[string]any{"project": "acme-staging", "region": "us-east1", "telemetry": map[string]any{"endpoint": "http://otel-collector.acme.internal:4318"}}
	reg := assemble(t)
	for _, name := range []string{"Staging", "Preview"} {
		env := resolve(t, reg, s, stacktest.AcmeShop(), name)
		got := otelEnv(t, env, "shop-api.service")
		if got["OTEL_EXPORTER_OTLP_ENDPOINT"] != `"http://otel-collector.acme.internal:4318"` || got["OTEL_TRACES_SAMPLER"] != "" {
			t.Errorf("%s: shop-api sets %v", name, got)
		}
		for _, d := range env.Deployables {
			if hasTraceAgent(t, env, d.Name) {
				t.Errorf("%s: %s holds the trace agent role to export to a collector", name, d.Name)
			}
		}
		if name == "Preview" && !strings.HasPrefix(got["OTEL_RESOURCE_ATTRIBUTES"], `{"$concat":["deployment.environment.name=Preview-pr",{"$parameter":"pr"},",gcp.project_id=acme-staging,`) {
			t.Errorf("a preview member's resource attributes are %s", got["OTEL_RESOURCE_ATTRIBUTES"])
		}
	}
}

// ordersSettings returns the settings e gives the server Orders.
func ordersSettings(e *ir.Environment) *ir.DeployableSettings {
	for _, set := range e.Settings {
		if set.Of.Deployable == "Orders" {
			return set
		}
	}
	set := &ir.DeployableSettings{Of: ir.DeployableRef{Deployable: "Orders"}}
	e.Settings = append(e.Settings, set)
	return set
}

// TestTelemetryOfOneDeployable (D67, amended): a deployable's settings turn
// its export on in an environment without telemetry, to Cloud Trace, with
// the role, and no other deployable exports.
func TestTelemetryOfOneDeployable(t *testing.T) {
	s := shop()
	ordersSettings(s.Environment("Staging")).Values = map[string]any{"telemetry": true}
	env := resolve(t, assemble(t), s, stacktest.AcmeShop(), "Staging")
	if got := otelEnv(t, env, "Orders.service"); got["OTEL_EXPORTER_OTLP_ENDPOINT"] != `"https://telemetry.googleapis.com"` || !hasTraceAgent(t, env, "Orders") {
		t.Errorf("Orders, whose settings turn export on, sets %v", got)
	}
	if got := otelEnv(t, env, "shop-api.service"); len(got) != 0 || hasTraceAgent(t, env, "shop-api") {
		t.Errorf("shop-api sets %v", got)
	}
}

// TestTelemetryRefuses (D67, amended): resolution refuses an endpoint that
// is no http or https URL, a sample ratio outside 0 to 1, a member it does
// not know, Google's endpoint over http, over which no runtime sends the
// account's token, and a deployable's telemetry that is not a boolean.
func TestTelemetryRefuses(t *testing.T) {
	reg := assemble(t)
	for _, c := range []struct {
		name  string
		edit  func(*ir.Environment)
		match string
	}{
		{"endpoint", func(e *ir.Environment) {
			e.Values["telemetry"] = map[string]any{"endpoint": "grpc://collector:4317"}
		}, "telemetry"},
		{"ratio", func(e *ir.Environment) { e.Values["telemetry"] = map[string]any{"sampleRatio": 1.5} }, "telemetry"},
		{"unknown", func(e *ir.Environment) { e.Values["telemetry"] = map[string]any{"headers": "a=b"} }, "telemetry"},
		{"google over http", func(e *ir.Environment) {
			e.Values["telemetry"] = map[string]any{"endpoint": "http://telemetry.googleapis.com"}
		}, "Google's OTLP endpoint takes https alone"},
		{"setting", func(e *ir.Environment) { ordersSettings(e).Values = map[string]any{"telemetry": "on"} }, "telemetry"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := shop()
			c.edit(s.Environment("Staging"))
			_, err := stack.Resolve(reg, stack.Input{Stack: s, Services: stacktest.AcmeShop(), Environment: "Staging"})
			if err == nil || !strings.Contains(err.Error(), c.match) {
				t.Errorf("resolve = %v, want an error naming %s", err, c.match)
			}
		})
	}
}

// TestBootstrapEnablesTheTraceAPIs (D67, amended): the bootstrap of an
// environment with Cloud Run enables the Telemetry API, whose OTLP
// endpoint takes the spans, and Cloud Trace, whether it exports there, as
// Production does, or not, as Staging, whose project another environment
// may export from: bootstrap sees its own environment alone.
func TestBootstrapEnablesTheTraceAPIs(t *testing.T) {
	for _, env := range []string{"Production", "Staging"} {
		f := newDeployFixture(t, false)
		resolved := resolve(t, f.reg, shop(), stacktest.AcmeShop(), env)
		target, _ := f.reg.Target(gcp.Target)
		if _, err := target.Bootstrap.Bootstrap(context.Background(), registry.BootstrapRequest{
			Environment: resolved, Repository: "acme/shop", Provisioner: f.prov, Dir: t.TempDir(),
		}); err != nil {
			t.Fatal(err)
		}
		changes := f.cloud.Changes()
		for _, api := range []string{"enable telemetry.googleapis.com", "enable cloudtrace.googleapis.com"} {
			if !slices.Contains(changes, api) {
				t.Errorf("%s's bootstrap did not %s", env, api)
			}
		}
	}
}
