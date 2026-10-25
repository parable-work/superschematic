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

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

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

func serve(t *testing.T) (*httptest.Server, *statements) {
	t.Helper()
	impl := &statements{lines: map[types.IdentityUUID][]string{}, lastName: map[types.IdentityUUID]string{}, last: map[types.IdentityUUID][]byte{}}
	router := chi.NewRouter()
	if err := api.RegisterRoutes(router, api.Config{
		Logger:          zap.NewNop(),
		Implementations: api.Implementations{Statements: impl},
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

func send(t *testing.T, server *httptest.Server, method, path string, body io.Reader, contentType string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
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
	if problem := refused(t, server, impl, body, contentType); !reflect.DeepEqual(problem["details"], map[string]any{"location": "body", "reason": "unknown fields: coupon"}) {
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
