package cloudflare

import (
	"fmt"
	"slices"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// A server's or a job's trace export on Workers (D67, D64 amended;
// docs/stack-model.md, section 18.5). The runtime's ./workers-telemetry
// exports when OpenTelemetry's own variables, which a Worker reads as its
// string bindings, name an endpoint, and nothing else sets them, so the
// target sets them from a setting, as gcp's does:
//
//   - the environment's values take `telemetry`, `{ "endpoint": "<url>" }`,
//     which turns export on for each server and job, to that OTLP/HTTP
//     collector, and `sampleRatio`, which samples that share of the traces
//     that start in the Worker. Cloudflare has no OTLP endpoint of its own
//     for traces a Worker sends, so the endpoint is required;
//   - a server's or a job's settings take `telemetry: false`, which turns
//     its export off in an environment that turns it on, or `true`.
//
// A Worker that exports gets plain_text bindings: OTEL_EXPORTER_OTLP_ENDPOINT,
// OTEL_SERVICE_NAME, its deployable's name, as the entrypoint's default is,
// and OTEL_RESOURCE_ATTRIBUTES: the environment (a preview member's name
// with its parameters), the provider, the account, and the bundle's digest
// as service.version, which the deploy pins; with a sample ratio,
// OTEL_TRACES_SAMPLER and its argument too. A collector that needs a
// header, from a secret, waits for a secret binding of its own, as on gcp.
//
// Every Worker version of a server or a job names WorkersCompatibilityFlags:
// the runtime carries a span's context through AsyncLocalStorage, which
// workerd gives under nodejs_als without the rest of Node, whether or not
// the environment exports, since the bundle is the same in every
// environment.

// WorkersCompatibilityFlags are the compatibility flags every Worker
// version of a server or a job names: nodejs_als, workerd's
// AsyncLocalStorage alone, which the runtime's ./workers-telemetry imports
// as node:async_hooks. workerd gives node: modules with no flag from
// compatibility date 2026-08-04, CompatibilityDate among them; the flag
// keeps a server whose settings name an earlier date running.
var WorkersCompatibilityFlags = []string{"nodejs_als"}

// The variables the target sets on a Worker that exports.
var telemetryVariables = []string{
	"OTEL_EXPORTER_OTLP_ENDPOINT",
	"OTEL_SERVICE_NAME",
	"OTEL_RESOURCE_ATTRIBUTES",
	"OTEL_TRACES_SAMPLER",
	"OTEL_TRACES_SAMPLER_ARG",
}

// telemetryValues is the schema of an environment's `telemetry` value. The
// endpoint is an https URL with no userinfo, query or fragment: the
// bindings are plain text, which the dashboard and Workers Logs show, and a
// span leaves the account in clear over http. http is taken for a
// collector on loopback alone, as under a local workerd.
const telemetryValues = `{
      "type": "object",
      "required": ["endpoint"],
      "properties": {
        "endpoint": {"type": "string", "pattern": "^(https://[^/?#@\\s,]+|http://(localhost|127\\.0\\.0\\.1|\\[::1\\])(:[0-9]+)?)(/[^?#\\s,]*)?$"},
        "sampleRatio": {"type": "number", "minimum": 0, "maximum": 1}
      },
      "additionalProperties": false
    }`

// telemetrySettings is how a Worker exports its traces, when it does.
type telemetrySettings struct {
	endpoint    string
	sampleRatio float64
	sampled     bool
}

// telemetryOf returns how deployable d of env exports its traces, or nil
// when it does not. It refuses a deployable that turns export on in an
// environment whose telemetry names no endpoint.
func telemetryOf(env registry.StackEnvironment, d ir.ResolvedDeployable) (*telemetrySettings, error) {
	envTelemetry, envOn := env.Values["telemetry"].(map[string]any)
	on := envOn
	if setting, ok := d.Settings["telemetry"].(bool); ok {
		on = setting
	}
	if !on {
		return nil, nil
	}
	endpoint, _ := envTelemetry["endpoint"].(string)
	if endpoint == "" {
		return nil, fmt.Errorf("%s %s turns telemetry on, and environment %s names no endpoint for it; set telemetry: { endpoint } beside accountId, the OTLP/HTTP collector Workers export to", d.Kind, d.Name, env.Name)
	}
	t := &telemetrySettings{endpoint: endpoint}
	if ratio, ok := envTelemetry["sampleRatio"].(float64); ok {
		t.sampleRatio, t.sampled = ratio, true
	}
	return t, nil
}

// addTelemetry adds to version the bindings of t, the trace export of
// deployable d, when it exports, and refuses a binding of d's config that
// names one of them.
func addTelemetry(env registry.StackEnvironment, v accountValues, d ir.ResolvedDeployable, version map[string]any) error {
	t, err := telemetryOf(env, d)
	if err != nil || t == nil {
		return err
	}
	for _, b := range d.Bindings {
		if slices.Contains(telemetryVariables, b.Field) {
			return fmt.Errorf("binding %s: the target sets %s itself, since the environment's telemetry turns export on (D67, D64 amended)", b.Field, b.Field)
		}
	}
	// The bundle's digest as a part of its own, which the deploy pins.
	attributes := join("deployment.environment.name=", suffixed(env, env.Name, "-"),
		",cloud.provider=cloudflare,cloud.platform=cloudflare_workers,cloud.account.id=", v.accountID,
		",service.version=sha256:", ir.BundleDigestToken)
	bindings := []map[string]any{
		{"type": bindingPlainText, "name": "OTEL_EXPORTER_OTLP_ENDPOINT", "text": t.endpoint},
		{"type": bindingPlainText, "name": "OTEL_SERVICE_NAME", "text": d.Name},
		{"type": bindingPlainText, "name": "OTEL_RESOURCE_ATTRIBUTES", "text": attributes},
	}
	if t.sampled {
		bindings = append(bindings,
			map[string]any{"type": bindingPlainText, "name": "OTEL_TRACES_SAMPLER", "text": "parentbased_traceidratio"},
			map[string]any{"type": bindingPlainText, "name": "OTEL_TRACES_SAMPLER_ARG", "text": fmt.Sprint(t.sampleRatio)},
		)
	}
	for _, b := range bindings {
		if err := addBinding(version, b); err != nil {
			return fmt.Errorf("%s %s: %w", d.Kind, d.Name, err)
		}
	}
	return nil
}
