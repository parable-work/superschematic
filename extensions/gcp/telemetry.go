package gcp

import (
	"fmt"
	"net/url"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// A deployable's trace export on gcp (D67, amended; docs/stack-model.md,
// section 7.5). The runtimes export when OpenTelemetry's own variables
// name an endpoint (D67), and nothing else on Cloud Run sets them, so the
// target sets them from a setting:
//
//   - the environment's values take `telemetry`, which turns export on for
//     each server, job and worker: `{}` sends to Cloud Trace's OTLP
//     endpoint, https://telemetry.googleapis.com, with the access token of
//     the deployable's own account, which each runtime gets from the
//     metadata server; `{ "endpoint": "<url>" }` sends to an OTLP/HTTP
//     collector there instead, with no token; `sampleRatio` samples that
//     share of the traces that start in the deployable;
//   - a deployable's settings take `telemetry: false`, which turns export
//     off for it in an environment that turns it on, or `telemetry: true`,
//     which turns it on, to the environment's endpoint or Cloud Trace's,
//     in one that does not.
//
// A deployable that exports gets OTEL_EXPORTER_OTLP_ENDPOINT,
// OTEL_SERVICE_NAME, its name, as the entrypoint's default is, and
// OTEL_RESOURCE_ATTRIBUTES: the environment (a preview member's name with
// its parameters), the project, which Cloud Trace's endpoint reads the
// spans' project from, the provider and region, and the image, as the
// deploy pins it to its digest, as service.version; with a sample ratio,
// OTEL_TRACES_SAMPLER and its argument too. One that exports to Cloud
// Trace gets the trace agent role on the project for its account, which
// holds telemetry.traces.write, and only then; and the bootstrap enables
// the Telemetry and Cloud Trace APIs for an environment with such a grant.

// CloudTraceEndpoint is Cloud Trace's OTLP endpoint, where a deployable
// exports when its environment's telemetry names no endpoint.
const CloudTraceEndpoint = "https://telemetry.googleapis.com"

// traceAgentRole is the role a deployable that exports to Cloud Trace
// holds on the project: it holds telemetry.traces.write, which the OTLP
// endpoint checks.
const traceAgentRole = "roles/cloudtrace.agent"

// The variables the target sets on a deployable that exports.
var telemetryVariables = []string{
	"OTEL_EXPORTER_OTLP_ENDPOINT",
	"OTEL_SERVICE_NAME",
	"OTEL_RESOURCE_ATTRIBUTES",
	"OTEL_TRACES_SAMPLER",
	"OTEL_TRACES_SAMPLER_ARG",
}

// telemetrySettings is how a deployable exports its traces, when it does.
type telemetrySettings struct {
	// endpoint is the OTLP/HTTP endpoint it exports to.
	endpoint string

	// sampleRatio is the share of the traces that start in it it samples;
	// sampled is false for every trace.
	sampleRatio float64
	sampled     bool
}

// cloudTrace reports whether the endpoint is Google's, which takes the
// account's access token, and so needs the trace agent role.
func (t telemetrySettings) cloudTrace() bool {
	u, err := url.Parse(t.endpoint)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "googleapis.com" || strings.HasSuffix(host, ".googleapis.com")
}

// telemetryOf returns how the deployable d of env exports its traces, or
// nil when it does not.
func telemetryOf(env registry.StackEnvironment, d ir.ResolvedDeployable) *telemetrySettings {
	envTelemetry, envOn := env.Values["telemetry"].(map[string]any)
	on := envOn
	if setting, ok := d.Settings["telemetry"].(bool); ok {
		on = setting
	}
	if !on {
		return nil
	}
	t := &telemetrySettings{endpoint: CloudTraceEndpoint}
	if endpoint, ok := envTelemetry["endpoint"].(string); ok && endpoint != "" {
		t.endpoint = endpoint
	}
	if ratio, ok := envTelemetry["sampleRatio"].(float64); ok {
		t.sampleRatio, t.sampled = ratio, true
	}
	return t
}

// telemetryEnv returns the container's environment variables for t, and
// refuses a binding of d that names one of them.
func telemetryEnv(env registry.StackEnvironment, v values, d ir.ResolvedDeployable, t *telemetrySettings) ([]any, error) {
	for _, b := range d.Bindings {
		for _, name := range telemetryVariables {
			if b.Field == name {
				return nil, fmt.Errorf("binding %s: the target sets %s itself, since the environment's telemetry turns export on (D67, amended)", b.Field, name)
			}
		}
	}
	var attributes ir.Concat
	switch prefix := join("deployment.environment.name=", suffixed(env, env.Name, "-"),
		",gcp.project_id=", v.project, ",cloud.provider=gcp,cloud.region=", v.region, ",service.version=").(type) {
	case ir.Concat:
		attributes = append(attributes, prefix...)
	default:
		attributes = append(attributes, prefix)
	}
	// The image as a part of its own, so the deploy pins it to its digest
	// here as it does the container's (stackdeploy.PinImages).
	attributes = append(attributes, imageRepository(v, env.Stack, d.Name))
	vars := []any{
		map[string]any{"name": "OTEL_EXPORTER_OTLP_ENDPOINT", "value": t.endpoint},
		map[string]any{"name": "OTEL_SERVICE_NAME", "value": d.Name},
		map[string]any{"name": "OTEL_RESOURCE_ATTRIBUTES", "value": attributes},
	}
	if t.sampled {
		vars = append(vars,
			map[string]any{"name": "OTEL_TRACES_SAMPLER", "value": "parentbased_traceidratio"},
			map[string]any{"name": "OTEL_TRACES_SAMPLER_ARG", "value": fmt.Sprint(t.sampleRatio)},
		)
	}
	return vars, nil
}
