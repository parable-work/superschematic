package sdk

import (
	"fmt"
	"net/http"

	"example.com/schemas/sdk/go/fixture-nested-arrays-api/namespaces"
)

// APIError represents a non-successful API response.
type APIError struct {
	Message         string
	StatusCode      int
	Body            []byte
	Code            string
	Details         map[string]any
	RequestID       string
	ResponseHeaders http.Header
}

func (e *APIError) Error() string {
	if e == nil {
		return "api error"
	}
	if e.Message == "" {
		return "api error"
	}
	if e.StatusCode > 0 {
		return fmt.Sprintf("%s (status: %d)", e.Message, e.StatusCode)
	}
	return e.Message
}

// AuthenticationError represents an HTTP 401 response.
type AuthenticationError struct{ *APIError }

// AuthorizationError represents an HTTP 403 response.
type AuthorizationError struct{ *APIError }

// RateLimitError represents an HTTP 429 response.
type RateLimitError struct {
	*APIError
	RetryAfter *int
}

// NetworkError represents transport-level request failures.
type NetworkError struct{ *APIError }

type ValidationError = namespaces.ValidationError

var NewValidationError = namespaces.NewValidationError
