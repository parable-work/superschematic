package response

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestContentDispositionParity: ContentDisposition writes the header each
// vector of content_disposition_parity.json holds for its filename, as the
// TypeScript runtime's contentDisposition does (D81).
func TestContentDispositionParity(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "content_disposition_parity.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parity struct {
		Vectors []struct {
			Name, Filename, Header string
		} `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &parity); err != nil {
		t.Fatal(err)
	}
	if len(parity.Vectors) == 0 {
		t.Fatal("no vectors")
	}
	for _, vector := range parity.Vectors {
		if got := ContentDisposition(vector.Filename); got != vector.Header {
			t.Errorf("%s: ContentDisposition(%q) = %q, want %q", vector.Name, vector.Filename, got, vector.Header)
		}
	}
}

type closingReader struct {
	io.Reader
	closed bool
}

func (c *closingReader) Close() error {
	c.closed = true
	return nil
}

// TestFileAnswersTheFile: File sends the body with the schema's media type,
// the implementation's filename or else the schema's, the implementation's
// headers beside them and no-store unless it sets a Cache-Control, and
// closes the body; a nil file is a 500 problem.
func TestFileAnswersTheFile(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/statements/a/export", nil)
	body := &closingReader{Reader: strings.NewReader("date,amount\n")}
	recorder := httptest.NewRecorder()
	File(recorder, request, "text/csv; charset=utf-8", "statement.csv", &FileDownload{
		Body:   body,
		Header: http.Header{"Last-Modified": {"Sun, 01 Mar 2026 00:00:00 GMT"}, "Content-Type": {"text/html"}},
	})
	result := recorder.Result()
	for key, want := range map[string]string{
		"Content-Type":        "text/csv; charset=utf-8",
		"Content-Disposition": `attachment; filename="statement.csv"; filename*=UTF-8''statement.csv`,
		"Cache-Control":       "no-store",
		"Last-Modified":       "Sun, 01 Mar 2026 00:00:00 GMT",
	} {
		if got := result.Header.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if got := recorder.Body.String(); got != "date,amount\n" || result.StatusCode != http.StatusOK || !body.closed {
		t.Errorf("status %d, body %q, closed %t", result.StatusCode, got, body.closed)
	}

	recorder = httptest.NewRecorder()
	File(recorder, request, "application/octet-stream", "", &FileDownload{Body: strings.NewReader("x"), Filename: "M\u00e4rz.csv", Header: http.Header{"Cache-Control": {"private, max-age=60"}}})
	if got := recorder.Header().Get("Content-Disposition"); got != `attachment; filename="M_rz.csv"; filename*=UTF-8''M%C3%A4rz.csv` {
		t.Errorf("Content-Disposition = %q", got)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "private, max-age=60" {
		t.Errorf("Cache-Control = %q, want the implementation's", got)
	}

	recorder = httptest.NewRecorder()
	File(recorder, request, "text/csv", "", nil)
	if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), `"code":"internal_error"`) {
		t.Errorf("nil file: %d %s", recorder.Code, recorder.Body)
	}
}
