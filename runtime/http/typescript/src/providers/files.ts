/*
The file processor (D159): what attachments (D154) and search (D155) call
on a stored file. It extracts the file's text and makes a preview, an
image a list shows in place of the file. A processor names the media types
it reads; for any other it answers null, and core tries the next one.
*/

/** A file to process: its bytes and what they are. */
export interface FileInput {
  readonly mediaType: string;
  readonly bytes: Uint8Array;
  readonly name?: string;
}

/** A file's text. */
export interface ExtractedText {
  readonly text: string;
  /** The pages it came from, for a paged document. */
  readonly pages?: number;
  /** Whether the text stops short of the file's. */
  readonly truncated?: boolean;
}

/** How large a preview may be. */
export interface PreviewOptions {
  readonly maxWidth?: number;
  readonly maxHeight?: number;
}

/** A preview: an image's bytes and what they are. */
export interface Preview {
  readonly mediaType: string;
  readonly bytes: Uint8Array;
  readonly width: number;
  readonly height: number;
}

/** A file processor. */
export interface FileProcessor {
  /** The media types it reads, each a type or a family ("image/*"). */
  readonly mediaTypes: readonly string[];
  /** The file's text, or null for a file it does not read. */
  extractText(file: FileInput): Promise<ExtractedText | null>;
  /** The file's preview, or null for a file it does not preview. */
  preview(file: FileInput, options?: PreviewOptions): Promise<Preview | null>;
}

/** Whether a processor that reads mediaTypes reads a file of mediaType. */
export function readsMediaType(mediaTypes: readonly string[], mediaType: string): boolean {
  const type = mediaType.split(';')[0]!.trim().toLowerCase();
  return mediaTypes.some(entry => {
    const want = entry.toLowerCase();
    return want === type || (want.endsWith('/*') && type.startsWith(want.slice(0, -1)));
  });
}
