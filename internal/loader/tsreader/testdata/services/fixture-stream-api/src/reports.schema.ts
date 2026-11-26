import { Nullable } from "@superschematic/schema";
import { HttpMethod, httpBinaryStream, manualRouteRegistration, QueryParam, rest } from "@superschematic/api";

// The columns of a report an export writes.
export abstract class ExportReportInput {
  columns: string[];
}

// What a report holds, and what a tool caller reads of an export.
export abstract class ReportSummary {
  reportId: string;
  rows: number;
}

export class Reports {
  // A report's summary, in the JSON envelope.
  @rest(HttpMethod.GET, "reports/{reportId}")
  describeReport(reportId: string): ReportSummary {
    throw new Error("schema declaration only");
  }

  // A report's rows as an Arrow IPC stream, which the route's handler
  // writes, with the export's id in a header.
  @rest(HttpMethod.POST, "reports/{reportId}/export")
  @manualRouteRegistration
  @httpBinaryStream({ contentType: "application/vnd.apache.arrow.stream", responseHeaders: ["X-Export-Id"] })
  exportReport(reportId: string, input: ExportReportInput, format: QueryParam<Nullable<string>>): ReportSummary {
    throw new Error("schema declaration only");
  }
}
