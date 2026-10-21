package gosdkgen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
)

// TestUnionArgumentsGoSDK runs unionRequestsSDKTest in the generated Go SDK
// of union-lists-api, which calls ShapeMutations with the arguments of
// union_requests.json's requests (sdktest.UnionRequestsPath), decoded into
// their Go types, against an httptest server that records what it is sent.
// A request whose arguments the Go types cannot hold (a value no member
// reads) is skipped; the requests say only those whose sdk is "sends" and
// whose status is 400 may be. Every other one is sent as the request's
// body, or refused before sending with the member's errors at the
// argument's paths, as its sdk says.
func TestUnionArgumentsGoSDK(t *testing.T) {
	schema, apiOutput := loadUnionListsAPI(t)
	requests, err := sdktest.LoadUnionRequests()
	if err != nil {
		t.Fatal(err)
	}
	requestsPath, err := sdktest.UnionRequestsPath()
	if err != nil {
		t.Fatal(err)
	}
	outcomesPath := filepath.Join(t.TempDir(), "outcomes.json")
	t.Setenv("UNION_REQUESTS", requestsPath)
	t.Setenv("UNION_OUTCOMES", outcomesPath)
	runInSDK(t, schema, apiOutput, sdktest.UnionListsService, "union_requests_test.go", unionRequestsSDKTest)

	data, err := os.ReadFile(outcomesPath)
	if err != nil {
		t.Fatal(err)
	}
	var outcomes map[string]struct {
		Skipped bool            `json:"skipped"`
		Sent    json.RawMessage `json:"sent"`
		Errors  json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(data, &outcomes); err != nil {
		t.Fatal(err)
	}
	for _, request := range requests {
		outcome, ok := outcomes[request.Name]
		if !ok {
			t.Errorf("%s: no outcome", request.Name)
			continue
		}
		if outcome.Skipped {
			if !request.SDKSends() || request.Status != 400 {
				t.Errorf("%s: the Go types cannot hold its arguments", request.Name)
			}
			continue
		}
		want, err := request.SDKErrors()
		if err != nil {
			t.Fatal(err)
		}
		if request.SDKSends() {
			var got, body any
			if err := json.Unmarshal(outcome.Sent, &got); err != nil || json.Unmarshal(request.Body, &body) != nil {
				t.Errorf("%s: sent %s, want %s", request.Name, outcome.Sent, request.Body)
				continue
			}
			if gotJSON, _ := json.Marshal(got); string(gotJSON) != mustMarshal(t, body) {
				t.Errorf("%s: sent %s, want %s", request.Name, outcome.Sent, request.Body)
			}
			continue
		}
		got, err := sdktest.FlattenFieldErrors(outcome.Errors)
		if err != nil {
			t.Fatal(err)
		}
		if got.String() != want.String() {
			t.Errorf("%s: refused with %s, want %s (sent %s)", request.Name, got, want, outcome.Sent)
		}
	}
}

// mustMarshal is value's JSON, its object members in sorted order.
func mustMarshal(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// unionRequestsSDKTest runs in the generated SDK module. For each request
// of $UNION_REQUESTS it decodes the body's arguments into their Go types,
// calls the operation and records, in $UNION_OUTCOMES by the request's
// name, whether it skipped the request, the body the server was sent and
// the field errors of the SDK's validation error.
const unionRequestsSDKTest = `package sdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	sdk "example.com/schemas/sdk/go/union-lists-api"
	"example.com/schemas/sdk/go/union-lists-api/namespaces"
	types "example.com/schemas/types/go/union-lists-api"
)

type outcome struct {
	Skipped bool            ` + "`json:\"skipped,omitempty\"`" + `
	Sent    json.RawMessage ` + "`json:\"sent,omitempty\"`" + `
	Errors  json.RawMessage ` + "`json:\"errors,omitempty\"`" + `
}

func TestUnionArguments(t *testing.T) {
	data, err := os.ReadFile(os.Getenv("UNION_REQUESTS"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Requests []struct {
			Name      string                     ` + "`json:\"name\"`" + `
			Operation string                     ` + "`json:\"operation\"`" + `
			Body      map[string]json.RawMessage ` + "`json:\"body\"`" + `
			Status    int                        ` + "`json:\"status\"`" + `
			Data      json.RawMessage            ` + "`json:\"data\"`" + `
		} ` + "`json:\"requests\"`" + `
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	outcomes := map[string]outcome{}
	for _, request := range file.Requests {
		var sent json.RawMessage
		answer := request.Data
		if request.Status != 200 {
			answer = json.RawMessage("null")
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sent, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(` + "`" + `{"data":` + "`" + ` + string(answer) + ` + "`" + `,"meta":{"requestId":"req-1"}}` + "`" + `))
		}))
		client, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		var callErr error
		skipped := false
		switch request.Operation {
		case "drawShape":
			var shape types.ShapeWrapper
			if json.Unmarshal(request.Body["shape"], &shape) != nil {
				skipped = true
				break
			}
			_, callErr = client.ShapeNamespace.DrawShape(ctx, namespaces.ShapeDrawShapeInput{Shape: shape.Value})
		case "pay":
			var payment types.PaymentWrapper
			if json.Unmarshal(request.Body["payment"], &payment) != nil {
				skipped = true
				break
			}
			_, callErr = client.ShapeNamespace.Pay(ctx, namespaces.ShapePayInput{Payment: payment.Value})
		case "checkout":
			body, _ := json.Marshal(request.Body)
			var input types.Checkout
			if json.Unmarshal(body, &input) != nil {
				skipped = true
				break
			}
			_, callErr = client.ShapeNamespace.Checkout(ctx, input)
		default:
			t.Fatalf("%s: unknown operation %s", request.Name, request.Operation)
		}
		server.Close()
		result := outcome{Skipped: skipped, Sent: sent}
		var validation *namespaces.ValidationError
		switch {
		case errors.As(callErr, &validation):
			result.Errors, _ = json.Marshal(validation.Errors)
		case callErr != nil && request.Status == 200:
			t.Errorf("%s: %v", request.Name, callErr)
		}
		outcomes[request.Name] = result
	}
	out, err := json.Marshal(outcomes)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("UNION_OUTCOMES"), out, 0o644); err != nil {
		t.Fatal(err)
	}
}
`
