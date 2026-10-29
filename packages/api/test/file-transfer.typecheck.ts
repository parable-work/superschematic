// Type test, checked by `bun run typecheck` (tsconfig.test.json) and never
// run: FileUpload marks a field of an input type a file, with its limits
// unsaid or named, and FileDownload a result that is a file of a named
// media type (D81).
import { HttpMethod, rest } from "@superschematic/api";
import type { FileDownload, FileUpload } from "@superschematic/api";

type UUID = string & { readonly __brand: "Identity.UUID" };

export abstract class ImportStatementInput {
  accountId: UUID;
  statement: FileUpload<{ maxBytes: 5242880; contentTypes: ["text/csv", "application/x-ofx"]; extensions: [".csv", ".ofx", ".qfx"] }>;
  receipt: FileUpload;
}

export abstract class BadUploadInput {
  // @ts-expect-error maxBytes is a number of bytes
  file: FileUpload<{ maxBytes: "5MB" }>;
  // @ts-expect-error a constraint FileUpload does not know
  other: FileUpload<{ maxFiles: 2 }>;
}

export abstract class ImportResult {
  imported: number;
}

export class Statements {
  @rest(HttpMethod.POST, "statements")
  importStatement(input: ImportStatementInput): ImportResult {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.GET, "statements/{accountId}/export")
  exportStatement(accountId: UUID): FileDownload<{ contentType: "text/csv"; filename: "statement.csv" }> {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.GET, "statements/{accountId}/pdf")
  // @ts-expect-error a download names its media type
  exportPdf(accountId: UUID): FileDownload<{ filename: "statement.pdf" }> {
    throw new Error("schema declaration only");
  }
}
