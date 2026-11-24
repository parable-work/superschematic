package response

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fileAnswerParity is runtime/http/testdata/file_answer_parity.json.
type fileAnswerParity struct {
	ETags []struct {
		Name, Tag, Header string
		Refused           bool
	} `json:"etags"`
	Dates []struct {
		Name, Value string
		Seconds     *int64
	} `json:"dates"`
	Matches []struct {
		Name, Header, ETag string
		Names              bool
	} `json:"matches"`
	Answers []struct {
		Name     string
		Download struct {
			ContentType  string
			ContentTypes []string
			Filename     string
			Disposition  string
			CacheControl string
			Sandbox      json.RawMessage
		}
		File struct {
			Filename, ContentType string
			ETag                  string `json:"etag"`
			LastModified          string
			Headers               map[string]string
		}
		Method  string
		Request map[string]string
		Status  int
		Body    *string
		Headers map[string]*string
	} `json:"answers"`
}

func readFileAnswerParity(t *testing.T) fileAnswerParity {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "file_answer_parity.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parity fileAnswerParity
	if err := json.Unmarshal(raw, &parity); err != nil {
		t.Fatal(err)
	}
	if len(parity.ETags) == 0 || len(parity.Dates) == 0 || len(parity.Matches) == 0 || len(parity.Answers) == 0 {
		t.Fatal("a section holds no vector")
	}
	return parity
}

// TestFileValidatorParity: EntityTag, ParseHTTPDate and IfNoneMatchNames
// answer each vector of file_answer_parity.json as the TypeScript
// runtime's entityTag, parseHttpDate and ifNoneMatchNames do (D141).
func TestFileValidatorParity(t *testing.T) {
	parity := readFileAnswerParity(t)
	for _, vector := range parity.ETags {
		got, err := EntityTag(vector.Tag)
		if vector.Refused {
			if err == nil {
				t.Errorf("%s: EntityTag(%q) = %q, want a refusal", vector.Name, vector.Tag, got)
			}
			continue
		}
		if err != nil || got != vector.Header {
			t.Errorf("%s: EntityTag(%q) = %q, %v, want %q", vector.Name, vector.Tag, got, err, vector.Header)
		}
	}
	for _, vector := range parity.Dates {
		got, ok := ParseHTTPDate(vector.Value)
		switch {
		case vector.Seconds == nil && ok:
			t.Errorf("%s: ParseHTTPDate(%q) = %d, want no date", vector.Name, vector.Value, got)
		case vector.Seconds != nil && (!ok || got != *vector.Seconds):
			t.Errorf("%s: ParseHTTPDate(%q) = %d, %t, want %d", vector.Name, vector.Value, got, ok, *vector.Seconds)
		}
	}
	for _, vector := range parity.Matches {
		if got := IfNoneMatchNames(vector.Header, vector.ETag); got != vector.Names {
			t.Errorf("%s: IfNoneMatchNames(%q, %q) = %t, want %t", vector.Name, vector.Header, vector.ETag, got, vector.Names)
		}
	}
}

// TestFileAnswerParity: ServeFile answers each vector's request with its
// status and headers, as the TypeScript runtime's downloadResponse does
// (D141), and closes the body whether it sent it or not.
func TestFileAnswerParity(t *testing.T) {
	for _, vector := range readFileAnswerParity(t).Answers {
		t.Run(vector.Name, func(t *testing.T) {
			spec := FileSpec{
				ContentType:  vector.Download.ContentType,
				ContentTypes: vector.Download.ContentTypes,
				Filename:     vector.Download.Filename,
				Disposition:  vector.Download.Disposition,
				CacheControl: vector.Download.CacheControl,
			}
			if raw := vector.Download.Sandbox; len(raw) > 0 {
				if err := json.Unmarshal(raw, &spec.Sandbox); err != nil {
					if err := json.Unmarshal(raw, &spec.SandboxAllow); err != nil {
						t.Fatalf("sandbox %s: %v", raw, err)
					}
				}
			}
			body := &closingReader{Reader: strings.NewReader("the file")}
			file := &FileDownload{Body: body, Filename: vector.File.Filename, ContentType: vector.File.ContentType, ETag: vector.File.ETag}
			if vector.File.LastModified != "" {
				at, err := time.Parse(time.RFC3339Nano, vector.File.LastModified)
				if err != nil {
					t.Fatal(err)
				}
				file.LastModified = at
			}
			if len(vector.File.Headers) > 0 {
				file.Header = http.Header{}
				for key, value := range vector.File.Headers {
					file.Header.Set(key, value)
				}
			}
			request := httptest.NewRequest(vector.Method, "/api/files/1", nil)
			for key, value := range vector.Request {
				request.Header.Set(key, value)
			}
			recorder := httptest.NewRecorder()
			ServeFile(recorder, request, spec, file)
			result := recorder.Result()
			if result.StatusCode != vector.Status {
				t.Fatalf("status %d, want %d: %s", result.StatusCode, vector.Status, recorder.Body)
			}
			for key, want := range vector.Headers {
				got, sent := result.Header[http.CanonicalHeaderKey(key)]
				switch {
				case want == nil && sent:
					t.Errorf("%s = %q, want none", key, got)
				case want != nil && (len(got) != 1 || got[0] != *want):
					t.Errorf("%s = %q, want %q", key, got, *want)
				}
			}
			switch {
			case vector.Body != nil:
				if got := recorder.Body.String(); got != *vector.Body {
					t.Errorf("body %q, want %q", got, *vector.Body)
				}
			case vector.Status == http.StatusOK:
				if got := recorder.Body.String(); got != "the file" {
					t.Errorf("body %q, want the file", got)
				}
			}
			if !body.closed {
				t.Error("the body was not closed")
			}
		})
	}
}

// TestFileSpecSandboxPolicy: a spec with tokens and no Sandbox is still a
// sandbox, and one with neither is none.
func TestFileSpecSandboxPolicy(t *testing.T) {
	for _, c := range []struct {
		spec FileSpec
		want string
	}{
		{FileSpec{}, ""},
		{FileSpec{Sandbox: true}, "sandbox"},
		{FileSpec{SandboxAllow: []string{"allow-scripts"}}, "sandbox allow-scripts"},
		{FileSpec{Sandbox: true, SandboxAllow: []string{"allow-scripts", "allow-forms"}}, "sandbox allow-scripts allow-forms"},
	} {
		if got := c.spec.SandboxPolicy(); got != c.want {
			t.Errorf("%+v: %q, want %q", c.spec, got, c.want)
		}
	}
}
