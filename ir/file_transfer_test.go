package ir

import (
	"reflect"
	"strings"
	"testing"
)

// TestFileDownloadAnswer: the disposition, the media types and the sandbox
// policy a FileDownload answers with (D141), each as its servers write it.
func TestFileDownloadAnswer(t *testing.T) {
	plain := &FileDownloadDef{ContentType: "text/csv"}
	if got := plain.DispositionOrDefault(); got != FileDispositionAttachment {
		t.Errorf("disposition = %q, want attachment", got)
	}
	if got := plain.SandboxPolicy(); got != "" {
		t.Errorf("sandbox policy = %q, want none", got)
	}
	if got := plain.AllContentTypes(); !reflect.DeepEqual(got, []string{"text/csv"}) {
		t.Errorf("media types = %v", got)
	}

	page := &FileDownloadDef{
		ContentType:  "image/png",
		ContentTypes: []string{"image/jpeg", "image/png", "image/jpeg"},
		Disposition:  FileDispositionInline,
		Sandbox:      &FileDownloadSandbox{Allow: []string{"allow-scripts", "allow-popups"}},
	}
	if got := page.DispositionOrDefault(); got != "inline" {
		t.Errorf("disposition = %q, want inline", got)
	}
	if got := page.SandboxPolicy(); got != "sandbox allow-scripts allow-popups" {
		t.Errorf("sandbox policy = %q", got)
	}
	if got := page.AllContentTypes(); !reflect.DeepEqual(got, []string{"image/png", "image/jpeg"}) {
		t.Errorf("media types = %v, want each once", got)
	}
	if got := (&FileDownloadDef{ContentType: "text/html", Sandbox: &FileDownloadSandbox{}}).SandboxPolicy(); got != "sandbox" {
		t.Errorf("sandbox policy = %q, want sandbox", got)
	}
	var none *FileDownloadDef
	if none.DispositionOrDefault() != "attachment" || none.SandboxPolicy() != "" || none.AllContentTypes() != nil {
		t.Error("a nil download answers something")
	}
}

// TestValidateFileDownload: a FileDownload's members are refused when a
// header could not carry them as written (D81, D141).
func TestValidateFileDownload(t *testing.T) {
	valid := &FileDownloadDef{
		ContentType:  "text/plain; charset=utf-8",
		ContentTypes: []string{"text/html; charset=utf-8"},
		Filename:     "statement.txt",
		Disposition:  "inline",
		CacheControl: "private, max-age=60",
		Sandbox:      &FileDownloadSandbox{Allow: []string{"allow-scripts", "allow-same-origin"}},
	}
	if errs := ValidateFileDownload("Set.op", valid); len(errs) != 0 {
		t.Fatalf("a valid download is refused: %v", errs)
	}
	if errs := ValidateFileDownload("Set.op", nil); len(errs) != 0 {
		t.Fatalf("no download is refused: %v", errs)
	}
	for _, c := range []struct {
		name     string
		download FileDownloadDef
		want     string
	}{
		{"no media type", FileDownloadDef{ContentType: "csv"}, `contentType "csv" is not a media type`},
		{"a listed value that is no media type", FileDownloadDef{ContentType: "text/csv", ContentTypes: []string{"text/plain", "plain"}}, `contentTypes holds "plain"`},
		{"a filename with a directory", FileDownloadDef{ContentType: "text/csv", Filename: "a/b.csv"}, "is not a filename alone"},
		{"another disposition", FileDownloadDef{ContentType: "text/csv", Disposition: "form-data"}, `disposition "form-data" is not "attachment" or "inline"`},
		{"a Cache-Control with a line break", FileDownloadDef{ContentType: "text/csv", CacheControl: "private\r\nX: y"}, "is not a Cache-Control value"},
		{"a Cache-Control with a space around it", FileDownloadDef{ContentType: "text/csv", CacheControl: " private"}, "is not a Cache-Control value"},
		{"a Cache-Control outside ASCII", FileDownloadDef{ContentType: "text/csv", CacheControl: "privé"}, "is not a Cache-Control value"},
		{"a sandbox token without allow-", FileDownloadDef{ContentType: "text/html", Sandbox: &FileDownloadSandbox{Allow: []string{"scripts"}}}, `sandbox holds "scripts"`},
		{"a sandbox token with a separator", FileDownloadDef{ContentType: "text/html", Sandbox: &FileDownloadSandbox{Allow: []string{"allow-scripts; script-src *"}}}, "is not a sandbox token"},
		{"a sandbox token in capitals", FileDownloadDef{ContentType: "text/html", Sandbox: &FileDownloadSandbox{Allow: []string{"allow-Scripts"}}}, "is not a sandbox token"},
		{"a sandbox token that is allow- alone", FileDownloadDef{ContentType: "text/html", Sandbox: &FileDownloadSandbox{Allow: []string{"allow-"}}}, "is not a sandbox token"},
		{"a sandbox token twice", FileDownloadDef{ContentType: "text/html", Sandbox: &FileDownloadSandbox{Allow: []string{"allow-forms", "allow-forms"}}}, `sandbox holds "allow-forms" twice`},
	} {
		errs := ValidateFileDownload("Set.op", &c.download)
		if len(errs) != 1 || !strings.Contains(errs[0].Error(), c.want) || !strings.HasPrefix(errs[0].Error(), "Set.op: ") {
			t.Errorf("%s: %v, want one error containing %q", c.name, errs, c.want)
		}
	}
}
