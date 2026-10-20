package sdk

import (
	"fmt"
	"net/http"

	"example.com/schemas/sdk/go/fixture-paged-api/namespaces"
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
	if e.StatusCode > 0 && e.Code != "" {
		return fmt.Sprintf("%s (code: %s, status: %d)", e.Message, e.Code, e.StatusCode)
	}
	if e.StatusCode > 0 {
		return fmt.Sprintf("%s (status: %d)", e.Message, e.StatusCode)
	}
	return e.Message
}

// AuthenticationError represents an HTTP 401 response.
type AuthenticationError struct{ *APIError }

// Unwrap lets errors.As find the *APIError of a 401.
func (e *AuthenticationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return unwrapAPIError(e.APIError)
}

// AuthorizationError represents an HTTP 403 response.
type AuthorizationError struct{ *APIError }

// Unwrap lets errors.As find the *APIError of a 403.
func (e *AuthorizationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return unwrapAPIError(e.APIError)
}

// RateLimitError represents an HTTP 429 response.
type RateLimitError struct {
	*APIError
	RetryAfter *int
}

// Unwrap lets errors.As find the *APIError of a 429.
func (e *RateLimitError) Unwrap() error {
	if e == nil {
		return nil
	}
	return unwrapAPIError(e.APIError)
}

// unwrapAPIError returns a nil interface for a nil *APIError, so errors.As
// does not find a typed nil.
func unwrapAPIError(apiErr *APIError) error {
	if apiErr == nil {
		return nil
	}
	return apiErr
}

// NetworkError represents transport-level request failures. It has no
// Unwrap on purpose: a caller that checks *APIError first must not read a
// transport failure as an API answer with status 0.
type NetworkError struct{ *APIError }

type ValidationError = namespaces.ValidationError

var NewValidationError = namespaces.NewValidationError
