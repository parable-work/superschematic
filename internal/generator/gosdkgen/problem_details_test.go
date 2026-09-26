package gosdkgen

import "testing"

// TestProblemDetailsReachTheSDKError runs problemDetailsSDKTest in the
// generated SDK: a refused request's RFC 9457 problem becomes an error whose
// Message is the problem's detail and whose Error() names its code and
// status.
func TestProblemDetailsReachTheSDKError(t *testing.T) {
	runInNestedArraysSDK(t, "problem_details_test.go", problemDetailsSDKTest)
}

// problemDetailsSDKTest runs in the generated SDK module against an httptest
// server that refuses every request with the given status and body.
const problemDetailsSDKTest = `package sdk_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "example.com/schemas/sdk/go/fixture-nested-arrays-api"
)

func refuse(t *testing.T, status int, body string) error {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	client, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GridNamespace.GridLabels(context.Background(), "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40", nil)
	if err == nil {
		t.Fatal("a refused request returned no error")
	}
	return err
}

func TestProblemDetailsBecomeTheErrorText(t *testing.T) {
	for _, tc := range []struct {
		name, body, message, code, text string
	}{
		{
			name:    "problem with a code",
			body:    "{\"type\":\"about:blank\",\"title\":\"Bad Request\",\"status\":400,\"detail\":\"order 7 has already shipped\",\"code\":\"ORDER_SHIPPED\",\"requestId\":\"req-1\"}",
			message: "order 7 has already shipped",
			code:    "ORDER_SHIPPED",
			text:    "order 7 has already shipped (code: ORDER_SHIPPED, status: 400)",
		},
		{
			name:    "problem without a code",
			body:    "{\"title\":\"Bad Request\",\"status\":400,\"detail\":\"order 7 has already shipped\"}",
			message: "order 7 has already shipped",
			text:    "order 7 has already shipped (status: 400)",
		},
		{
			name:    "detail wins over error",
			body:    "{\"detail\":\"quantity must be positive\",\"error\":\"Bad Request\"}",
			message: "quantity must be positive",
			text:    "quantity must be positive (status: 400)",
		},
		{
			name:    "an error body without detail",
			body:    "{\"error\":\"quantity must be positive\",\"code\":\"BAD_QUANTITY\"}",
			message: "quantity must be positive",
			code:    "BAD_QUANTITY",
			text:    "quantity must be positive (code: BAD_QUANTITY, status: 400)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := refuse(t, http.StatusBadRequest, tc.body)
			var apiErr *sdk.APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("err = %T %v, want *sdk.APIError", err, err)
			}
			if apiErr.Message != tc.message || apiErr.Code != tc.code || apiErr.StatusCode != http.StatusBadRequest {
				t.Errorf("APIError = (%q, %q, %d), want (%q, %q, 400)", apiErr.Message, apiErr.Code, apiErr.StatusCode, tc.message, tc.code)
			}
			if got := err.Error(); got != tc.text {
				t.Errorf("Error() = %q, want %q", got, tc.text)
			}
		})
	}
}

// A typed error keeps the text: a 403 is an AuthorizationError around the
// same APIError.
func TestForbiddenProblemKeepsDetailAndCode(t *testing.T) {
	err := refuse(t, http.StatusForbidden, "{\"status\":403,\"detail\":\"orders are read-only here\",\"code\":\"ORDER_READ_ONLY\"}")
	var forbidden *sdk.AuthorizationError
	if !errors.As(err, &forbidden) {
		t.Fatalf("err = %T %v, want *sdk.AuthorizationError", err, err)
	}
	if want := "orders are read-only here (code: ORDER_READ_ONLY, status: 403)"; err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}
}
`
