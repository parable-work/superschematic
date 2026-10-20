package gosdkgen

import "testing"

// TestStatusErrorsUnwrapToAPIError runs errorUnwrapSDKTest in the generated
// SDK. A 401, 403 or 429 answer comes back as a typed wrapper, and errors.As
// must still find its *APIError. A transport failure must not: a caller reads
// a found *APIError as an answer from the server.
func TestStatusErrorsUnwrapToAPIError(t *testing.T) {
	runInNestedArraysSDK(t, "error_unwrap_test.go", errorUnwrapSDKTest)
}

// errorUnwrapSDKTest runs in the generated SDK module against httptest
// servers.
const errorUnwrapSDKTest = `package sdk_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "example.com/schemas/sdk/go/fixture-nested-arrays-api"
)

func gridLabels(t *testing.T, baseURL string) error {
	t.Helper()
	noRetries := 0
	client, err := sdk.New(sdk.SDKConfig{BaseURL: baseURL, MaxRateLimitRetries: &noRetries})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GridNamespace.GridLabels(context.Background(), "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40", nil)
	if err == nil {
		t.Fatal("the request returned no error")
	}
	return err
}

func TestStatusErrorFindsTheAPIError(t *testing.T) {
	for _, tc := range []struct {
		status  int
		wrapper func(error) bool
	}{
		{http.StatusUnauthorized, func(err error) bool { var e *sdk.AuthenticationError; return errors.As(err, &e) }},
		{http.StatusForbidden, func(err error) bool { var e *sdk.AuthorizationError; return errors.As(err, &e) }},
		{http.StatusTooManyRequests, func(err error) bool { var e *sdk.RateLimitError; return errors.As(err, &e) }},
	} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, "{\"detail\":\"refused\",\"code\":\"REFUSED\"}")
			}))
			t.Cleanup(server.Close)

			// A caller often wraps the SDK error; errors.As must see through both layers.
			err := fmt.Errorf("call the API: %w", gridLabels(t, server.URL))
			if !tc.wrapper(err) {
				t.Fatalf("err = %v, want the typed wrapper for %d", err, tc.status)
			}
			var apiErr *sdk.APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("errors.As(*APIError) missed a %d: %v", tc.status, err)
			}
			if apiErr.StatusCode != tc.status || apiErr.Code != "REFUSED" {
				t.Errorf("APIError = %+v, want status %d and code REFUSED", apiErr, tc.status)
			}
		})
	}
}

func TestNetworkErrorIsNotAnAPIError(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	baseURL := server.URL
	server.Close()

	err := gridLabels(t, baseURL)
	var network *sdk.NetworkError
	if !errors.As(err, &network) {
		t.Fatalf("err = %T %v, want *sdk.NetworkError", err, err)
	}
	var apiErr *sdk.APIError
	if errors.As(err, &apiErr) {
		t.Fatalf("errors.As found an *APIError behind a transport failure: %+v", apiErr)
	}
}

func TestNilWrapperUnwrapsToNil(t *testing.T) {
	for _, err := range []interface{ Unwrap() error }{
		(*sdk.AuthenticationError)(nil),
		&sdk.AuthenticationError{},
		(*sdk.AuthorizationError)(nil),
		&sdk.AuthorizationError{},
		(*sdk.RateLimitError)(nil),
		&sdk.RateLimitError{},
	} {
		if got := err.Unwrap(); got != nil {
			t.Errorf("%T.Unwrap() = %#v, want nil", err, got)
		}
	}
}
`
