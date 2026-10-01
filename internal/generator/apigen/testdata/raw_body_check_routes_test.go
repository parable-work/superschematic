// This file runs inside the generated API module of raw-body-check-api
// (TestRawBodyCheckRoutesRefuseBeforeDecoding copies it there). The module
// registers jsonkeys.DuplicateKeyErrors as the raw-body check of
// Generic.JSON, so saveNote's route checks the body and meta fields of its
// raw JSON body before it decodes the body. The implementation records
// each call, so a test can check a refused request never arrived.
package rawbodycheckapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	api "example.com/schemas/api/raw-body-check-api"
	types "example.com/schemas/types/go/raw-body-check-api"
)

const saveNotePath = "/api/notes"

type notes struct {
	calls int
	last  *types.SaveNoteInput
}

func (n *notes) SaveNote(_ context.Context, input *types.SaveNoteInput) (*string, error) {
	n.calls++
	n.last = input
	title := input.Title
	return &title, nil
}

func serve(t *testing.T) (*httptest.Server, *notes) {
	t.Helper()
	impl := &notes{}
	router := chi.NewRouter()
	if err := api.RegisterRoutes(router, api.Config{
		Logger:          zap.NewNop(),
		Implementations: api.Implementations{Note: impl},
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server, impl
}

// post sends body to saveNote and returns the status and the decoded
// response object.
func post(t *testing.T, server *httptest.Server, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, server.URL+saveNotePath, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("%s: response is not a JSON object: %s", body, raw)
	}
	return resp.StatusCode, decoded
}

// refused sends body, checks the route answered 400 without calling the
// implementation, and returns the response.
func refused(t *testing.T, server *httptest.Server, impl *notes, body string) map[string]any {
	t.Helper()
	before := impl.calls
	status, response := post(t, server, body)
	if status != http.StatusBadRequest {
		t.Fatalf("%s: status = %d, want 400; response %v", body, status, response)
	}
	if impl.calls != before {
		t.Fatalf("%s: a refused request reached the implementation", body)
	}
	return response
}

// errorPaths flattens the errors object of a validation response to
// path -> validator, joining nested object paths with a dot.
func errorPaths(prefix string, errs map[string]any, out map[string]string) {
	for key, value := range errs {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		switch v := value.(type) {
		case map[string]any:
			errorPaths(path, v, out)
		case []any:
			for _, entry := range v {
				e, _ := entry.(map[string]any)
				validator, _ := e["validator"].(string)
				out[path] = validator
			}
		}
	}
}

// refusedAt sends body and checks the 400 response carries exactly one
// duplicateKey error at each of paths.
func refusedAt(t *testing.T, server *httptest.Server, impl *notes, body string, paths ...string) {
	t.Helper()
	response := refused(t, server, impl, body)
	errs, ok := response["errors"].(map[string]any)
	if !ok {
		t.Fatalf("%s: response has no errors object: %v", body, response)
	}
	got := map[string]string{}
	errorPaths("", errs, got)
	want := map[string]string{}
	for _, path := range paths {
		want[path] = "duplicateKey"
	}
	if len(got) != len(want) {
		t.Fatalf("%s: errors = %v, want duplicateKey at %v", body, got, paths)
	}
	for path, validator := range want {
		if got[path] != validator {
			t.Errorf("%s: error at %s = %q, want %q; errors %v", body, path, got[path], validator, got)
		}
	}
}

func TestABodyTheCheckPassesReachesTheImplementation(t *testing.T) {
	server, impl := serve(t)
	// history is a list of Generic.JSON, which the check does not name.
	status, response := post(t, server, `{"title": "t", "body": {"a": 1}, "meta": {"b": 2}, "history": [{"c": 1, "c": 2}]}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; response %v", status, response)
	}
	if impl.calls != 1 || impl.last == nil || impl.last.Title != "t" {
		t.Fatalf("the implementation received %+v in %d calls", impl.last, impl.calls)
	}
}

func TestARepeatedKeyIsRefusedAtItsFieldPath(t *testing.T) {
	server, impl := serve(t)
	refusedAt(t, server, impl, `{"title": "t", "body": {"a": 1, "a": 2}}`, "body.a")
	refusedAt(t, server, impl, `{"title": "t", "body": {"a": 1}, "meta": {"b": 1, "c": 2, "b": 3}}`, "meta.b")
}

func TestEveryCheckedFieldIsReportedTogether(t *testing.T) {
	server, impl := serve(t)
	refusedAt(t, server, impl, `{"title": "t", "body": {"a": 1, "a": 2}, "meta": {"b": 1, "b": 2}}`, "body.a", "meta.b")
}

// TestTheCheckRunsBeforeTheBodyIsDecoded: a title of the wrong JSON type
// fails the decode, and with a clean body that is the answer. With a
// repeated key as well, the check's errors are the answer, so the check ran
// first.
func TestTheCheckRunsBeforeTheBodyIsDecoded(t *testing.T) {
	server, impl := serve(t)
	response := refused(t, server, impl, `{"title": 5, "body": {"a": 1}}`)
	if response["detail"] != "Invalid request body" || response["errors"] != nil {
		t.Fatalf("a body that fails to decode answered %v, want the decode error", response)
	}
	refusedAt(t, server, impl, `{"title": 5, "body": {"a": 1, "a": 2}}`, "body.a")
}

func TestANullBodyIsStillRequired(t *testing.T) {
	server, impl := serve(t)
	response := refused(t, server, impl, `null`)
	if response["detail"] != "input is required" {
		t.Fatalf("null answered %v, want input is required", response)
	}
}
