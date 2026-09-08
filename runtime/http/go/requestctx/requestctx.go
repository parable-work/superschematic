package requestctx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"
)

type (
	loggerKey    struct{}
	ipAddressKey struct{}
)

// ContextWithLogger returns a new context with the logger set.
func ContextWithLogger(ctx context.Context, logger *zap.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, logger)
}

// LoggerFromContext extracts the logger from context.
func LoggerFromContext(ctx context.Context) *zap.Logger {
	if logger, ok := ctx.Value(loggerKey{}).(*zap.Logger); ok {
		return logger
	}
	return zap.NewNop()
}

// ContextWithIPAddress returns a new context with the client IP address set.
func ContextWithIPAddress(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, ipAddressKey{}, ip)
}

// GetIPAddress extracts the client IP address from context.
func GetIPAddress(ctx context.Context) string {
	if ip, ok := ctx.Value(ipAddressKey{}).(string); ok {
		return ip
	}
	return "unknown"
}

// GetClientIP extracts the client IP address from the request.
func GetClientIP(r *http.Request) string {
	if ip := chimiddleware.GetClientIP(r.Context()); ip != "" {
		return ip
	}

	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		ips := strings.Split(xff, ",")
		if len(ips) > 0 {
			return strings.TrimSpace(ips[0])
		}
	}

	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}

	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return host
}

// CheckContext returns a descriptive wrapped error when a request context is invalid.
func CheckContext(ctx context.Context) error {
	err := ctx.Err()
	if err == nil {
		return nil
	}

	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("request cancelled: %w", err)
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("request timeout: %w", err)
	}

	return fmt.Errorf("context error: %w", err)
}
