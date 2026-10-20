package telemetry_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/parable-work/superschematic/runtime/http/go/middleware"
	"github.com/parable-work/superschematic/runtime/http/go/telemetry"
	"github.com/parable-work/superschematic/runtime/http/go/telemetry/otlptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.uber.org/zap"
)

// The variables a test clears, so the developer's shell does not turn
// export on or change it.
var variables = []string{
	telemetry.EndpointVariable, telemetry.TracesEndpointVariable,
	telemetry.ProtocolVariable, telemetry.TracesProtocolVariable,
	telemetry.SamplerVariable, telemetry.SamplerArgVariable,
	telemetry.PropagatorsVariable, telemetry.DisabledVariable,
	"OTEL_SERVICE_NAME", "OTEL_RESOURCE_ATTRIBUTES", "OTEL_EXPORTER_OTLP_HEADERS",
}

func clean(t *testing.T) {
	t.Helper()
	for _, name := range variables {
		t.Setenv(name, "")
	}
}

// start points export at a receiver of the test's own, and starts it with
// the defaults a server's entrypoint gives.
func start(t *testing.T) (*otlptest.Receiver, func()) {
	t.Helper()
	clean(t)
	receiver := otlptest.Start(t)
	t.Setenv(telemetry.EndpointVariable, receiver.URL)
	stop, err := telemetry.Start(context.Background(), telemetry.Defaults{Service: "shop-orders", Stack: "shop-stack", Kind: "server", Logger: zap.NewNop()})
	require.NoError(t, err)
	t.Cleanup(stop)
	return receiver, stop
}

// router is a generated-style API router: the request log middleware every
// generated API registers, and a route with a parameter.
func router(handle http.HandlerFunc) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestLogMiddleware(func(context.Context) *zap.Logger { return zap.NewNop() }))
	r.Get("/api/orders/{id}", handle)
	return r
}

func TestNoEndpointStartsNothing(t *testing.T) {
	clean(t)
	providerBefore := otel.GetTracerProvider()
	stop, err := telemetry.Start(context.Background(), telemetry.Defaults{Service: "shop-orders"})
	require.NoError(t, err)
	defer stop()

	assert.False(t, telemetry.Enabled())
	assert.Same(t, providerBefore, otel.GetTracerProvider(), "no tracer provider is installed")
	mux := http.NewServeMux()
	assert.Same(t, mux, telemetry.Handler(mux), "Handler wraps nothing")
	assert.Nil(t, telemetry.Transport(nil), "Transport wraps nothing")

	// A call carries no trace context.
	var traceparent string
	downstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		traceparent = r.Header.Get("traceparent")
	}))
	defer downstream.Close()
	resp, err := (&http.Client{Transport: telemetry.Transport(nil)}).Get(downstream.URL)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Empty(t, traceparent)
}

func TestDisabledStartsNothing(t *testing.T) {
	clean(t)
	receiver := otlptest.Start(t)
	t.Setenv(telemetry.EndpointVariable, receiver.URL)
	t.Setenv(telemetry.DisabledVariable, "TRUE")
	stop, err := telemetry.Start(context.Background(), telemetry.Defaults{Service: "shop-orders"})
	require.NoError(t, err)
	stop()
	assert.False(t, telemetry.Enabled())
	assert.Zero(t, receiver.Requests())
}

func TestARequestIsAServerSpanNamedByItsRoute(t *testing.T) {
	receiver, stop := start(t)
	handler := telemetry.Handler(router(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	const parentTrace, parentSpan = "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"
	req := httptest.NewRequest(http.MethodGet, "/api/orders/42", nil)
	req.Header.Set("traceparent", "00-"+parentTrace+"-"+parentSpan+"-01")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	// The probes make no span.
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	stop()

	spans := receiver.Spans()
	require.Len(t, spans, 1, "%v", spans)
	span := spans[0]
	assert.Equal(t, "GET /api/orders/{id}", span.Name)
	assert.Equal(t, otlptest.KindServer, span.Kind)
	assert.Equal(t, "/api/orders/{id}", span.Attributes["http.route"])
	assert.Equal(t, "204", span.Attributes["http.response.status_code"])
	assert.Equal(t, parentTrace, span.TraceID, "the incoming traceparent is the parent")
	assert.Equal(t, parentSpan, span.ParentSpanID)
	assert.Equal(t, "shop-orders", span.Resource["service.name"])
	assert.Equal(t, "shop-stack", span.Resource["service.namespace"])
	assert.Equal(t, "server", span.Resource["superschematic.deployable.kind"])
	assert.Empty(t, receiver.Failures())
}

func TestTheVariablesOverrideTheDefaults(t *testing.T) {
	clean(t)
	receiver := otlptest.Start(t)
	t.Setenv(telemetry.TracesEndpointVariable, receiver.URL+"/v1/traces")
	t.Setenv("OTEL_SERVICE_NAME", "orders")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "service.namespace=shop,deployment.environment.name=Dev")
	stop, err := telemetry.Start(context.Background(), telemetry.Defaults{Service: "shop-orders", Stack: "shop-stack", Kind: "job"})
	require.NoError(t, err)
	require.NoError(t, telemetry.Job(context.Background(), "ShipOrders", func(context.Context) error { return nil }))
	stop()

	spans := receiver.Spans()
	require.Len(t, spans, 1)
	assert.Equal(t, "orders", spans[0].Resource["service.name"])
	assert.Equal(t, "shop", spans[0].Resource["service.namespace"])
	assert.Equal(t, "Dev", spans[0].Resource["deployment.environment.name"])
	assert.Equal(t, "job", spans[0].Resource["superschematic.deployable.kind"])
}

func TestACallCarriesTheTraceInAClientSpan(t *testing.T) {
	receiver, stop := start(t)
	var traceparent string
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceparent = r.Header.Get("traceparent")
		w.WriteHeader(http.StatusOK)
	}))
	defer downstream.Close()
	client := &http.Client{Transport: telemetry.Transport(nil)}
	handler := telemetry.Handler(router(func(w http.ResponseWriter, r *http.Request) {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, downstream.URL+"/api/stock", nil)
		if err == nil {
			var resp *http.Response
			if resp, err = client.Do(req); err == nil {
				_ = resp.Body.Close()
			}
		}
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/orders/7", nil))
	stop()

	spans := receiver.Spans()
	require.Len(t, spans, 2, "%v", spans)
	var server, call otlptest.Span
	for _, s := range spans {
		switch s.Kind {
		case otlptest.KindServer:
			server = s
		case otlptest.KindClient:
			call = s
		}
	}
	assert.Equal(t, "GET", call.Name)
	assert.Equal(t, server.TraceID, call.TraceID)
	assert.Equal(t, server.SpanID, call.ParentSpanID, "the call is the request's child")
	parts := strings.Split(traceparent, "-")
	require.Len(t, parts, 4, "traceparent %q", traceparent)
	assert.Equal(t, call.TraceID, parts[1])
	assert.Equal(t, call.SpanID, parts[2], "the callee's parent is the client span")
}

func TestAJobRunIsASpanThatRecordsItsFailure(t *testing.T) {
	receiver, stop := start(t)
	failure := errors.New("the carrier is down")
	err := telemetry.Job(context.Background(), "ShipOrders", func(context.Context) error { return failure })
	require.ErrorIs(t, err, failure)
	stop()

	spans := receiver.Spans()
	require.Len(t, spans, 1)
	assert.Equal(t, "ShipOrders", spans[0].Name)
	assert.Equal(t, otlptest.KindInternal, spans[0].Kind)
	assert.Equal(t, "ShipOrders", spans[0].Attributes["superschematic.job"])
	assert.Equal(t, otlptest.StatusError, spans[0].Status)
}

func TestStopFlushesBeforeTheBatchIsDue(t *testing.T) {
	clean(t)
	receiver := otlptest.Start(t)
	t.Setenv(telemetry.EndpointVariable, receiver.URL)
	t.Setenv("OTEL_BSP_SCHEDULE_DELAY", "60000")
	stop, err := telemetry.Start(context.Background(), telemetry.Defaults{Service: "shop-orders-ship-orders", Kind: "job"})
	require.NoError(t, err)
	require.NoError(t, telemetry.Job(context.Background(), "ShipOrders", func(context.Context) error { return nil }))
	started := time.Now()
	stop()
	assert.Less(t, time.Since(started), 5*time.Second)
	require.Len(t, receiver.Spans(), 1, "a job's span reaches the collector before the process exits")
}

func TestUnsupportedValuesAreRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		variable, value, want string
	}{
		"grpc":            {telemetry.ProtocolVariable, "grpc", "OTEL_EXPORTER_OTLP_PROTOCOL is \"grpc\""},
		"json":            {telemetry.TracesProtocolVariable, "http/json", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL is \"http/json\""},
		"b3":              {telemetry.PropagatorsVariable, "tracecontext,b3", "OTEL_PROPAGATORS names \"b3\""},
		"sampler":         {telemetry.SamplerVariable, "jaeger_remote", "OTEL_TRACES_SAMPLER is \"jaeger_remote\""},
		"ratio":           {telemetry.SamplerArgVariable, "2", "OTEL_TRACES_SAMPLER_ARG is \"2\""},
		"endpoint":        {telemetry.EndpointVariable, "localhost:4318", "OTEL_EXPORTER_OTLP_ENDPOINT is \"localhost:4318\""},
		"resource syntax": {"OTEL_RESOURCE_ATTRIBUTES", "stack", "OTEL_RESOURCE_ATTRIBUTES"},
	} {
		t.Run(name, func(t *testing.T) {
			clean(t)
			t.Setenv(telemetry.EndpointVariable, "http://localhost:4318")
			if tc.variable == telemetry.SamplerArgVariable {
				t.Setenv(telemetry.SamplerVariable, "traceidratio")
			}
			t.Setenv(tc.variable, tc.value)
			_, err := telemetry.Start(context.Background(), telemetry.Defaults{Service: "shop-orders"})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.False(t, telemetry.Enabled())
		})
	}
}

func TestSamplerAndPropagatorsAreRead(t *testing.T) {
	receiver, stop := start(t)
	stop()
	clean(t)
	t.Setenv(telemetry.EndpointVariable, receiver.URL)
	t.Setenv(telemetry.SamplerVariable, "always_off")
	t.Setenv(telemetry.PropagatorsVariable, "none")
	stop, err := telemetry.Start(context.Background(), telemetry.Defaults{Service: "shop-orders"})
	require.NoError(t, err)
	_, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider)
	require.True(t, ok)
	require.NoError(t, telemetry.Job(context.Background(), "ShipOrders", func(context.Context) error { return nil }))
	assert.Empty(t, otel.GetTextMapPropagator().Fields(), "none propagates nothing")
	stop()
	assert.Empty(t, receiver.Spans(), "always_off samples nothing")
}
