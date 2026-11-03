import { Identity } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { bodyLimit, FileDownload, FileUpload, HttpMethod, rest } from "@superschematic/api";

// A bank statement to import: the account it belongs to, the statement as
// a CSV or OFX file of at most 1 KiB, an optional receipt, and a note.
export abstract class ImportStatementInput {
  accountId: Identity.UUID;
  statement: FileUpload<{ maxBytes: 1024; contentTypes: ["text/csv", "application/x-ofx"]; extensions: [".csv", ".ofx", ".qfx"] }>;
  receipt: Nullable<FileUpload>;
  note: Nullable<string>;
}

// What an import read.
export abstract class ImportResult {
  accountId: Identity.UUID;
  filename: string;
  lines: number;
  receipt: Nullable<string>;
  note: Nullable<string>;
}

export class Statements {
  // Imports a statement's lines; the whole request is at most 1 MiB.
  @rest(HttpMethod.POST, "statements")
  @bodyLimit({ megabytes: 1 })
  importStatement(input: ImportStatementInput): ImportResult {
    throw new Error("schema declaration only");
  }

  // The lines an account's statements held, as CSV.
  @rest(HttpMethod.GET, "statements/{accountId}/export")
  exportStatement(accountId: Identity.UUID): FileDownload<{ contentType: "text/csv; charset=utf-8"; filename: "statement.csv" }> {
    throw new Error("schema declaration only");
  }

  // The last statement an account imported, as it was sent and named.
  @rest(HttpMethod.GET, "statements/{accountId}/original")
  originalStatement(accountId: Identity.UUID): FileDownload<{ contentType: "application/octet-stream" }> {
    throw new Error("schema declaration only");
  }
}
