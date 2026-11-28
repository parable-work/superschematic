/*
A body read whole up to a limit: a request's, a fetched response's or a
bucket object's stream, read a chunk at a time and refused once more than
the limit arrives, so a body past it is never held. Each caller answers the
refusal its own way (a 413, a 400 of Go's wording, a metadata document
refused) and keeps its own limit. Nothing here imports from node:, so a
Workers bundle of a caller stays clean.
*/

/**
 * The bytes of body, read to its end, or undefined once more than limit
 * bytes arrive: it stops reading there and cancels the stream. No body
 * reads as no bytes; a read that fails rejects as the stream does.
 */
export async function readCapped(body: ReadableStream<Uint8Array> | null | undefined, limit: number): Promise<Uint8Array<ArrayBuffer> | undefined> {
  if (!body) return new Uint8Array(0);
  const reader = body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    size += value.byteLength;
    if (size > limit) {
      await reader.cancel().catch(() => undefined);
      return undefined;
    }
    chunks.push(value);
  }
  const whole = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) {
    whole.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return whole;
}
