// Package telemetry exports the traces of a stack's server, job or worker
// over OTLP (D67; docs/stack-model.md, section 8.1). The generated
// entrypoint calls Start once with what it knows of its deployable, and
// everything else comes from OpenTelemetry's own environment variables:
//
//   - OTEL_EXPORTER_OTLP_ENDPOINT, or OTEL_EXPORTER_OTLP_TRACES_ENDPOINT,
//     turns export on. Without either, Start sets nothing up: no exporter,
//     no background work, and the global tracer stays a no-op, so Handler
//     and Transport return what they wrap.
//   - OTEL_EXPORTER_OTLP_HEADERS, OTEL_EXPORTER_OTLP_TIMEOUT and their
//     per-signal forms reach the exporter, which reads them itself.
//   - An endpoint under googleapis.com, Cloud Trace's OTLP endpoint, gets
//     the access token of the workload's account from the metadata server
//     on each export, unless a headers variable names an authorization
//     header (google.go).
//   - OTEL_EXPORTER_OTLP_PROTOCOL, and its traces form, may only name
//     http/protobuf, the one protocol the runtime exports over.
//   - OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES override the defaults
//     the entrypoint gives.
//   - OTEL_TRACES_SAMPLER and OTEL_TRACES_SAMPLER_ARG pick the sampler,
//     parentbased_always_on unless set.
//   - OTEL_PROPAGATORS picks the propagators, tracecontext and baggage
//     unless set.
//   - OTEL_SDK_DISABLED=true turns export off whatever else is set.
//
// A value the runtime does not support stops Start with an error that
// names the variable, as a refused config field stops a server.
//
// With export on, Handler makes a server span per request, which the
// generated API's request log middleware names by its route template,
// Transport a client span per call another service's SDK makes, carrying
// W3C traceparent and tracestate, and Job a span per run of a job. The
// worker package makes a span per message it handles. The function Start
// returns flushes the spans and stops the exporter, so a job's run reaches
// the collector before the process exits.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/zap"
)

// The variables Start reads beside those the exporter and the resource
// read themselves.
const (
	EndpointVariable       = "OTEL_EXPORTER_OTLP_ENDPOINT"
	TracesEndpointVariable = "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"
	ProtocolVariable       = "OTEL_EXPORTER_OTLP_PROTOCOL"
	TracesProtocolVariable = "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"
	SamplerVariable        = "OTEL_TRACES_SAMPLER"
	SamplerArgVariable     = "OTEL_TRACES_SAMPLER_ARG"
	PropagatorsVariable    = "OTEL_PROPAGATORS"
	DisabledVariable       = "OTEL_SDK_DISABLED"
)

// Protocol is the one OTLP protocol the runtime exports over.
const Protocol = "http/protobuf"

// The resource attributes Defaults sets beside service.name.
const (
	// StackAttribute is the stack the deployable is of.
	StackAttribute = "service.namespace"
	// KindAttribute is the deployable's kind: server, job or worker.
	KindAttribute = "superschematic.deployable.kind"
)

// JobAttribute names the job a job's span runs.
const JobAttribute = "superschematic.job"

// flushTimeout bounds how long the function Start returns waits for the
// spans still buffered to reach the collector.
const flushTimeout = 5 * time.Second

// exportTimeout bounds one export through the client that sends Google's
// access token (google.go), as the exporter's own client's default does.
const exportTimeout = 10 * time.Second

// scope is the instrumentation scope of the spans the package makes
// itself.
const scope = "github.com/parable-work/superschematic/runtime/http/go/telemetry"

// healthPaths are the paths Handler makes no span for: the platform's
// probes, which the generated server answers.
var healthPaths = map[string]bool{"/healthz": true, "/readyz": true}

// enabled is whether Start turned export on.
var enabled atomic.Bool

// Defaults is what the generated entrypoint knows of its deployable, the
// defaults of its resource, which OTEL_SERVICE_NAME and
// OTEL_RESOURCE_ATTRIBUTES override.
type Defaults struct {
	// Service is the deployable's name, the default service.name.
	Service string

	// Stack is the stack's name, service.namespace.
	Stack string

	// Kind is the deployable's kind: server, job or worker.
	Kind string

	// Logger logs that export is on, and what the SDK reports, such as an
	// export that failed; nil logs nothing.
	Logger *zap.Logger
}

// Start sets up export from the environment, as the package says, and
// returns the function that flushes the spans still buffered and stops the
// exporter, which the entrypoint defers. With export off it sets nothing up
// and returns a function that does nothing.
func Start(ctx context.Context, d Defaults) (stop func(), err error) {
	logger := d.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	cfg, err := readConfig(os.Getenv)
	if err != nil {
		return nil, err
	}
	if !cfg.on {
		return func() {}, nil
	}
	res, err := resource.New(ctx,
		resource.WithTelemetrySDK(),
		resource.WithAttributes(d.attributes()...),
		resource.WithFromEnv(),
	)
	if err != nil {
		return nil, fmt.Errorf("OTEL_SERVICE_NAME or OTEL_RESOURCE_ATTRIBUTES: %w", err)
	}
	var options []otlptracehttp.Option
	google := googleAuth(cfg.endpoint, os.Getenv)
	if google {
		options = append(options, otlptracehttp.WithHTTPClient(&http.Client{
			Transport: newGoogleTokenTransport(nil, ""),
			Timeout:   exportTimeout,
		}))
	}
	exporter, err := otlptracehttp.New(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("the OTLP exporter: %w", err)
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(cfg.sampler),
	)
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		logger.Warn("telemetry", zap.Error(err))
	}))
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(cfg.propagator)
	enabled.Store(true)
	logger.Info("exporting traces", zap.String("endpoint", cfg.endpoint), zap.String("protocol", Protocol), zap.Bool("googleAuth", google))
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), flushTimeout)
		defer cancel()
		if err := provider.Shutdown(ctx); err != nil {
			logger.Warn("flush the traces", zap.Error(err))
		}
		enabled.Store(false)
		otel.SetTracerProvider(noop.NewTracerProvider())
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())
	}, nil
}

// Enabled reports whether Start turned export on.
func Enabled() bool {
	return enabled.Load()
}

// Handler makes a server span for each request next serves, but for
// /healthz and /readyz, the child of the trace an incoming traceparent
// names. The span is named by the method until the generated API's request
// log middleware names it by its route template. With export off it is
// next itself.
func Handler(next http.Handler) http.Handler {
	if !Enabled() {
		return next
	}
	return otelhttp.NewHandler(next, "server",
		otelhttp.WithFilter(func(r *http.Request) bool { return !healthPaths[r.URL.Path] }),
	)
}

// Transport makes a client span for each call base makes and sends its
// trace context in traceparent and tracestate: the transport of the SDK
// client of a calls edge. A nil base is http.DefaultTransport. With export
// off it is base itself.
func Transport(base http.RoundTripper) http.RoundTripper {
	if !Enabled() {
		return base
	}
	return otelhttp.NewTransport(base,
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return r.Method }),
	)
}

// Job runs a job's method in a span named after its @job class, whose
// status records the run's error.
func Job(ctx context.Context, name string, run func(context.Context) error) error {
	ctx, span := otel.Tracer(scope).Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attribute.String(JobAttribute, name)),
	)
	defer span.End()
	err := run(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}

// attributes are the resource attributes of d.
func (d Defaults) attributes() []attribute.KeyValue {
	var attrs []attribute.KeyValue
	if d.Service != "" {
		attrs = append(attrs, attribute.String("service.name", d.Service))
	}
	if d.Stack != "" {
		attrs = append(attrs, attribute.String(StackAttribute, d.Stack))
	}
	if d.Kind != "" {
		attrs = append(attrs, attribute.String(KindAttribute, d.Kind))
	}
	return attrs
}

// config is what Start reads from the environment.
type config struct {
	on         bool
	endpoint   string
	sampler    sdktrace.Sampler
	propagator propagation.TextMapPropagator
}

// readConfig reads the variables the package's documentation lists, and
// refuses each value the runtime does not support, naming its variable.
func readConfig(getenv func(string) string) (config, error) {
	var cfg config
	var problems []error
	endpointVar := TracesEndpointVariable
	cfg.endpoint = strings.TrimSpace(getenv(TracesEndpointVariable))
	if cfg.endpoint == "" {
		endpointVar = EndpointVariable
		cfg.endpoint = strings.TrimSpace(getenv(EndpointVariable))
	}
	disabled := strings.EqualFold(strings.TrimSpace(getenv(DisabledVariable)), "true")
	cfg.on = cfg.endpoint != "" && !disabled
	if !cfg.on {
		return cfg, nil
	}
	if err := checkEndpoint(cfg.endpoint); err != nil {
		problems = append(problems, fmt.Errorf("%s is %q: %w", endpointVar, cfg.endpoint, err))
	}
	for _, name := range []string{TracesProtocolVariable, ProtocolVariable} {
		if value := strings.TrimSpace(getenv(name)); value != "" && value != Protocol {
			problems = append(problems, fmt.Errorf("%s is %q; the runtime exports over %s alone", name, value, Protocol))
		}
	}
	var err error
	if cfg.sampler, err = readSampler(getenv); err != nil {
		problems = append(problems, err)
	}
	if cfg.propagator, err = readPropagators(getenv(PropagatorsVariable)); err != nil {
		problems = append(problems, err)
	}
	return cfg, errors.Join(problems...)
}

// checkEndpoint refuses an endpoint that is not an http or https URL with a
// host, which the exporter would otherwise leave for its default.
func checkEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("it is no http or https URL")
	}
	return nil
}

// readSampler reads OTEL_TRACES_SAMPLER and OTEL_TRACES_SAMPLER_ARG.
func readSampler(getenv func(string) string) (sdktrace.Sampler, error) {
	name := strings.TrimSpace(getenv(SamplerVariable))
	ratio := func() (float64, error) {
		arg := strings.TrimSpace(getenv(SamplerArgVariable))
		if arg == "" {
			return 1, nil
		}
		r, err := strconv.ParseFloat(arg, 64)
		if err != nil || r < 0 || r > 1 {
			return 0, fmt.Errorf("%s is %q; it is a number from 0 to 1", SamplerArgVariable, arg)
		}
		return r, nil
	}
	switch name {
	case "", "parentbased_always_on":
		return sdktrace.ParentBased(sdktrace.AlwaysSample()), nil
	case "always_on":
		return sdktrace.AlwaysSample(), nil
	case "always_off":
		return sdktrace.NeverSample(), nil
	case "parentbased_always_off":
		return sdktrace.ParentBased(sdktrace.NeverSample()), nil
	case "traceidratio", "parentbased_traceidratio":
		r, err := ratio()
		if err != nil {
			return nil, err
		}
		if name == "traceidratio" {
			return sdktrace.TraceIDRatioBased(r), nil
		}
		return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(r)), nil
	}
	return nil, fmt.Errorf("%s is %q; the runtime samples with always_on, always_off, traceidratio, parentbased_always_on, parentbased_always_off or parentbased_traceidratio", SamplerVariable, name)
}

// readPropagators reads OTEL_PROPAGATORS: tracecontext and baggage unless
// it is set, none for no propagator.
func readPropagators(value string) (propagation.TextMapPropagator, error) {
	if strings.TrimSpace(value) == "" {
		value = "tracecontext,baggage"
	}
	var props []propagation.TextMapPropagator
	seen := map[string]bool{}
	for _, name := range strings.Split(value, ",") {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		switch name {
		case "tracecontext":
			props = append(props, propagation.TraceContext{})
		case "baggage":
			props = append(props, propagation.Baggage{})
		case "none":
		default:
			return nil, fmt.Errorf("%s names %q; the runtime propagates tracecontext and baggage, or none", PropagatorsVariable, name)
		}
	}
	if seen["none"] {
		props = nil
	}
	return propagation.NewCompositeTextMapPropagator(props...), nil
}
