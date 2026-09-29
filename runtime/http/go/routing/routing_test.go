package routing

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestPathParamDecodesOnce drives a chi route with a string path parameter
// over a raw connection, so each request target reaches the server exactly
// as written, and checks the value PathParam returns: decoded once whether
// chi matched the route against the decoded path or the raw one, and a 400
// for a path that does not decode (net/http refuses a bad escape before
// routing; PathParam refuses bytes that are not UTF-8).
func TestPathParamDecodesOnce(t *testing.T) {
	router := chi.NewRouter()
	router.Route("/api", func(r chi.Router) {
		r.Get("/items/{id}", func(w http.ResponseWriter, r *http.Request) {
			id, err := PathParam(r, "id")
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, id)
		})
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	for _, tc := range []struct{ segment, want string }{
		// A value with a literal % (or an escape sequence as text), encoded
		// once as encodeURIComponent would.
		{"%25", "%"},
		{"a%2525b", "a%25b"},
		{"100%25", "100%"},
		{"x%2541y", "x%41y"},
		{"%25E9", "%E9"},
		{"a%2Fb", "a/b"},
		{"caf%C3%A9", "caf\u00e9"},
		{"a%2Bb%20c", "a+b c"},
		// Other encodings of a value decode to the same value.
		{"%41", "A"},
		{"caf%c3%a9", "caf\u00e9"},
		{"a%2fb", "a/b"},
	} {
		status, body := get(t, server, "/api/items/"+tc.segment)
		if status != http.StatusOK || body != tc.want {
			t.Errorf("GET /api/items/%s = %d %q, want 200 %q", tc.segment, status, body, tc.want)
		}
	}
	for _, segment := range []string{"%", "100%", "%ZZ", "a%2", "%E9", "%C3%28"} {
		if status, body := get(t, server, "/api/items/"+segment); status != http.StatusBadRequest {
			t.Errorf("GET /api/items/%s = %d %q, want 400", segment, status, body)
		}
	}
}

// get sends a GET for target as written and returns the status and body.
func get(t *testing.T, server *httptest.Server, target string) (int, string) {
	t.Helper()
	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: example.test\r\nConnection: close\r\n\r\n", target); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}
