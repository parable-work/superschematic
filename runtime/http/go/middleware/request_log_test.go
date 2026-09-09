package middleware_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/parable-work/superschematic/runtime/http/go/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func newTestLogger() (*zap.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(zap.InfoLevel)
	return zap.New(core), logs
}

func testLoggerGetter(logger *zap.Logger) middleware.LoggerGetter {
	return func(_ context.Context) *zap.Logger { return logger }
}

func TestRequestLogMiddleware(t *testing.T) {
	t.Run("logs 200 with correct fields", func(t *testing.T) {
		logger, logs := newTestLogger()
		handler := middleware.RequestLogMiddleware(testLoggerGetter(logger))(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("hello"))
			}),
		)

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		require.Equal(t, 1, logs.Len())
		entry := logs.All()[0]
		assert.Equal(t, "api-request", entry.Message)
		assert.Equal(t, int64(200), entry.ContextMap()["status"])
		assert.Equal(t, int64(5), entry.ContextMap()["response_bytes"])
		assert.Contains(t, entry.ContextMap(), "duration_ms")
	})

	t.Run("defaults to 200 when handler writes without WriteHeader", func(t *testing.T) {
		logger, logs := newTestLogger()
		handler := middleware.RequestLogMiddleware(testLoggerGetter(logger))(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("implicit 200"))
			}),
		)

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		require.Equal(t, 1, logs.Len())
		assert.Equal(t, int64(200), logs.All()[0].ContextMap()["status"])
	})

	t.Run("4xx logs error_body and does not record span error", func(t *testing.T) {
		exporter := tracetest.NewInMemoryExporter()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
		defer func() { _ = tp.Shutdown(context.Background()) }()

		logger, logs := newTestLogger()
		handler := middleware.RequestLogMiddleware(testLoggerGetter(logger))(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"bad input"}`))
			}),
		)

		ctx, span := tp.Tracer("test").Start(context.Background(), "test-span")
		req := httptest.NewRequest(http.MethodPost, "/test", nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		span.End()

		require.Equal(t, 1, logs.Len())
		entry := logs.All()[0]
		assert.Equal(t, int64(400), entry.ContextMap()["status"])
		assert.Equal(t, `{"error":"bad input"}`, entry.ContextMap()["error_body"])

		spans := exporter.GetSpans()
		for _, s := range spans {
			assert.Empty(t, s.Events, "4xx should not record span error events")
			assert.NotEqual(t, otelcodes.Error, s.Status.Code, "4xx should not set span status to Error")
		}
	})

	t.Run("5xx logs error_body and records span error", func(t *testing.T) {
		exporter := tracetest.NewInMemoryExporter()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
		defer func() { _ = tp.Shutdown(context.Background()) }()

		logger, logs := newTestLogger()
		handler := middleware.RequestLogMiddleware(testLoggerGetter(logger))(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("internal error"))
			}),
		)

		ctx, span := tp.Tracer("test").Start(context.Background(), "test-span")
		req := httptest.NewRequest(http.MethodGet, "/test", nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		span.End()

		require.Equal(t, 1, logs.Len())
		entry := logs.All()[0]
		assert.Equal(t, int64(500), entry.ContextMap()["status"])
		assert.Equal(t, "internal error", entry.ContextMap()["error_body"])

		spans := exporter.GetSpans()
		var foundError bool
		var exceptionType string
		var exceptionMessage string
		for _, s := range spans {
			for _, ev := range s.Events {
				if ev.Name == "exception" {
					foundError = true
					var statusAttr int64
					for _, attr := range ev.Attributes {
						if attr.Key == attribute.Key("http.response.status_code") {
							statusAttr = attr.Value.AsInt64()
						}
						if attr.Key == attribute.Key("exception.type") {
							exceptionType = attr.Value.AsString()
						}
						if attr.Key == attribute.Key("exception.message") {
							exceptionMessage = attr.Value.AsString()
						}
					}
					assert.Equal(t, int64(500), statusAttr)
				}
			}
		}
		assert.True(t, foundError, "5xx should record a span error event")
		assert.Contains(t, exceptionType, "HTTPServerError")
		assert.Equal(t, "HTTP 500", exceptionMessage)

		var spanStatus otelcodes.Code
		for _, s := range spans {
			spanStatus = s.Status.Code
		}
		assert.Equal(t, otelcodes.Error, spanStatus, "5xx should set span status to Error")
	})

	t.Run("span error does not include response body", func(t *testing.T) {
		exporter := tracetest.NewInMemoryExporter()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
		defer func() { _ = tp.Shutdown(context.Background()) }()

		logger, _ := newTestLogger()
		handler := middleware.RequestLogMiddleware(testLoggerGetter(logger))(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("secret db error: password=hunter2"))
			}),
		)

		ctx, span := tp.Tracer("test").Start(context.Background(), "test-span")
		req := httptest.NewRequest(http.MethodGet, "/test", nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		span.End()

		spans := exporter.GetSpans()
		for _, s := range spans {
			for _, ev := range s.Events {
				if ev.Name == "exception" {
					for _, attr := range ev.Attributes {
						if attr.Key == "exception.message" {
							assert.NotContains(t, attr.Value.AsString(), "hunter2",
								"span error should not leak response body")
							assert.Equal(t, "HTTP 500", attr.Value.AsString())
						}
					}
				}
			}
		}
	})

	t.Run("error body truncated at 512 bytes", func(t *testing.T) {
		logger, logs := newTestLogger()
		longBody := strings.Repeat("x", 1000)
		handler := middleware.RequestLogMiddleware(testLoggerGetter(logger))(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(longBody))
			}),
		)

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		require.Equal(t, 1, logs.Len())
		body := logs.All()[0].ContextMap()["error_body"].(string)
		assert.Len(t, body, 512)
	})

	t.Run("no error_body field for 2xx responses", func(t *testing.T) {
		logger, logs := newTestLogger()
		handler := middleware.RequestLogMiddleware(testLoggerGetter(logger))(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("success"))
			}),
		)

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		require.Equal(t, 1, logs.Len())
		_, hasErrorBody := logs.All()[0].ContextMap()["error_body"]
		assert.False(t, hasErrorBody)
	})

	t.Run("sets http.route span attribute from chi route context", func(t *testing.T) {
		exporter := tracetest.NewInMemoryExporter()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
		defer func() { _ = tp.Shutdown(context.Background()) }()

		logger, logs := newTestLogger()

		r := chi.NewRouter()
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				ctx, span := tp.Tracer("test").Start(req.Context(), "test-span")
				defer span.End()
				next.ServeHTTP(w, req.WithContext(ctx))
			})
		})
		r.Use(middleware.RequestLogMiddleware(testLoggerGetter(logger)))
		r.Get("/users/{id}", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		req := httptest.NewRequest(http.MethodGet, "/users/123", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		require.Equal(t, 1, logs.Len())
		assert.Equal(t, "/users/{id}", logs.All()[0].ContextMap()["http_route"])

		spans := exporter.GetSpans()
		require.NotEmpty(t, spans)
		var routeAttr string
		for _, s := range spans {
			for _, attr := range s.Attributes {
				if attr.Key == attribute.Key("http.route") {
					routeAttr = attr.Value.AsString()
				}
			}
		}
		assert.Equal(t, "/users/{id}", routeAttr)
	})

	t.Run("panic logged as 500 with panic field then re-panics", func(t *testing.T) {
		logger, logs := newTestLogger()
		handler := middleware.RequestLogMiddleware(testLoggerGetter(logger))(
			http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
				panic("something broke")
			}),
		)

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()

		assert.Panics(t, func() { handler.ServeHTTP(rec, req) })

		require.Equal(t, 1, logs.Len())
		entry := logs.All()[0]
		assert.Equal(t, int64(500), entry.ContextMap()["status"])
		assert.Equal(t, true, entry.ContextMap()["panic"])
	})

	t.Run("WriteHeader double call uses first status", func(t *testing.T) {
		logger, logs := newTestLogger()
		handler := middleware.RequestLogMiddleware(testLoggerGetter(logger))(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				w.WriteHeader(http.StatusInternalServerError)
			}),
		)

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		require.Equal(t, 1, logs.Len())
		assert.Equal(t, int64(404), logs.All()[0].ContextMap()["status"],
			"should record the first WriteHeader status, not the second")
	})

	t.Run("nil logger getter does not panic", func(t *testing.T) {
		handler := middleware.RequestLogMiddleware(nil)(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}),
		)

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()
		assert.NotPanics(t, func() { handler.ServeHTTP(rec, req) })
	})

	t.Run("error.code span attribute set for 5xx with error code", func(t *testing.T) {
		exporter := tracetest.NewInMemoryExporter()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
		defer func() { _ = tp.Shutdown(context.Background()) }()

		logger, _ := newTestLogger()
		handler := middleware.RequestLogMiddleware(testLoggerGetter(logger))(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if ec, ok := w.(middleware.ErrorCodeRecorder); ok {
					ec.SetErrorCode("WA-AU-001")
				}
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("internal error"))
			}),
		)

		ctx, span := tp.Tracer("test").Start(context.Background(), "test-span")
		req := httptest.NewRequest(http.MethodGet, "/test", nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		span.End()

		spans := exporter.GetSpans()
		require.NotEmpty(t, spans)

		// Check span-level attribute
		var spanErrorCode string
		for _, s := range spans {
			for _, attr := range s.Attributes {
				if attr.Key == attribute.Key("error.code") {
					spanErrorCode = attr.Value.AsString()
				}
			}
		}
		assert.Equal(t, "WA-AU-001", spanErrorCode, "span should have error.code attribute")

		// Check error event attribute
		var eventErrorCode string
		for _, s := range spans {
			for _, ev := range s.Events {
				if ev.Name == "exception" {
					for _, attr := range ev.Attributes {
						if attr.Key == attribute.Key("error.code") {
							eventErrorCode = attr.Value.AsString()
						}
					}
				}
			}
		}
		assert.Equal(t, "WA-AU-001", eventErrorCode, "error event should have error.code attribute")
	})

	t.Run("error.code span attribute set for 4xx with error code", func(t *testing.T) {
		exporter := tracetest.NewInMemoryExporter()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
		defer func() { _ = tp.Shutdown(context.Background()) }()

		logger, _ := newTestLogger()
		handler := middleware.RequestLogMiddleware(testLoggerGetter(logger))(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if ec, ok := w.(middleware.ErrorCodeRecorder); ok {
					ec.SetErrorCode("WA-RS-002")
				}
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte("not found"))
			}),
		)

		ctx, span := tp.Tracer("test").Start(context.Background(), "test-span")
		req := httptest.NewRequest(http.MethodGet, "/test", nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		span.End()

		spans := exporter.GetSpans()
		require.NotEmpty(t, spans)

		// 4xx should have error.code as span attribute but no error event
		var spanErrorCode string
		for _, s := range spans {
			for _, attr := range s.Attributes {
				if attr.Key == attribute.Key("error.code") {
					spanErrorCode = attr.Value.AsString()
				}
			}
		}
		assert.Equal(t, "WA-RS-002", spanErrorCode, "4xx span should have error.code attribute")

		// 4xx should NOT have error events
		for _, s := range spans {
			assert.Empty(t, s.Events, "4xx should not record span error events")
		}
	})

	t.Run("no error.code span attribute when error code is empty", func(t *testing.T) {
		exporter := tracetest.NewInMemoryExporter()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
		defer func() { _ = tp.Shutdown(context.Background()) }()

		logger, _ := newTestLogger()
		handler := middleware.RequestLogMiddleware(testLoggerGetter(logger))(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("internal error"))
			}),
		)

		ctx, span := tp.Tracer("test").Start(context.Background(), "test-span")
		req := httptest.NewRequest(http.MethodGet, "/test", nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		span.End()

		spans := exporter.GetSpans()
		require.NotEmpty(t, spans)

		for _, s := range spans {
			for _, attr := range s.Attributes {
				assert.NotEqual(t, attribute.Key("error.code"), attr.Key,
					"span should not have error.code attribute when no error code is set")
			}
			for _, ev := range s.Events {
				for _, attr := range ev.Attributes {
					assert.NotEqual(t, attribute.Key("error.code"), attr.Key,
						"error event should not have error.code attribute when no error code is set")
				}
			}
		}
	})

	t.Run("error.code on 2xx is set as span attribute", func(t *testing.T) {
		// Edge case: if someone sets an error code on a successful response,
		// the span attribute should still appear (the middleware does not filter by status).
		exporter := tracetest.NewInMemoryExporter()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
		defer func() { _ = tp.Shutdown(context.Background()) }()

		logger, _ := newTestLogger()
		handler := middleware.RequestLogMiddleware(testLoggerGetter(logger))(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if ec, ok := w.(middleware.ErrorCodeRecorder); ok {
					ec.SetErrorCode("WA-XX-001")
				}
				w.WriteHeader(http.StatusOK)
			}),
		)

		ctx, span := tp.Tracer("test").Start(context.Background(), "test-span")
		req := httptest.NewRequest(http.MethodGet, "/test", nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		span.End()

		spans := exporter.GetSpans()
		require.NotEmpty(t, spans)

		var found bool
		for _, s := range spans {
			for _, attr := range s.Attributes {
				if attr.Key == attribute.Key("error.code") {
					found = true
					assert.Equal(t, "WA-XX-001", attr.Value.AsString())
				}
			}
		}
		assert.True(t, found, "error.code should be set even on 2xx if SetErrorCode was called")
	})
}

func TestHTTPServerError_PreservesErrorChain(t *testing.T) {
	spanErr := middleware.NewHTTPServerError(http.StatusInternalServerError)
	require.NotNil(t, spanErr)
	assert.Equal(t, "HTTP 500", spanErr.Error())

	unwrapped := errors.Unwrap(spanErr)
	require.NotNil(t, unwrapped)
	assert.Equal(t, "HTTP 500", unwrapped.Error())

	var typed *middleware.HTTPServerError
	require.ErrorAs(t, spanErr, &typed)
}

type flushTracker struct {
	http.ResponseWriter
	flushed bool
}

func (f *flushTracker) Flush() { f.flushed = true }

func TestRequestLogMiddleware_FlushForwarding(t *testing.T) {
	logger, _ := newTestLogger()
	inner := &flushTracker{ResponseWriter: httptest.NewRecorder()}

	handler := middleware.RequestLogMiddleware(testLoggerGetter(logger))(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/stream", nil)
	handler.ServeHTTP(inner, req)
	assert.True(t, inner.flushed, "Flush should be forwarded to underlying writer")
}
