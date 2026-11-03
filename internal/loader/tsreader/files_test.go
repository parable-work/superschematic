package tsreader

import (
	"reflect"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// fileTransferService is an API with the declarations given and a
// Statements set holding the methods given (D81).
func fileTransferService(t *testing.T, decls, methods string) string {
	t.Helper()
	return decoratorTestService(t, "API", map[string]string{"src/a.schema.ts": `import { Identity } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { FileDownload, FileUpload, HttpMethod, rest } from "@superschematic/api";
export abstract class ImportResult {
  imported: number;
}
` + decls + `
export class Statements {
` + methods + `
}
`})
}

// TestFileTransfersLoad: FileUpload<C> makes a string field a file with
// C's constraints, bare or optional, and FileDownload<C> makes an
// operation's result a file of C's media type with no JSON type (D81).
func TestFileTransfersLoad(t *testing.T) {
	dir := fileTransferService(t, `export abstract class ImportStatementInput {
  accountId: Identity.UUID;
  statement: FileUpload<{ maxBytes: 5242880; contentTypes: ["text/csv", "application/x-ofx"]; extensions: readonly [".csv", ".ofx"] }>;
  receipt: Nullable<FileUpload>;
}`, `  @rest(HttpMethod.POST, "statements")
  importStatement(input: ImportStatementInput): ImportResult {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.GET, "statements/{accountId}/export")
  exportStatement(accountId: Identity.UUID): FileDownload<{ contentType: "text/csv; charset=utf-8"; filename: "statement.csv" }> {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.GET, "statements/{accountId}/raw")
  exportRaw(accountId: Identity.UUID): FileDownload<{ contentType: "application/octet-stream" }> {
    throw new Error("schema declaration only");
  }`)
	schema, _, err := LoadService(dir)
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	fields := schema.Types["ImportStatementInput"].Fields
	statement, receipt := fields[1], fields[2]
	if statement.TypeRef != (ir.TypeRef{Name: "string"}) || !statement.Required {
		t.Errorf("statement = %+v, want a required string", statement)
	}
	want := &ir.FileUploadConfig{MaxSize: 5242880, AllowedTypes: []string{"text/csv", "application/x-ofx"}, AllowedExtensions: []string{".csv", ".ofx"}}
	if !reflect.DeepEqual(statement.FileUpload, want) {
		t.Errorf("statement's upload = %+v, want %+v", statement.FileUpload, want)
	}
	if receipt.Required || receipt.TypeRef != (ir.TypeRef{Name: "string"}) || !reflect.DeepEqual(receipt.FileUpload, &ir.FileUploadConfig{}) {
		t.Errorf("receipt = %+v (upload %+v), want an optional string with no constraints", receipt, receipt.FileUpload)
	}
	if fields[0].FileUpload != nil {
		t.Errorf("accountId is an upload: %+v", fields[0].FileUpload)
	}

	ops := schema.OperationSets[0].Operations
	if ops[0].FileDownload != nil {
		t.Errorf("importStatement downloads: %+v", ops[0].FileDownload)
	}
	export, raw := ops[1], ops[2]
	if got, want := export.FileDownload, (&ir.FileDownloadDef{ContentType: "text/csv; charset=utf-8", Filename: "statement.csv"}); !reflect.DeepEqual(got, want) {
		t.Errorf("exportStatement's download = %+v, want %+v", got, want)
	}
	if export.TypeRef != (ir.TypeRef{}) || !export.Required {
		t.Errorf("exportStatement's result = %+v (required %t), want no type", export.TypeRef, export.Required)
	}
	if got, want := raw.FileDownload, (&ir.FileDownloadDef{ContentType: "application/octet-stream"}); !reflect.DeepEqual(got, want) {
		t.Errorf("exportRaw's download = %+v, want %+v", got, want)
	}
}

// TestFileTransfersRefused: each wrapper stays where it means something,
// and its config is written as the wrapper documents it (D81).
func TestFileTransfersRefused(t *testing.T) {
	for _, tc := range []struct {
		name, decls, methods, want string
	}{
		{
			name:  "an upload argument",
			decls: "",
			methods: `  @rest(HttpMethod.POST, "statements")
  importStatement(file: FileUpload): ImportResult { throw new Error("schema declaration only"); }`,
			want: "FileUpload is a field of the operation's input type",
		},
		{
			name:  "an upload result",
			decls: "",
			methods: `  @rest(HttpMethod.GET, "statements")
  getStatement(): FileUpload { throw new Error("schema declaration only"); }`,
			want: "FileUpload marks a field of an input type",
		},
		{
			name:    "a download field",
			decls:   `export abstract class Holder { file: FileDownload<{ contentType: "text/csv" }>; }`,
			methods: "",
			want:    "FileDownload marks an operation's result",
		},
		{
			name:  "a download argument",
			decls: "",
			methods: `  @rest(HttpMethod.POST, "statements")
  put(file: FileDownload<{ contentType: "text/csv" }>): ImportResult { throw new Error("schema declaration only"); }`,
			want: "FileDownload marks an operation's result, not an argument",
		},
		{
			name:    "an unknown upload constraint",
			decls:   `export abstract class In { file: FileUpload<{ maxBytes: 1024; maxFiles: 2 }>; }`,
			methods: "",
			want:    `FileUpload has no constraint "maxFiles"`,
		},
		{
			name:    "a fractional limit",
			decls:   `export abstract class In { file: FileUpload<{ maxBytes: 1.5 }>; }`,
			methods: "",
			want:    "FileUpload maxBytes must be a whole number of bytes",
		},
		{
			name:    "a media type with parameters",
			decls:   `export abstract class In { file: FileUpload<{ contentTypes: ["text/csv; charset=utf-8"] }>; }`,
			methods: "",
			want:    "not a lowercase media type without parameters",
		},
		{
			name:    "an extension without its dot",
			decls:   `export abstract class In { file: FileUpload<{ extensions: ["csv"] }>; }`,
			methods: "",
			want:    "not a filename extension with its dot",
		},
		{
			name:  "a download without a media type",
			decls: "",
			methods: `  @rest(HttpMethod.GET, "statements")
  // @ts-expect-error the media type is required
  exportStatement(): FileDownload<{ filename: "a.csv" }> { throw new Error("schema declaration only"); }`,
			want: "FileDownload<C> takes a type literal naming the media type",
		},
		{
			name:  "a download filename with a directory",
			decls: "",
			methods: `  @rest(HttpMethod.GET, "statements")
  exportStatement(): FileDownload<{ contentType: "text/csv"; filename: "../a.csv" }> { throw new Error("schema declaration only"); }`,
			want: "is not a filename alone",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := LoadService(fileTransferService(t, tc.decls, tc.methods))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadService = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}
