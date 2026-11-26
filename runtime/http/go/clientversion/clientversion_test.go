package clientversion

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

// parity is runtime/http/testdata/client_version_parity.json.
type parity struct {
	Versions []struct {
		Name, A, B string
		Compare    *int
	} `json:"versions"`
	Settings []struct {
		Name, Setting string
		Minimums      map[string]string
		Refused       bool
	} `json:"settings"`
	Decisions []struct {
		Name, Setting string
		Header        *string
		TooOld        *TooOld `json:"tooOld"`
	} `json:"decisions"`
}

func readParity(t *testing.T) parity {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "client_version_parity.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vectors parity
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors.Versions) == 0 || len(vectors.Settings) == 0 || len(vectors.Decisions) == 0 {
		t.Fatal("a section holds no vector")
	}
	return vectors
}

// TestParityVectors: Compare, Parse and Check answer each vector of
// client_version_parity.json, as the TypeScript and Rust runtimes do.
func TestParityVectors(t *testing.T) {
	vectors := readParity(t)
	for _, vector := range vectors.Versions {
		got, ok := Compare(vector.A, vector.B)
		switch {
		case vector.Compare == nil && ok:
			t.Errorf("%s: Compare(%q, %q) = %d, want no answer", vector.Name, vector.A, vector.B, got)
		case vector.Compare != nil && (!ok || got != *vector.Compare):
			t.Errorf("%s: Compare(%q, %q) = %d, %t, want %d", vector.Name, vector.A, vector.B, got, ok, *vector.Compare)
		}
		// The order is antisymmetric.
		if back, okBack := Compare(vector.B, vector.A); ok != okBack || back != -got {
			t.Errorf("%s: Compare(%q, %q) = %d, %t, the reverse of %d, %t", vector.Name, vector.B, vector.A, back, okBack, got, ok)
		}
	}
	for _, vector := range vectors.Settings {
		minimums, err := Parse(vector.Setting)
		if vector.Refused {
			if err == nil || !strings.HasPrefix(err.Error(), Setting) {
				t.Errorf("%s: Parse(%q) = %v, %v, want a refusal that names the setting", vector.Name, vector.Setting, minimums.Listed(), err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(minimums.Listed(), vector.Minimums) {
			t.Errorf("%s: Parse(%q) = %v, %v, want %v", vector.Name, vector.Setting, minimums.Listed(), err, vector.Minimums)
		}
		if minimums.Empty() != (len(vector.Minimums) == 0) {
			t.Errorf("%s: Empty() = %t", vector.Name, minimums.Empty())
		}
		// What String writes reads back as the same list.
		if again, err := Parse(minimums.String()); err != nil || !reflect.DeepEqual(again.Listed(), minimums.Listed()) {
			t.Errorf("%s: %q reads back as %v, %v", vector.Name, minimums.String(), again.Listed(), err)
		}
	}
	for _, vector := range vectors.Decisions {
		minimums, err := Parse(vector.Setting)
		if err != nil {
			t.Fatalf("%s: %v", vector.Name, err)
		}
		header := ""
		if vector.Header != nil {
			header = *vector.Header
		}
		if got := minimums.Check(header); !reflect.DeepEqual(got, vector.TooOld) {
			t.Errorf("%s: Check(%q) = %+v, want %+v", vector.Name, header, got, vector.TooOld)
		}
	}
}

// TestMiddleware: a client under its minimum is answered 426 with the
// problem client_too_old, the request's id and the three details, and
// never reaches the handler; every other request does, and a list of no
// client adds no step.
func TestMiddleware(t *testing.T) {
	minimums, err := Parse("cli=0.5.0")
	if err != nil {
		t.Fatal(err)
	}
	reached := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusNoContent)
	})
	handler := chimiddleware.RequestID(minimums.Middleware(next))

	request := httptest.NewRequest(http.MethodGet, "/api/carts", nil)
	request.Header.Set(Header, "cli/0.4.0")
	request.Header.Set("X-Request-Id", "req-1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUpgradeRequired || reached != 0 {
		t.Fatalf("status %d, handler reached %d times", recorder.Code, reached)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("Content-Type = %q", got)
	}
	var problem map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"type": "about:blank", "title": "Upgrade Required", "status": float64(426), "code": "client_too_old", "requestId": "req-1",
		"detail":  "cli 0.4.0 is older than 0.5.0, the oldest version of cli this API serves. Upgrade cli.",
		"details": map[string]any{"client": "cli", "version": "0.4.0", "minimum": "0.5.0"},
	}
	if !reflect.DeepEqual(problem, want) {
		t.Errorf("problem = %v, want %v", problem, want)
	}

	for _, header := range []string{"", "cli/0.5.0", "desktop/0.0.1", "cli/next"} {
		request := httptest.NewRequest(http.MethodGet, "/api/carts", nil)
		if header != "" {
			request.Header.Set(Header, header)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNoContent {
			t.Errorf("%q: status %d, want the handler's", header, recorder.Code)
		}
	}
	if reached != 4 {
		t.Errorf("the handler was reached %d times, want 4", reached)
	}

	// No minimum, no step: the handler itself comes back.
	if got := (Minimums{}).Middleware(next); reflect.ValueOf(got).Pointer() != reflect.ValueOf(next).Pointer() {
		t.Error("a list of no client wrapped the handler")
	}
}
