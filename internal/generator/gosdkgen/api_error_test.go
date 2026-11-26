package gosdkgen

import "testing"

// TestAPIErrorCarriesTheFieldErrors runs apiErrorServerTest against a server
// that answers every request with a validation problem
// (runAgainstBodyArgsRoutes): the SDK's APIError holds the problem's field
// errors in ValidationErrors, a nested field's under its name, and none for
// a problem without them.
func TestAPIErrorCarriesTheFieldErrors(t *testing.T) {
	runAgainstBodyArgsRoutes(t, "api_error_test.go", apiErrorServerTest)
}

// apiErrorServerTest runs in the generated API module of body-args-api.
const apiErrorServerTest = `package bodyargsapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "example.com/schemas/sdk/go/body-args-api"
	"example.com/schemas/sdk/go/body-args-api/namespaces"
	types "example.com/schemas/types/go/body-args-api"
)

func TestTheSDKReadsTheFieldErrorsOfAProblem(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		fields     []string
	}{
		{
			name:   "field errors",
			body:   ` + "`" + `{"type":"about:blank","title":"Bad Request","status":400,"detail":"Validation failed","code":"bad_request","errors":{"document":[{"validator":"required","message":"required field"}],"point":{"x":[{"validator":"min","message":"must be at least 0"}]}}}` + "`" + `,
			fields: []string{"document", "point"},
		},
		{
			name: "no field errors",
			body: ` + "`" + `{"type":"about:blank","title":"Bad Request","status":400,"detail":"Request body is required","code":"bad_request"}` + "`" + `,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.TagNamespace.StoreDocument(context.Background(), namespaces.TagStoreDocumentInput{Document: types.GenericJSON("1")})
			var apiErr *sdk.APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
				t.Fatalf("StoreDocument = %v, want a 400 APIError", err)
			}
			if len(tc.fields) == 0 {
				if apiErr.ValidationErrors != nil {
					t.Errorf("ValidationErrors = %v, want nil", apiErr.ValidationErrors)
				}
				return
			}
			for _, field := range tc.fields {
				if _, ok := apiErr.ValidationErrors[field]; !ok {
					t.Errorf("ValidationErrors = %v, want %s", apiErr.ValidationErrors, field)
				}
			}
			if list, ok := apiErr.ValidationErrors["document"].([]types.ValidationError); !ok || len(list) != 1 || list[0].Validator != "required" {
				t.Errorf("document's errors = %#v", apiErr.ValidationErrors["document"])
			}
			if nested, ok := apiErr.ValidationErrors["point"].(types.ValidationErrors); !ok || nested["x"] == nil {
				t.Errorf("point's errors = %#v", apiErr.ValidationErrors["point"])
			}
		})
	}
}
`
