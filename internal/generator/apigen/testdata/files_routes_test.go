// This file runs inside the generated API module of fixture-files-api
// (TestFileTransferRoutes copies it there). The implementation keeps the
// statements each account imported in memory, as the TypeScript test's
// does (internal/generator/tsrestgen/testdata/files_implementation.ts), and
// records each call, so a test can check a refused request never arrived.
package fixturefilesapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"

	"github.com/parable-work/superschematic/runtime/http/go/clientversion"

	api "example.com/schemas/api/fixture-files-api"
	types "example.com/schemas/types/go/fixture-files-api"
)

const account = "1VgEh72lXvTXkH"

type statements struct {
	calls    int
	lines    map[types.IdentityUUID][]string
	lastName map[types.IdentityUUID]string
	last     map[types.IdentityUUID][]byte
}

func (s *statements) ImportStatement(_ context.Context, input *types.ImportStatementInput, files map[string]*api.FileUploadData) (*types.ImportResult, error) {
	s.calls++
	statement := files["statement"]
	data, err := statement.ReadAllBytes()
	if err != nil {
		return nil, err
	}
	var lines []string
	for i, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if i > 0 && line != "" {
			lines = append(lines, line)
		}
	}
	key := input.AccountId
	s.lines[key] = append(s.lines[key], lines...)
	s.lastName[key], s.last[key] = statement.Filename, data
	result := &types.ImportResult{AccountId: input.AccountId, Filename: statement.Filename, Lines: float64(len(lines))}
	if note, ok := input.Note.Get(); ok {
		result.Note = note
	}
	if receipt := files["receipt"]; receipt != nil {
		result.Receipt = fmt.Sprintf("%s (%d bytes)", receipt.Filename, receipt.Size)
	}
	return result, nil
}

func (s *statements) ExportStatement(_ context.Context, accountID types.IdentityUUID) (*api.FileDownload, error) {
	lines, ok := s.lines[accountID]
	if !ok {
		return nil, api.NotFoundError("Statement", nil)
	}
	return &api.FileDownload{Body: strings.NewReader("date,amount\n" + strings.Join(lines, "\n") + "\n")}, nil
}

func (s *statements) OriginalStatement(_ context.Context, accountID types.IdentityUUID) (*api.FileDownload, error) {
	data, ok := s.last[accountID]
	if !ok {
		return nil, api.NotFoundError("Statement", nil)
	}
	return &api.FileDownload{Body: bytes.NewReader(data), Filename: s.lastName[accountID]}, nil
}

// previewedAt is when every preview says its statement last changed.
var previewedAt = time.Date(2026, time.March, 1, 0, 0, 0, 900_000_000, time.UTC)

// PreviewStatement answers the last statement as a page shows it (D141):
// an OFX statement as a page of its own media type and name, any other as
// the schema's text, each with an entity tag of its length and a time.
func (s *statements) PreviewStatement(_ context.Context, accountID types.IdentityUUID) (*api.FileDownload, error) {
	data, ok := s.last[accountID]
	if !ok {
		return nil, api.NotFoundError("Statement", nil)
	}
	file := &api.FileDownload{Body: bytes.NewReader(data), ETag: fmt.Sprintf("len-%d", len(data)), LastModified: previewedAt}
	if strings.HasSuffix(strings.ToLower(s.lastName[accountID]), ".ofx") {
		file.Body = strings.NewReader("<pre>" + string(data) + "</pre>")
		file.ContentType, file.Filename = "text/html; charset=utf-8", "statement.html"
	}
	if strings.HasSuffix(strings.ToLower(s.lastName[accountID]), ".qfx") {
		// A media type the schema does not list: the route answers 500.
		file.ContentType = "application/x-qfx"
	}
	return file, nil
}

func serve(t *testing.T) (*httptest.Server, *statements) {
	t.Helper()
	return serveWith(t, clientversion.Minimums{})
}

// serveWith serves the API with the oldest version of each client it
// serves (D143).
func serveWith(t *testing.T, minimums clientversion.Minimums) (*httptest.Server, *statements) {
	t.Helper()
	impl := &statements{lines: map[types.IdentityUUID][]string{}, lastName: map[types.IdentityUUID]string{}, last: map[types.IdentityUUID][]byte{}}
	router := chi.NewRouter()
	router.Use(chimiddleware.RequestID)
	if err := api.RegisterRoutes(router, api.Config{
		Logger:            zap.NewNop(),
		MinClientVersions: minimums,
		Implementations:   api.Implementations{Statements: impl},
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server, impl
}

// part is one file part: its name, filename, Content-Type and bytes.
type part struct {
	name, filename, contentType, body string
}

// multipartBody writes data, when not empty, and each file part.
func multipartBody(t *testing.T, data string, parts ...part) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if data != "" {
		if err := writer.WriteField("data", data); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range parts {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, p.name, p.filename))
		if p.contentType != "" {
			header.Set("Content-Type", p.contentType)
		}
		w, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, p.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, writer.FormDataContentType()
}

func send(t *testing.T, server *httptest.Server, method, path string, body io.Reader, contentType string, headers ...string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, raw
}

func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("response is not a JSON object: %s", raw)
	}
	return decoded
}

// routerOptionsSet is true in the module generated with every key of the
// naming file's [go_api] table set (TestFileTransferRoutesUnderTheRouterOptions
// writes router_options_mode_test.go, which sets it).
var routerOptionsSet bool

// refused posts body and checks the route answered 400 bad_request without
// calling the implementation; it returns the problem.
func refused(t *testing.T, server *httptest.Server, impl *statements, body io.Reader, contentType string) map[string]any {
	t.Helper()
	before := impl.calls
	resp, raw := send(t, server, http.MethodPost, "/api/statements", body, contentType)
	problem := decode(t, raw)
	if resp.StatusCode != http.StatusBadRequest || problem["code"] != "bad_request" {
		t.Fatalf("status %d, problem %v, want a 400 bad_request", resp.StatusCode, problem)
	}
	if impl.calls != before {
		t.Fatal("a refused request reached the implementation")
	}
	return problem
}

func TestImportAndExport(t *testing.T) {
	server, _ := serve(t)
	body, contentType := multipartBody(t, `{"accountId":"`+account+`","note":"march"}`,
		part{"statement", "march.csv", "text/csv", "date,amount\n2026-03-01,-12.50\n2026-03-02,100.00\n"},
		part{"receipt", "receipt.pdf", "application/pdf", "%PDF"})
	resp, raw := send(t, server, http.MethodPost, "/api/statements", body, contentType)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("import: %d %s", resp.StatusCode, raw)
	}
	want := map[string]any{"accountId": account, "filename": "march.csv", "lines": float64(2), "receipt": "receipt.pdf (4 bytes)", "note": "march"}
	if got := decode(t, raw)["data"]; !reflect.DeepEqual(got, want) {
		t.Errorf("import = %v, want %v", got, want)
	}

	// A media type with parameters and an extension in another case.
	body, contentType = multipartBody(t, `{"accountId":"`+account+`"}`, part{"statement", "April 2026.OFX", "application/x-ofx; charset=utf-8", "OFXHEADER:100\n<STMTTRN>\n"})
	if resp, raw := send(t, server, http.MethodPost, "/api/statements", body, contentType); resp.StatusCode != http.StatusOK {
		t.Fatalf("OFX import: %d %s", resp.StatusCode, raw)
	}

	resp, raw = send(t, server, http.MethodGet, "/api/statements/"+account+"/export", nil, "")
	for key, want := range map[string]string{
		"Content-Type":        "text/csv; charset=utf-8",
		"Content-Disposition": `attachment; filename="statement.csv"; filename*=UTF-8''statement.csv`,
		"Cache-Control":       "no-store",
	} {
		if got := resp.Header.Get(key); got != want {
			t.Errorf("export %s = %q, want %q", key, got, want)
		}
	}
	if string(raw) != "date,amount\n2026-03-01,-12.50\n2026-03-02,100.00\n<STMTTRN>\n" {
		t.Errorf("export = %q", raw)
	}

	resp, raw = send(t, server, http.MethodGet, "/api/statements/"+account+"/original", nil, "")
	if got := resp.Header.Get("Content-Disposition"); got != `attachment; filename="April 2026.OFX"; filename*=UTF-8''April%202026.OFX` {
		t.Errorf("original Content-Disposition = %q", got)
	}
	if resp.Header.Get("Content-Type") != "application/octet-stream" || string(raw) != "OFXHEADER:100\n<STMTTRN>\n" {
		t.Errorf("original = %q %q", resp.Header.Get("Content-Type"), raw)
	}

	resp, raw = send(t, server, http.MethodGet, "/api/statements/1VgEh72lXvTXkJ/export", nil, "")
	if resp.StatusCode != http.StatusNotFound || resp.Header.Get("Content-Type") != "application/problem+json" {
		t.Errorf("unknown account: %d %s", resp.StatusCode, raw)
	}
}

// TestPreview: a FileDownload with D141's members answers inline, as the
// media type and under the name the implementation chose from the schema's
// list, with the schema's Cache-Control and sandbox, nosniff and the
// implementation's validators; a request whose validators match them is
// answered 304 with no body; and a media type the schema does not list is
// a 500. The TypeScript router answers the same
// (internal/generator/tsrestgen/testdata/files_runtime.test.ts).
func TestPreview(t *testing.T) {
	server, _ := serve(t)
	imports := func(filename, contentType, text string) {
		t.Helper()
		body, bodyType := multipartBody(t, `{"accountId":"`+account+`"}`, part{"statement", filename, contentType, text})
		if resp, raw := send(t, server, http.MethodPost, "/api/statements", body, bodyType); resp.StatusCode != http.StatusOK {
			t.Fatalf("import %s: %d %s", filename, resp.StatusCode, raw)
		}
	}
	path := "/api/statements/" + account + "/preview"

	imports("march.csv", "text/csv", "date,amount\n2026-03-01,1\n")
	resp, raw := send(t, server, http.MethodGet, path, nil, "")
	for key, want := range map[string]string{
		"Content-Type":                 "text/plain; charset=utf-8",
		"Content-Disposition":          `inline; filename="statement.txt"; filename*=UTF-8''statement.txt`,
		"Cache-Control":                "private, no-cache",
		"X-Content-Type-Options":       "nosniff",
		"Content-Security-Policy":      "sandbox allow-popups",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Etag":                         `"len-25"`,
		"Last-Modified":                "Sun, 01 Mar 2026 00:00:00 GMT",
	} {
		if got := resp.Header.Get(key); got != want {
			t.Errorf("preview %s = %q, want %q", key, got, want)
		}
	}
	if resp.StatusCode != http.StatusOK || string(raw) != "date,amount\n2026-03-01,1\n" {
		t.Errorf("preview = %d %q", resp.StatusCode, raw)
	}

	for name, headers := range map[string][]string{
		"the tag":               {"If-None-Match", `"len-25"`},
		"the tag, weak, listed": {"If-None-Match", `"len-1", W/"len-25"`},
		"any tag":               {"If-None-Match", "*"},
		"the time":              {"If-Modified-Since", "Sun, 01 Mar 2026 00:00:00 GMT"},
		"a later time":          {"If-Modified-Since", "Mon, 02 Mar 2026 00:00:00 GMT"},
	} {
		resp, raw := send(t, server, http.MethodGet, path, nil, "", headers...)
		if resp.StatusCode != http.StatusNotModified || len(raw) != 0 {
			t.Errorf("%s: %d %q, want 304 and no body", name, resp.StatusCode, raw)
		}
		if resp.Header.Get("Etag") != `"len-25"` || resp.Header.Get("Cache-Control") != "private, no-cache" || resp.Header.Get("Content-Type") != "" {
			t.Errorf("%s: 304 headers = %v", name, resp.Header)
		}
	}
	for name, headers := range map[string][]string{
		"another tag":                  {"If-None-Match", `"len-1"`},
		"an earlier time":              {"If-Modified-Since", "Sat, 28 Feb 2026 23:59:59 GMT"},
		"another tag and a later time": {"If-None-Match", `"len-1"`, "If-Modified-Since", "Mon, 02 Mar 2026 00:00:00 GMT"},
		"a time that is no HTTP date":  {"If-Modified-Since", "2026-03-02T00:00:00Z"},
	} {
		if resp, raw := send(t, server, http.MethodGet, path, nil, "", headers...); resp.StatusCode != http.StatusOK || len(raw) == 0 {
			t.Errorf("%s: %d %q, want the file", name, resp.StatusCode, raw)
		}
	}

	// A media type and a name the implementation chose from the schema's list.
	imports("april.ofx", "application/x-ofx", "OFXHEADER:100\n")
	resp, raw = send(t, server, http.MethodGet, path, nil, "")
	if got := resp.Header.Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("OFX preview Content-Type = %q", got)
	}
	if got := resp.Header.Get("Content-Disposition"); got != `inline; filename="statement.html"; filename*=UTF-8''statement.html` {
		t.Errorf("OFX preview Content-Disposition = %q", got)
	}
	if string(raw) != "<pre>OFXHEADER:100\n</pre>" {
		t.Errorf("OFX preview = %q", raw)
	}

	// One outside it.
	imports("may.qfx", "application/x-ofx", "OFXHEADER:100\n")
	resp, raw = send(t, server, http.MethodGet, path, nil, "")
	if resp.StatusCode != http.StatusInternalServerError || resp.Header.Get("Content-Type") != "application/problem+json" || resp.Header.Get("Etag") != "" {
		t.Errorf("unlisted media type: %d %v %s", resp.StatusCode, resp.Header, raw)
	}
}

// TestClientVersions: routes registered with minimum client versions
// answer a client under its minimum 426 with the problem client_too_old,
// its request id and the three details, before the route runs; a client
// at its minimum, a client the list does not name, a version that is no
// semantic version and a request that names no client are served, and so
// is the OpenAPI document, which is no route of the API (D143). The
// TypeScript router answers the same
// (internal/generator/tsrestgen/testdata/files_runtime.test.ts).
func TestClientVersions(t *testing.T) {
	minimums, err := clientversion.Parse("cli=0.5.0,desktop=2.1.0")
	if err != nil {
		t.Fatal(err)
	}
	server, impl := serveWith(t, minimums)
	imports := func(client string) (*http.Response, []byte) {
		body, contentType := multipartBody(t, `{"accountId":"`+account+`"}`, part{"statement", "march.csv", "text/csv", "date,amount\n"})
		headers := []string{"X-Request-Id", "req-1"}
		if client != "" {
			headers = append(headers, "X-Client-Version", client)
		}
		return send(t, server, http.MethodPost, "/api/statements", body, contentType, headers...)
	}

	resp, raw := imports("cli/0.4.9")
	if resp.StatusCode != http.StatusUpgradeRequired || resp.Header.Get("Content-Type") != "application/problem+json" || impl.calls != 0 {
		t.Fatalf("an old client: %d %s, %d calls", resp.StatusCode, raw, impl.calls)
	}
	want := map[string]any{
		"type": "about:blank", "title": "Upgrade Required", "status": float64(426), "code": "client_too_old", "requestId": "req-1",
		"detail":  "cli 0.4.9 is older than 0.5.0, the oldest version of cli this API serves. Upgrade cli.",
		"details": map[string]any{"client": "cli", "version": "0.4.9", "minimum": "0.5.0"},
	}
	if got := decode(t, raw); !reflect.DeepEqual(got, want) {
		t.Errorf("problem = %v, want %v", got, want)
	}

	for _, client := range []string{"cli/0.5.0", "desktop/3.0.0-beta.1", "mobile/0.0.1", "cli/next", ""} {
		if resp, raw := imports(client); resp.StatusCode != http.StatusOK {
			t.Errorf("client %q: %d %s", client, resp.StatusCode, raw)
		}
	}
	if impl.calls != 5 {
		t.Errorf("the implementation ran %d times, want 5", impl.calls)
	}
	// A download is a route as any other, and the document is none.
	if resp, _ := send(t, server, http.MethodGet, "/api/statements/"+account+"/export", nil, "", "X-Client-Version", "desktop/2.0.9"); resp.StatusCode != http.StatusUpgradeRequired {
		t.Errorf("an old client's download: %d", resp.StatusCode)
	}
	if resp, _ := send(t, server, http.MethodGet, "/api/openapi.json", nil, "", "X-Client-Version", "desktop/2.0.9"); resp.StatusCode != http.StatusOK {
		t.Errorf("the OpenAPI document for an old client: %d", resp.StatusCode)
	}
}

// TestRefusedFiles: each file the statement field does not admit is
// refused at its field, with the field error the TypeScript router
// answers, before the JSON fields.
func TestRefusedFiles(t *testing.T) {
	server, impl := serve(t)
	for _, tc := range []struct {
		file   *part
		errors map[string]any
	}{
		{nil, map[string]any{"statement": []any{map[string]any{"validator": "required", "message": "required file"}}}},
		{&part{"statement", "march.csv", "text/csv", strings.Repeat("x", 1025)},
			map[string]any{"statement": []any{map[string]any{"validator": "uploadMaxBytes", "message": "file is 1025 bytes, over the 1024 allowed"}}}},
		{&part{"statement", "march.csv", "text/plain", "date,amount\n"},
			map[string]any{"statement": []any{map[string]any{"validator": "uploadContentType", "message": "file is text/plain, not one of text/csv, application/x-ofx"}}}},
		{&part{"statement", "march.csv", "", "date,amount\n"},
			map[string]any{"statement": []any{map[string]any{"validator": "uploadContentType", "message": "file is application/octet-stream, not one of text/csv, application/x-ofx"}}}},
		{&part{"statement", "march.xlsx", "text/csv", "date,amount\n"},
			map[string]any{"statement": []any{map[string]any{"validator": "uploadExtension", "message": "file name does not end in .csv, .ofx, .qfx"}}}},
	} {
		var parts []part
		if tc.file != nil {
			parts = append(parts, *tc.file)
		}
		body, contentType := multipartBody(t, `{"note":"not checked yet"}`, parts...)
		problem := refused(t, server, impl, body, contentType)
		if problem["detail"] != "Request body does not match the declared input" ||
			!reflect.DeepEqual(problem["details"], map[string]any{"location": "body", "reason": "validation failed"}) ||
			!reflect.DeepEqual(problem["errors"], tc.errors) {
			t.Errorf("problem = %v, want errors %v", problem, tc.errors)
		}
	}

	// A JSON body holds no file, so its required statement is missing.
	problem := refused(t, server, impl, strings.NewReader(`{"accountId":"`+account+`"}`), "application/json")
	if !reflect.DeepEqual(problem["errors"], map[string]any{"statement": []any{map[string]any{"validator": "required", "message": "required file"}}}) {
		t.Errorf("JSON body: %v", problem)
	}
}

// TestRefusedFields: the JSON fields are refused as a JSON input's are, the
// file fields set aside; a body neither multipart nor JSON is refused.
func TestRefusedFields(t *testing.T) {
	server, impl := serve(t)
	csv := part{"statement", "march.csv", "text/csv", "date,amount\n"}

	body, contentType := multipartBody(t, `{"accountId":"`+account+`","coupon":"x"}`, csv)
	if routerOptionsSet {
		// go_api.undeclared_input_keys = "keep" passes the key on to the
		// input type's decoder, which ignores it.
		if resp, raw := send(t, server, http.MethodPost, "/api/statements", body, contentType); resp.StatusCode != http.StatusOK {
			t.Errorf("unknown field under the router options: %d %s", resp.StatusCode, raw)
		}
	} else if problem := refused(t, server, impl, body, contentType); !reflect.DeepEqual(problem["details"], map[string]any{"location": "body", "reason": "unknown fields: coupon"}) {
		t.Errorf("unknown field: %v", problem)
	}
	body, contentType = multipartBody(t, `{`, csv)
	if problem := refused(t, server, impl, body, contentType); problem["detail"] != "Request body is not valid JSON" {
		t.Errorf("not JSON: %v", problem)
	}
	body, contentType = multipartBody(t, "", csv)
	if problem := refused(t, server, impl, body, contentType); !reflect.DeepEqual(problem["errors"], map[string]any{"accountId": []any{map[string]any{"validator": "required", "message": "required field"}}}) {
		t.Errorf("no data: %v", problem)
	}
	if problem := refused(t, server, impl, strings.NewReader("hello"), "text/plain"); problem["detail"] != "Request body is not multipart/form-data" {
		t.Errorf("text body: %v", problem)
	}
}

// TestBodyLimit: @bodyLimit caps the whole multipart body.
func TestBodyLimit(t *testing.T) {
	server, impl := serve(t)
	body, contentType := multipartBody(t, `{"accountId":"`+account+`"}`,
		part{"statement", "march.csv", "text/csv", "date,amount\n"},
		part{"receipt", "receipt.pdf", "application/pdf", strings.Repeat("x", 1024*1024+1)})
	resp, raw := send(t, server, http.MethodPost, "/api/statements", body, contentType)
	if resp.StatusCode != http.StatusRequestEntityTooLarge || impl.calls != 0 {
		t.Errorf("over @bodyLimit: %d %s", resp.StatusCode, raw)
	}
}

// TestPartLimit: a body of 1000 parts is read and one of 1001 refused as
// no multipart body, as the TypeScript router refuses it (D81). Go's
// ReadForm reads at most 1000 parts.
func TestPartLimit(t *testing.T) {
	server, impl := serve(t)
	// The data part and the statement, then notes up to n parts.
	parts := func(n int) []part {
		out := []part{{"statement", "march.csv", "text/csv", "date,amount\n"}}
		for len(out) < n-1 {
			out = append(out, part{"note", "", "", "x"})
		}
		return out
	}
	body, contentType := multipartBody(t, `{"accountId":"`+account+`"}`, parts(1000)...)
	if resp, raw := send(t, server, http.MethodPost, "/api/statements", body, contentType); resp.StatusCode != http.StatusOK {
		t.Fatalf("1000 parts: %d %s", resp.StatusCode, raw)
	}
	body, contentType = multipartBody(t, `{"accountId":"`+account+`"}`, parts(1001)...)
	if problem := refused(t, server, impl, body, contentType); problem["detail"] != "Request body is not multipart/form-data" {
		t.Errorf("1001 parts: %v", problem)
	}
}
