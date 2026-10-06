package sdk

import (
	"context"
	"net/http"

	"example.com/schemas/sdk/go/fixture-nested-arrays-api/runtime"
)

type TokenProvider func(ctx context.Context) (string, error)

type TokenStorage interface {
	GetToken(ctx context.Context) string
	SetToken(ctx context.Context, token string)
	ClearToken(ctx context.Context)
}

type RequestInterceptor func(ctx context.Context, req *http.Request) error

type ResponseInterceptor func(ctx context.Context, response *http.Response, body []byte) ([]byte, error)

type SDKConfig struct {
	BaseURL             string
	Timeout             int
	MaxResponseSize     int64
	Auth                *AuthConfig
	ServiceCredential   *ServiceCredentialConfig
	Encryption          *EncryptionConfig
	RequestInterceptor  RequestInterceptor
	ResponseInterceptor ResponseInterceptor
	Transport           http.RoundTripper
	MaxRateLimitRetries *int
}

// AuthConfig configures authentication for the SDK client.
//
// Token resolution precedence (first non-empty wins):
//  1. Token — the static token, from construction, SetToken or a refresh
//  2. GetToken — dynamic token provider called on every request
//  3. Storage — token retrieved from a TokenStorage implementation
//
// A server that forwards its end user on each call sets GetToken to read
// the token of the request on the call's context (serviceauth.ForwardedToken
// in the Go HTTP runtime) and sets no Token or RefreshToken (D37).
//
// Only one of GetToken or TokenProvider should be set. If both are provided,
// NewHTTPClient returns an error. TokenProvider is deprecated; use GetToken instead.
type AuthConfig struct {
	Token string

	// Deprecated: Use GetToken instead. Setting both GetToken and TokenProvider
	// will cause NewHTTPClient to return a configuration error.
	TokenProvider TokenProvider

	// GetToken is called on every request, with the request's context, to
	// dynamically resolve an auth token when no static Token is set. It takes
	// precedence over Storage.
	GetToken TokenProvider

	// RefreshToken is called when a request receives a 401 Unauthorized response.
	// The returned token replaces the current token for subsequent requests.
	RefreshToken TokenProvider

	Storage    TokenStorage
	HeaderName string
}

// ServiceCredentialConfig configures the calling service's own credential,
// beside the end user's (D37). The client sends "Bearer <token>" in each
// header on every request. A 401 whose problem code is service_unauthorized
// asks Token for a fresh token once and retries, without the end-user
// refresh; any other 401 never asks Token.
type ServiceCredentialConfig struct {
	// Token returns the credential; fresh asks for a new token rather than a
	// cached one. A serviceauth.TokenSource of the Go HTTP runtime is one.
	Token func(ctx context.Context, fresh bool) (string, error)

	// Headers carry the token; empty means Service-Authorization alone.
	Headers []string
}

type PublicEncryptionKey = runtime.PublicEncryptionKey

type EncryptionConfig struct {
	PublicEncryptionKey *PublicEncryptionKey
}

type EncryptedRequestOptions = runtime.EncryptedRequestOptions

type EncryptedPayloadEnvelope = runtime.EncryptedPayloadEnvelope

type UploadFile = runtime.UploadFile

type MultipartBody = runtime.MultipartBody
