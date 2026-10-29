import { multipartBoundary, parseMultipart } from './multipart.js';
import type { OperationSpec, OperationUpload, RequestContext } from './operation.js';
import { badRequest } from './problem.js';

/*
A multipart input (D81). An operation whose input type has file fields
(FileUpload<C>, or a file-upload scalar) takes its body as
multipart/form-data: each file in a part named as its field, and the
input's other fields as JSON in the `data` part. A JSON body, with no file,
is read as any input is. The route checks the files first, in the input's
field order: a required one missing, one over its limit, one whose
Content-Type or filename extension its field does not admit. A refused
file is a refused input, as every generated server answers it (D42): 400
bad_request, "Request body does not match the declared input", reason
"validation failed", and the file's error at its field in `errors`. Then
the JSON fields are parsed and validated as a JSON body's are, the file
fields set aside.
*/

/** The files of a multipart input, by field name; a file the request left out is absent. */
export type UploadedFiles = Readonly<Record<string, File>>;

/** The decoded input of an operation with file parts: the JSON fields and the files. */
export interface UploadInput {
  readonly input: unknown;
  readonly files: UploadedFiles;
}

/** The detail of a body that is neither multipart/form-data nor JSON. */
export const BODY_NOT_MULTIPART = 'Request body is not multipart/form-data';

/** The part that holds the input's JSON fields. */
export const DATA_PART = 'data';

/** The media type a file part without a Content-Type has (RFC 7578, section 4.4). */
export const DEFAULT_PART_CONTENT_TYPE = 'application/octet-stream';

/**
 * The body cap of an operation with file parts and no @bodyLimit: every
 * file at its limit, beside the JSON cap for the rest of the body.
 */
export function uploadBodyLimitBytes(uploads: readonly OperationUpload[], jsonLimitBytes: number): number {
  return uploads.reduce((total, upload) => total + upload.maxBytes, jsonLimitBytes);
}

/** A refused file or input field: the reason and the field errors the router's input refusal carries. */
export class UploadInputError extends Error {
  constructor(
    readonly reason: string,
    readonly errors: Record<string, { validator: string; message: string }[]>
  ) {
    super(`input refused: ${reason}`);
    this.name = 'UploadInputError';
  }
}

/** The media type of a Content-Type, lowercased, without its parameters. */
export function mediaTypeOf(contentType: string): string {
  const mediaType = contentType.split(';', 1)[0]!.trim().toLowerCase();
  return mediaType === '' ? DEFAULT_PART_CONTENT_TYPE : mediaType;
}

/**
 * Checks one file part against its field: present when required, at most
 * maxBytes, of an admitted media type and with an admitted extension.
 * Returns the field's error, or undefined for a file it admits or an
 * optional one left out.
 */
export function checkUpload(upload: OperationUpload, file: File | undefined): { validator: string; message: string } | undefined {
  if (file === undefined) {
    return upload.required ? { validator: 'required', message: 'required file' } : undefined;
  }
  if (file.size > upload.maxBytes) {
    return { validator: 'uploadMaxBytes', message: `file is ${file.size} bytes, over the ${upload.maxBytes} allowed` };
  }
  const mediaType = mediaTypeOf(file.type);
  if (upload.contentTypes.length > 0 && !upload.contentTypes.includes(mediaType)) {
    return { validator: 'uploadContentType', message: `file is ${mediaType}, not one of ${upload.contentTypes.join(', ')}` };
  }
  if (upload.extensions.length > 0) {
    const name = file.name.toLowerCase();
    if (!upload.extensions.some(extension => name.endsWith(extension.toLowerCase()))) {
      return { validator: 'uploadExtension', message: `file name does not end in ${upload.extensions.join(', ')}` };
    }
  }
  return undefined;
}

/**
 * Parses an input's JSON fields with the input's strict parser, setting
 * aside the errors at its file fields, which the JSON never holds: the
 * file fields' values in the JSON are dropped first, and a refusal whose
 * only errors are at file fields converts the fields as they are. Any
 * other refusal is rethrown, its file fields' errors left out.
 */
export function parseUploadFields(spec: OperationSpec, value: unknown): unknown {
  const input = spec.input!;
  const uploads = spec.uploads ?? [];
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    return input.parse(value);
  }
  const fields: Record<string, unknown> = { ...(value as Record<string, unknown>) };
  for (const upload of uploads) delete fields[upload.name];
  try {
    return input.parse(fields);
  } catch (error) {
    const refused = error as { reason?: unknown; errors?: unknown };
    if (refused?.reason !== 'validation failed' || typeof refused.errors !== 'object' || refused.errors === null) throw error;
    const atFile = (path: string) => uploads.some(upload => path === upload.name || path.startsWith(`${upload.name}.`) || path.startsWith(`${upload.name}[`));
    const kept = Object.entries(refused.errors as Record<string, { validator: string; message: string }[]>).filter(([path]) => !atFile(path));
    if (kept.length > 0) throw new UploadInputError('validation failed', Object.fromEntries(kept));
    return input.fromJSON ? input.fromJSON(fields) : fields;
  }
}

/** A JSON text as the router parses a JSON body: undefined when empty. */
function parseJson(text: string): unknown {
  if (text === '') return undefined;
  try {
    return JSON.parse(text);
  } catch (error) {
    throw badRequest('Request body is not valid JSON', { cause: error });
  }
}

/** Reads the body of an operation with file parts: multipart/form-data, or JSON with no file. */
export async function decodeUploads(ctx: RequestContext, spec: OperationSpec): Promise<UploadInput> {
  const uploads = spec.uploads ?? [];
  const mediaType = mediaTypeOf(ctx.headers.get('content-type') ?? '');
  // The JSON text of the input's other fields; a multipart body without
  // data, or with an empty one, has none, so each of them is absent.
  let text = '';
  let multipart = false;
  const files: Record<string, File> = {};
  if (mediaType === 'application/json') {
    text = await ctx.raw.text();
  } else if (mediaType === 'multipart/form-data') {
    multipart = true;
    let parts;
    try {
      parts = parseMultipart(new Uint8Array(await ctx.raw.arrayBuffer()), multipartBoundary(ctx.headers.get('content-type')!));
    } catch (error) {
      throw badRequest(BODY_NOT_MULTIPART, { cause: error });
    }
    for (const upload of uploads) {
      // The first part of the name that is a file; a text part is no file.
      const part = parts.find(candidate => candidate.name === upload.name && candidate.filename !== undefined);
      if (part !== undefined) files[upload.name] = new File([part.body as BlobPart], part.filename!, { type: part.contentType });
    }
    // The first text part named data.
    const data = parts.find(part => part.name === DATA_PART && part.filename === undefined);
    if (data !== undefined) text = new TextDecoder().decode(data.body);
  } else {
    throw badRequest(BODY_NOT_MULTIPART);
  }

  // The files first, in the input's field order, as the Go router checks them.
  const fileErrors: Record<string, { validator: string; message: string }[]> = {};
  for (const upload of uploads) {
    const refused = checkUpload(upload, files[upload.name]);
    if (refused) fileErrors[upload.name] = [refused];
  }
  if (Object.keys(fileErrors).length > 0) throw new UploadInputError('validation failed', fileErrors);

  const fields = parseJson(text);
  if (fields === undefined && !multipart) {
    // An empty JSON body, as the router reads one without files.
    if (spec.input?.required) throw badRequest('Request body is required');
    return { input: undefined, files };
  }
  return { input: parseUploadFields(spec, fields ?? {}), files };
}
