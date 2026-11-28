package middleware

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/parable-work/superschematic/runtime/http/go/response"
	"github.com/parable-work/superschematic/runtime/http/go/session"
	"go.uber.org/zap"
)

// RateLimitDecision is a PrincipalRateLimitStore's answer for one request.
type RateLimitDecision struct {
	// Allowed reports whether the request fits in the current window.
	Allowed bool
	// RetryAfter is how long until the window that refused the request
	// ends. It is meaningful only when Allowed is false.
	RetryAfter time.Duration
}

// PrincipalRateLimitStore holds the counters behind
// @rateLimit({ key: 'principal' }) routes (D131).
//
// The generated router takes one from the service, so the counter lives
// wherever the service keeps shared state and the runtime has no database
// dependency. Take must count the request and decide in one atomic step,
// so that replicas sharing the store share one count.
type PrincipalRateLimitStore interface {
	// Take counts one request for bucket and principal in the window that
	// contains now, and reports whether that count is within limit.
	Take(ctx context.Context, bucket, principal string, limit int, window time.Duration) (RateLimitDecision, error)
}

// PrincipalReader reads a request's principal for a principal rate limit:
// "" when the request has none.
type PrincipalReader func(*http.Request) string

// SessionPrincipal is the principal of the core's @rateLimit key
// 'principal': the authenticated principal's ID the auth middleware put on
// the request (session.GetPrincipalID).
func SessionPrincipal(r *http.Request) string {
	return session.GetPrincipalID(r.Context())
}

// PrincipalRateLimit limits a route to requestLimit requests per window
// for each principal, counted in store (D131). principalOf reads the
// principal; the route runs it after the auth middleware and the
// permission check, so the principal is the caller's.
//
// A request with no principal is refused 400 rather than counted under an
// empty key, and a store failure refuses the request 503: the limit exists
// to cap spend, so it fails closed. A refusal is an RFC 9457 problem, with
// a Retry-After header on 429.
func PrincipalRateLimit(
	store PrincipalRateLimitStore,
	bucket string,
	requestLimit int,
	window time.Duration,
	principalOf PrincipalReader,
	getLogger LoggerGetter,
) func(http.Handler) http.Handler {
	return countsRuns(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal := principalOf(r)
			if principal == "" {
				response.RequestError(w, r, http.StatusBadRequest,
					"This operation is limited per principal and the request names none", "rate_limit_principal_missing")
				return
			}

			decision, err := store.Take(r.Context(), bucket, principal, requestLimit, window)
			if err != nil {
				// A caller that hung up is not a store failure; nobody reads
				// the answer.
				if r.Context().Err() != nil {
					return
				}
				loggerFromContext(r.Context(), getLogger).Error("principal rate limit store failed",
					zap.String("bucket", bucket), zap.Error(err))
				response.RequestError(w, r, http.StatusServiceUnavailable,
					"Rate limit check is unavailable; try again shortly", "rate_limit_unavailable")
				return
			}
			if !decision.Allowed {
				w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(decision.RetryAfter)))
				response.RequestError(w, r, http.StatusTooManyRequests,
					fmt.Sprintf("This principal exceeded %d requests per %s", requestLimit, windowName(window)), "too_many_requests")
				return
			}

			next.ServeHTTP(w, r)
		})
	})
}

// retryAfterSeconds rounds up to whole seconds, the unit Retry-After takes,
// and never answers 0, which a client would read as "retry now".
func retryAfterSeconds(d time.Duration) int {
	seconds := int(math.Ceil(d.Seconds()))
	if seconds < 1 {
		return 1
	}
	return seconds
}

// windowName reads a window as prose: "minute" rather than "1m0s".
func windowName(window time.Duration) string {
	switch window {
	case time.Second:
		return "second"
	case time.Minute:
		return "minute"
	case time.Hour:
		return "hour"
	}
	return window.String()
}
