package cloudflare_test

import (
	"slices"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/stack"

	"github.com/parable-work/superschematic/extensions/cloudflare"
)

// Trace export on Workers (D67, D64 amended).

// telemetryStack is the web fixture with Staging's telemetry on, to a
// collector, sampling a quarter of the traces.
func telemetryStack() *ir.Stack {
	s := webStack()
	values := map[string]any{"accountId": accountID, "workersSubdomain": "acme", "telemetry": map[string]any{"endpoint": "https://otel.acme.dev", "sampleRatio": 0.25}}
	s.Environments[0].Values = values
	return s
}

// TestTelemetryBindings: with Staging's telemetry on, the server's and the
// job's versions get the OTEL_ bindings, the service named after the
// deployable, the resource naming the environment, the account and the
// bundle's digest the deploy pins, and the sampler; Production, which turns
// none on, gets none; a member of Preview names its environment with its
// parameter; every server's and job's version names nodejs_als, and the
// site's Worker, which runs the extension's own script, names none.
func TestTelemetryBindings(t *testing.T) {
	reg := assemble(t)
	env, errs := resolveOver(t, reg, telemetryStack(), webServices(), "Staging")
	if errs != nil {
		t.Fatal(errs)
	}
	for _, d := range []string{"shop-storefront", sweepCartsJob} {
		version := node(t, env, d+".version")
		bindings := bindingsOf(t, version)
		for name, want := range map[string]string{
			"OTEL_EXPORTER_OTLP_ENDPOINT": `{"name":"OTEL_EXPORTER_OTLP_ENDPOINT","text":"https://otel.acme.dev","type":"plain_text"}`,
			"OTEL_SERVICE_NAME":           `{"name":"OTEL_SERVICE_NAME","text":"` + d + `","type":"plain_text"}`,
			"OTEL_RESOURCE_ATTRIBUTES": `{"name":"OTEL_RESOURCE_ATTRIBUTES","text":"deployment.environment.name=Staging,cloud.provider=cloudflare,cloud.platform=cloudflare_workers,` +
				`cloud.account.id=` + accountID + `,service.version=sha256:{bundle-digest}","type":"plain_text"}`,
			"OTEL_TRACES_SAMPLER":     `{"name":"OTEL_TRACES_SAMPLER","text":"parentbased_traceidratio","type":"plain_text"}`,
			"OTEL_TRACES_SAMPLER_ARG": `{"name":"OTEL_TRACES_SAMPLER_ARG","text":"0.25","type":"plain_text"}`,
		} {
			if bindings[name] != want {
				t.Errorf("%s's binding %s is\n%s, want\n%s", d, name, bindings[name], want)
			}
		}
		if got := jsonOf(t, version.Properties["compatibilityFlags"]); got != `["nodejs_als"]` {
			t.Errorf("%s's version names compatibility flags %s", d, got)
		}
		var names []string
		for _, b := range version.Properties["bindings"].([]any) {
			names = append(names, b.(map[string]any)["name"].(string))
		}
		if !slices.IsSorted(names) {
			t.Errorf("%s's bindings are not sorted: %v", d, names)
		}
	}
	if _, ok := node(t, env, "storefront-web.version").Properties["compatibilityFlags"]; ok {
		t.Error("the site's version names compatibility flags")
	}

	production := resolveWeb(t, reg, "Production")
	if _, ok := bindingsOf(t, node(t, production, "shop-storefront.version"))["OTEL_EXPORTER_OTLP_ENDPOINT"]; ok {
		t.Error("Production, which turns no telemetry on, exports")
	}
	if got := jsonOf(t, node(t, production, "shop-storefront.version").Properties["compatibilityFlags"]); got != `["nodejs_als"]` {
		t.Errorf("Production's version names compatibility flags %s", got)
	}

	preview, errs := resolveOver(t, reg, telemetryStack(), webServices(), "Preview")
	if errs != nil {
		t.Fatal(errs)
	}
	attributes := node(t, preview, "shop-storefront.version")
	for _, b := range attributes.Properties["bindings"].([]any) {
		if b := b.(map[string]any); b["name"] == "OTEL_RESOURCE_ATTRIBUTES" {
			if got := jsonOf(t, b["text"]); got != `{"$concat":["deployment.environment.name=Preview-pr",{"$parameter":"pr"},",cloud.provider=cloudflare,cloud.platform=cloudflare_workers,cloud.account.id=`+accountID+`,service.version=sha256:{bundle-digest}"]}` {
				t.Errorf("the member's resource attributes are %s", got)
			}
		}
	}
}

// TestTelemetryOfOneDeployable: the job's telemetry: false turns its export
// off in Staging, which turns it on; a server's telemetry: true in an
// environment with no telemetry names no endpoint and is refused; and a
// config field the target would set is refused.
func TestTelemetryOfOneDeployable(t *testing.T) {
	reg := assemble(t)
	s := telemetryStack()
	off := s.Environments[0].Settings[0]
	off.Values = map[string]any{"telemetry": false}
	env, errs := resolveOver(t, reg, s, webServices(), "Staging")
	if errs != nil {
		t.Fatal(errs)
	}
	if _, ok := bindingsOf(t, node(t, env, sweepCartsJob+".version"))["OTEL_EXPORTER_OTLP_ENDPOINT"]; ok {
		t.Error("the job whose telemetry is off exports")
	}
	if _, ok := bindingsOf(t, node(t, env, "shop-storefront.version"))["OTEL_EXPORTER_OTLP_ENDPOINT"]; !ok {
		t.Error("the server exports nothing")
	}

	s = webStack()
	s.Environments[0].Settings[0].Values = map[string]any{"telemetry": true}
	_, errs = resolveOver(t, reg, s, webServices(), "Staging")
	refusedWith(t, errs, stack.CodeLowering, "job "+sweepCartsJob+" turns telemetry on, and environment Staging names no endpoint for it")

	services := webServices()
	for i := range services {
		if services[i].Name == "shop-storefront" {
			def := "x"
			services[i].Config.Fields = append(services[i].Config.Fields, stack.ConfigField{Name: "OTEL_SERVICE_NAME", Required: true, Default: &def})
		}
	}
	_, errs = resolveOver(t, reg, telemetryStack(), services, "Staging")
	refusedWith(t, errs, stack.CodeLowering, "binding OTEL_SERVICE_NAME: the target sets OTEL_SERVICE_NAME itself")
}

// TestTelemetryValues: an endpoint that is no https URL, or one with
// userinfo or a query, or plain http but to loopback, a ratio above 1 and
// telemetry with no endpoint are refused; http to loopback is taken.
func TestTelemetryValues(t *testing.T) {
	reg := assemble(t)
	for name, telemetry := range map[string]map[string]any{
		"no URL":      {"endpoint": "otel.acme.dev"},
		"userinfo":    {"endpoint": "https://key:secret@otel.acme.dev"},
		"plain http":  {"endpoint": "http://otel.acme.dev"},
		"a query":     {"endpoint": "https://otel.acme.dev/v1?token=x"},
		"a ratio":     {"endpoint": "https://otel.acme.dev", "sampleRatio": 2.0},
		"no endpoint": {"sampleRatio": 0.5},
	} {
		t.Run(name, func(t *testing.T) {
			s := webStack()
			s.Environments[0].Values = map[string]any{"accountId": accountID, "workersSubdomain": "acme", "telemetry": telemetry}
			_, errs := resolveOver(t, reg, s, webServices(), "Staging")
			if errs == nil || len(errs.Of(stack.CodeInvalidValues)) == 0 {
				t.Errorf("telemetry %v resolved: %v", telemetry, errs)
			}
		})
	}
	for _, endpoint := range []string{"http://localhost:4318", "http://127.0.0.1:4318/otlp", "https://otel.acme.dev:4318/otlp"} {
		s := webStack()
		s.Environments[0].Values = map[string]any{"accountId": accountID, "workersSubdomain": "acme", "telemetry": map[string]any{"endpoint": endpoint}}
		if _, errs := resolveOver(t, reg, s, webServices(), "Staging"); errs != nil {
			t.Errorf("endpoint %s: %v", endpoint, errs)
		}
	}
	if got := cloudflare.WorkersCompatibilityFlags; !slices.Equal(got, []string{"nodejs_als"}) {
		t.Errorf("WorkersCompatibilityFlags = %v", got)
	}
}
