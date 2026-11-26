// A file processor with no dependency (D159). It extracts the text of plain
// text and Markdown, previews them as an SVG of their first lines, and makes
// a thumbnail of a greyscale PGM image, the simplest image format there is,
// by averaging blocks of pixels. A processor for PDFs, photos and video
// implements the same two operations over a library or a service.
import {
  defineProvider,
  readsMediaType,
  type FileInput,
  type FileProcessor,
  type Preview,
} from '@superschematic/http-runtime/providers';

const TEXT = ['text/plain', 'text/markdown'];
const PGM = 'image/x-portable-graymap';

/** A greyscale image: its size and one byte per pixel, rows top to bottom. */
export interface Greymap {
  readonly width: number;
  readonly height: number;
  readonly pixels: Uint8Array;
}

/** Reads a PGM image, P2 (ASCII) or P5 (binary), of up to 255 levels. */
export function readPgm(bytes: Uint8Array): Greymap {
  let at = 0;
  const token = (): string => {
    for (;;) {
      while (at < bytes.length && /\s/u.test(String.fromCharCode(bytes[at]!))) at++;
      if (bytes[at] !== 0x23) break;
      while (at < bytes.length && bytes[at] !== 0x0a) at++;
    }
    const start = at;
    while (at < bytes.length && !/\s/u.test(String.fromCharCode(bytes[at]!))) at++;
    return new TextDecoder().decode(bytes.subarray(start, at));
  };
  const magic = token();
  if (magic !== 'P2' && magic !== 'P5') throw new Error(`text-preview: ${magic || 'no header'} is no PGM image`);
  const width = Number(token());
  const height = Number(token());
  const max = Number(token());
  if (!(width > 0 && height > 0 && max > 0 && max < 256)) throw new Error('text-preview: the PGM header is not a size and levels under 256');
  const pixels = new Uint8Array(width * height);
  if (magic === 'P5') {
    at++;
    pixels.set(bytes.subarray(at, at + pixels.length));
  } else {
    for (let i = 0; i < pixels.length; i++) pixels[i] = Number(token());
  }
  if (max !== 255) for (let i = 0; i < pixels.length; i++) pixels[i] = Math.round((pixels[i]! * 255) / max);
  return { width, height, pixels };
}

/** Writes a greymap as a binary PGM, P5. */
export function writePgm(image: Greymap): Uint8Array {
  const header = new TextEncoder().encode(`P5\n${image.width} ${image.height}\n255\n`);
  const out = new Uint8Array(header.length + image.pixels.length);
  out.set(header);
  out.set(image.pixels, header.length);
  return out;
}

/** The image made no larger than maxWidth by maxHeight, each pixel the mean of the block it covers. */
function shrink(image: Greymap, maxWidth: number, maxHeight: number): Greymap {
  const factor = Math.max(1, Math.ceil(image.width / maxWidth), Math.ceil(image.height / maxHeight));
  const width = Math.ceil(image.width / factor);
  const height = Math.ceil(image.height / factor);
  const pixels = new Uint8Array(width * height);
  for (let y = 0; y < height; y++) {
    for (let x = 0; x < width; x++) {
      let sum = 0;
      let count = 0;
      for (let dy = 0; dy < factor && y * factor + dy < image.height; dy++) {
        for (let dx = 0; dx < factor && x * factor + dx < image.width; dx++) {
          sum += image.pixels[(y * factor + dy) * image.width + x * factor + dx]!;
          count++;
        }
      }
      pixels[y * width + x] = Math.round(sum / count);
    }
  }
  return { width, height, pixels };
}

const escapeXml = (text: string) => text.replace(/[<>&"']/gu, ch => `&#${ch.charCodeAt(0)};`);

/** An SVG of the first lines of text. */
function textPreview(text: string, maxWidth: number, maxHeight: number): Preview {
  const lineHeight = 18;
  const lines = text.split(/\r?\n/u).slice(0, Math.max(1, Math.floor((maxHeight - 8) / lineHeight)));
  const height = Math.min(maxHeight, lines.length * lineHeight + 8);
  const chars = Math.max(4, Math.floor(maxWidth / 8));
  const rows = lines
    .map((line, i) => `<text x="4" y="${(i + 1) * lineHeight}">${escapeXml(line.length > chars ? `${line.slice(0, chars - 1)}…` : line)}</text>`)
    .join('');
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${maxWidth}" height="${height}" font-family="monospace" font-size="13"><rect width="100%" height="100%" fill="#fff"/>${rows}</svg>`;
  return { mediaType: 'image/svg+xml', bytes: new TextEncoder().encode(svg), width: maxWidth, height };
}

export const textPreviewProcessor = defineProvider({
  name: 'text-preview',
  implements: 'files',
  config: {
    MAX_CHARS: { optional: true, description: 'The longest text extract returns; 100000 when unset.' },
  },
  create(settings): FileProcessor {
    const maxChars = Number(settings.optionalText('MAX_CHARS') ?? 100_000);
    return {
      mediaTypes: [...TEXT, PGM],
      async extractText(file: FileInput) {
        if (!readsMediaType(TEXT, file.mediaType)) return null;
        const text = new TextDecoder().decode(file.bytes);
        return text.length > maxChars ? { text: text.slice(0, maxChars), truncated: true } : { text };
      },
      async preview(file: FileInput, options = {}) {
        const maxWidth = options.maxWidth ?? 320;
        const maxHeight = options.maxHeight ?? 160;
        if (readsMediaType(TEXT, file.mediaType)) return textPreview(new TextDecoder().decode(file.bytes), maxWidth, maxHeight);
        if (readsMediaType([PGM], file.mediaType)) {
          const thumb = shrink(readPgm(file.bytes), maxWidth, maxHeight);
          return { mediaType: PGM, bytes: writePgm(thumb), width: thumb.width, height: thumb.height };
        }
        return null;
      },
    };
  },
});

export default textPreviewProcessor;
