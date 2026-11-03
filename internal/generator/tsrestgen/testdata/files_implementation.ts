/*
A typed implementation of fixture-files-api's statements (D81), staged in
the generated package before it is type-checked (TestGeneratedFilesEndToEnd),
so tsc holds the methods to their generated signatures: importStatement
takes args.files, a statement File and an optional receipt, beside
args.input without its file fields, and the two downloads answer a
FileDownload. files_runtime.test.ts serves it through the generated router
and calls it through the generated SDK.

The statements are kept in memory by account: the lines of every statement
an account imported, after its header, and the last statement as it was
sent, under its name.
*/
import type { FileDownload } from '@superschematic/http-runtime';
import { notFound } from '@superschematic/http-runtime';
import type { ImportResult } from '@schemas/fixture-files-api-types/types';
import type { ExportStatementArgs, ImportStatementArgs, OriginalStatementArgs, StatementsImplementation } from './interfaces';

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
};
