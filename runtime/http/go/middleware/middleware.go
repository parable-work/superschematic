package middleware

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"
	"github.com/parable-work/superschematic/runtime/http/go/requestctx"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// LoggerSetter stores a request-scoped logger in context.
type LoggerSetter func(context.Context, *zap.Logger) context.Context

// LoggerGetter loads a request-scoped logger from context.
type LoggerGetter func(context.Context) *zap.Logger

// IPAddressSetter stores a client IP address in context.
type IPAddressSetter func(context.Context, string) context.Context

// ContextValueSetter stores a caller-provided value in context.
type ContextValueSetter[T any] func(context.Context, T) context.Context

// ErrorResponder writes an error response with a caller-defined format.
type ErrorResponder func(http.ResponseWriter, *http.Request, int, string)

// Logger creates request-scoped loggers with common HTTP fields.
func Logger(baseLogger *zap.Logger, setLogger LoggerSetter) func(http.Handler) http.Handler {
	if baseLogger == nil {
		baseLogger = zap.NewNop()
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			requestID := chimiddleware.GetReqID(ctx)

			w.Header().Set("X-Request-ID", requestID)

			requestLogger := baseLogger.With(
				zap.String("request_id", requestID),
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
			)

			if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() {
				sc := span.SpanContext()
				traceID := sc.TraceID()
				spanID := sc.SpanID()
				requestLogger = requestLogger.With(
					zap.String("dd.trace_id", strconv.FormatUint(binary.BigEndian.Uint64(traceID[8:]), 10)),
					zap.String("dd.span_id", strconv.FormatUint(binary.BigEndian.Uint64(spanID[:]), 10)),
				)
			}

			if clientIP := requestctx.GetClientIP(r); clientIP != "" {
				requestLogger = requestLogger.With(zap.String("client_ip", clientIP))
			}

			next.ServeHTTP(w, r.WithContext(setLogger(ctx, requestLogger)))
		})
	}
}

// ContextValueWithClientIP injects a caller-provided value plus the resolved client IP.
func ContextValueWithClientIP[T any](
	value T,
	setValue ContextValueSetter[T],
	setIPAddress IPAddressSetter,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := setValue(r.Context(), value)
			ctx = setIPAddress(ctx, requestctx.GetClientIP(r))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func keyByClientIP(r *http.Request) (string, error) {
	if ip := chimiddleware.GetClientIP(r.Context()); ip != "" {
		return ip, nil
	}

	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}

	return ip, nil
}

// RateLimit returns a rate limiting middleware using httprate.
func RateLimit(requestLimit int, windowLength time.Duration, getLogger LoggerGetter) func(http.Handler) http.Handler {
	return httprate.LimitBy(
		requestLimit,
		windowLength,
		keyByClientIP,
		httprate.WithLimitHandler(func(w http.ResponseWriter, r *http.Request) {
			logger := loggerFromContext(r.Context(), getLogger)
			logger.Warn("rate limit exceeded", zap.String("path", r.URL.Path))
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
		}),
	)
}

// BodyLimit limits the maximum request body size for a route.
func BodyLimit(megabytes int, respondError ErrorResponder) func(http.Handler) http.Handler {
	maxBytes := int64(megabytes) * 1024 * 1024

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > maxBytes {
				respondError(w, r, http.StatusRequestEntityTooLarge, fmt.Sprintf("Request body too large (max %dMB)", megabytes))
				return
			}

			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

// Timeout applies a request timeout and prevents partial writes on expiry.
func Timeout(timeout time.Duration, getLogger LoggerGetter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()

			done := make(chan struct{})
			panicChan := make(chan any, 1)

			tw := &timeoutWriter{
				header: make(http.Header),
			}

			go func() {
				defer func() {
					if p := recover(); p != nil {
						panicChan <- p
					}
				}()
				next.ServeHTTP(tw, r.WithContext(ctx))
				close(done)
			}()

			select {
			case p := <-panicChan:
				panic(p)
			case <-done:
				tw.mu.Lock()
				defer tw.mu.Unlock()

				dst := w.Header()
				for k, vv := range tw.header {
					dst[k] = vv
				}
				if !tw.wroteHeader {
					tw.code = http.StatusOK
				}
				w.WriteHeader(tw.code)
				_, _ = w.Write(tw.buf.Bytes())
			case <-ctx.Done():
				tw.mu.Lock()
				tw.timedOut = true
				tw.mu.Unlock()

				logger := loggerFromContext(r.Context(), getLogger)
				logger.Warn(
					"request timeout",
					zap.Duration("timeout", timeout),
					zap.String("path", r.URL.Path),
				)
				http.Error(w, "Gateway Timeout", http.StatusGatewayTimeout)
			}
		})
	}
}

const maxErrorBodyCapture = 512

// HTTPServerError wraps HTTP 5xx status errors so spans emit a descriptive
// exception.type while preserving the underlying message and chain.
type HTTPServerError struct {
	err error
}

func NewHTTPServerError(status int) *HTTPServerError {
	return &HTTPServerError{
		err: fmt.Errorf("HTTP %d", status),
	}
}

func (e *HTTPServerError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *HTTPServerError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// RequestLogMiddleware emits a single, information-dense log line per request.
// It captures identity (request id, trace), outcome (status, error body), and
// performance (duration, bytes) in one line. For 5xx responses it also records
// an OTel span error event so the tracing backend surfaces error details.
//
// Place this middleware after any middleware that enriches the request-scoped
// logger (for example an account or principal resolver) so the line carries
// those fields.
func RequestLogMiddleware(getLogger LoggerGetter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &responseRecorder{ResponseWriter: w}
			var panicked bool

			defer func() {
				if p := recover(); p != nil {
					panicked = true
					rec.status = http.StatusInternalServerError
					defer panic(p)
				}

				ctx := r.Context()
				logger := loggerFromContext(ctx, getLogger)

				status := rec.status
				if status == 0 {
					status = http.StatusOK
				}

				fields := []zap.Field{
					zap.Int("status", status),
					zap.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000.0),
					zap.Int("response_bytes", rec.bytesWritten),
				}

				if panicked {
					fields = append(fields, zap.Bool("panic", true))
				}

				route := chiRoutePattern(r)
				if route != "" {
					fields = append(fields, zap.String("http_route", route))
				}

				span := trace.SpanFromContext(ctx)
				if span.IsRecording() {
					if route != "" {
						span.SetAttributes(attribute.String("http.route", route))
					}
					if rec.errorCode != "" {
						span.SetAttributes(attribute.String("error.code", rec.errorCode))
					}
					if status >= 500 {
						attrs := []attribute.KeyValue{
							attribute.Int("http.response.status_code", status),
						}
						if route != "" {
							attrs = append(attrs, attribute.String("http.route", route))
						}
						if rec.errorCode != "" {
							attrs = append(attrs, attribute.String("error.code", rec.errorCode))
						}
						spanErr := NewHTTPServerError(status)
						span.RecordError(
							spanErr,
							trace.WithAttributes(attrs...),
						)
						span.SetStatus(codes.Error, spanErr.Error())
					}
				}

				if status >= 400 {
					body := rec.errorBody()
					if body != "" {
						fields = append(fields, zap.String("error_body", body))
					}
				}

				logger.Info("api-request", fields...)
			}()

			next.ServeHTTP(rec, r)
		})
	}
}

// ErrorCodeRecorder can be implemented by a response writer to capture an
// application error code (e.g., "WA-AU-001") for OpenTelemetry span attributes.
// The middleware's responseRecorder implements this interface so that error
// handlers can propagate the code without response headers or request context.
type ErrorCodeRecorder interface {
	SetErrorCode(code string)
}

// responseRecorder wraps http.ResponseWriter to capture status code, bytes
// written, and a truncated error body for 4xx/5xx responses.
type responseRecorder struct {
	http.ResponseWriter
	status       int
	bytesWritten int
	errorBuf     bytes.Buffer
	errorCode    string
}

// SetErrorCode stores the application error code for later use by the
// middleware when setting OTel span attributes.
func (cr *responseRecorder) SetErrorCode(code string) {
	cr.errorCode = code
}

func (cr *responseRecorder) WriteHeader(code int) {
	if cr.status != 0 {
		return
	}
	cr.status = code
	cr.ResponseWriter.WriteHeader(code)
}

func (cr *responseRecorder) Write(b []byte) (int, error) {
	if cr.status == 0 {
		cr.status = http.StatusOK
	}
	if cr.status >= 400 && cr.errorBuf.Len() < maxErrorBodyCapture {
		remaining := maxErrorBodyCapture - cr.errorBuf.Len()
		if len(b) > remaining {
			cr.errorBuf.Write(b[:remaining])
		} else {
			cr.errorBuf.Write(b)
		}
	}
	n, err := cr.ResponseWriter.Write(b)
	cr.bytesWritten += n
	return n, err
}

func (cr *responseRecorder) Unwrap() http.ResponseWriter {
	return cr.ResponseWriter
}

func (cr *responseRecorder) Flush() {
	if f, ok := cr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (cr *responseRecorder) errorBody() string {
	s := cr.errorBuf.String()
	if len(s) > maxErrorBodyCapture {
		return s[:maxErrorBodyCapture]
	}
	return s
}

func chiRoutePattern(r *http.Request) string {
	rctx := chi.RouteContext(r.Context())
	if rctx != nil {
		return rctx.RoutePattern()
	}
	return ""
}

func loggerFromContext(ctx context.Context, getLogger LoggerGetter) *zap.Logger {
	if getLogger == nil {
		return zap.NewNop()
	}

	logger := getLogger(ctx)
	if logger == nil {
		return zap.NewNop()
	}

	return logger
}

type timeoutWriter struct {
	mu          sync.Mutex
	header      http.Header
	buf         bytes.Buffer
	code        int
	timedOut    bool
	wroteHeader bool
}

func (tw *timeoutWriter) Header() http.Header {
	return tw.header
}

func (tw *timeoutWriter) WriteHeader(code int) {
	tw.mu.Lock()
	defer tw.mu.Unlock()

	if tw.timedOut || tw.wroteHeader {
		return
	}

	tw.wroteHeader = true
	tw.code = code
}

func (tw *timeoutWriter) Write(b []byte) (int, error) {
	tw.mu.Lock()
	defer tw.mu.Unlock()

	if tw.timedOut {
		return 0, context.DeadlineExceeded
	}

	if !tw.wroteHeader {
		tw.wroteHeader = true
		tw.code = http.StatusOK
	}

	return tw.buf.Write(b)
}
