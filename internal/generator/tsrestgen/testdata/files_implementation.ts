/*
A typed implementation of fixture-files-api's statements (D81), staged in
the generated package before it is type-checked (TestGeneratedFilesEndToEnd),
so tsc holds the methods to their generated signatures: importStatement
takes args.files, a statement File and an optional receipt, beside
args.input without its file fields, and the downloads answer a
FileDownload: previewStatement's with the media type, the entity tag and
the time D141 lets one name. files_runtime.test.ts serves it through the
generated router and calls it through the generated SDK.

The statements are kept in memory by account: the lines of every statement
an account imported, after its header, and the last statement as it was
sent, under its name.
*/
import type { FileDownload } from '@superschematic/http-runtime';
import { notFound } from '@superschematic/http-runtime';
import type { ImportResult } from '@schemas/fixture-files-api-types/types';
import type {
  ExportStatementArgs,
  ImportStatementArgs,
  OriginalStatementArgs,
  PreviewStatementArgs,
  StatementsImplementation,
} from './interfaces';

/** What each account imported: its lines, and its last statement as it was sent. */
export const imported = new Map<string, { lines: string[]; last: { name: string; bytes: Uint8Array } }>();

export const statements: StatementsImplementation = {
  async importStatement(args: ImportStatementArgs): Promise<ImportResult> {
    const text = await args.files.statement.text();
    const lines = text.split(/\r?\n/u).slice(1).filter(line => line !== '');
    const account = imported.get(args.input.accountId) ?? { lines: [], last: { name: '', bytes: new Uint8Array() } };
    account.lines.push(...lines);
    account.last = { name: args.files.statement.name, bytes: new Uint8Array(await args.files.statement.arrayBuffer()) };
    imported.set(args.input.accountId, account);
    return {
      accountId: args.input.accountId,
      filename: args.files.statement.name,
      lines: lines.length,
      receipt: args.files.receipt ? `${args.files.receipt.name} (${args.files.receipt.size} bytes)` : null,
      note: args.input.note ?? null,
    };
  },

  async exportStatement(args: ExportStatementArgs): Promise<FileDownload> {
    const account = imported.get(args.accountId);
    if (!account) throw notFound('No statement for the account');
    // A stream, sent as it is written, under the schema's filename.
    const rows = ['date,amount', ...account.lines];
    return {
      body: new ReadableStream<Uint8Array>({
        start(controller) {
          for (const row of rows) controller.enqueue(new TextEncoder().encode(`${row}\n`));
          controller.close();
        },
      }),
    };
  },

  async originalStatement(args: OriginalStatementArgs): Promise<FileDownload> {
    const account = imported.get(args.accountId);
    if (!account) throw notFound('No statement for the account');
    return { body: account.last.bytes, filename: account.last.name };
  },

  // The last statement as a page shows it (D141): an OFX statement as a
  // page of its own media type and name, any other as the schema's text,
  // each with an entity tag of its length and a time. A stream, so a test
  // sees a 304 cancel it unread.
  async previewStatement(args: PreviewStatementArgs): Promise<FileDownload> {
    const account = imported.get(args.accountId);
    if (!account) throw notFound('No statement for the account');
    const name = account.last.name.toLowerCase();
    const text = new TextDecoder().decode(account.last.bytes);
    const bytes = new TextEncoder().encode(name.endsWith('.ofx') ? `<pre>${text}</pre>` : text);
    const body = new ReadableStream<Uint8Array>({
      pull(controller) {
        controller.enqueue(bytes);
        controller.close();
      },
      cancel() {
        previews.cancelled += 1;
      },
    });
    const validators = { etag: `len-${account.last.bytes.length}`, lastModified: new Date('2026-03-01T00:00:00.900Z') };
    if (name.endsWith('.ofx')) return { body, contentType: 'text/html; charset=utf-8', filename: 'statement.html', ...validators };
    // A media type the schema does not list: the route answers 500.
    if (name.endsWith('.qfx')) return { body, contentType: 'application/x-qfx', ...validators };
    return { body, ...validators };
  },
};

/** How many preview bodies the route cancelled unsent. */
export const previews = { cancelled: 0 };
