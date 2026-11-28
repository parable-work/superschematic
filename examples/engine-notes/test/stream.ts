// A reader of a server-sent event stream, which both tests open on the
// namespace's events.
import assert from 'node:assert/strict';

/** A message of the stream: its event name and its data. */
export interface StreamMessage {
  event: string | undefined;
  data: any;
}

export interface Stream {
  /** next returns the next message, comments skipped, or fails after timeoutMs. */
  next(timeoutMs?: number): Promise<StreamMessage>;
  close(): void;
}

/** openStream reads the namespace's event stream at origin, with signals beside its events, as the caller headers names. */
export async function openStream(origin: string, headers: Record<string, string>): Promise<Stream> {
  const abort = new AbortController();
  const response = await fetch(`${origin}/namespaces/default/events?after=head&signals=1&schema=Note`, {
    headers: { ...headers, accept: 'text/event-stream' },
    signal: abort.signal,
  });
  assert.equal(response.status, 200);
  const reader = response.body!.pipeThrough(new TextDecoderStream()).getReader();
  let buffer = '';
  return {
    async next(timeoutMs = 5000): Promise<StreamMessage> {
      const deadline = Date.now() + timeoutMs;
      for (;;) {
        const end = buffer.indexOf('\n\n');
        if (end >= 0) {
          const frame = buffer.slice(0, end);
          buffer = buffer.slice(end + 2);
          const lines = frame.split('\n');
          const data = lines.find((line) => line.startsWith('data: '));
          if (data === undefined) {
            continue;
          }
          const event = lines.find((line) => line.startsWith('event: '))?.slice('event: '.length);
          return { event, data: JSON.parse(data.slice('data: '.length)) };
        }
        let timer: ReturnType<typeof setTimeout> | undefined;
        const timeout = new Promise<never>((_, reject) => {
          timer = setTimeout(() => reject(new Error('no message within the timeout')), Math.max(0, deadline - Date.now()));
        });
        try {
          const { done, value } = await Promise.race([reader.read(), timeout]);
          if (done) {
            throw new Error('the stream ended');
          }
          buffer += value;
        } finally {
          clearTimeout(timer);
        }
      }
    },
    close: () => abort.abort(),
  };
}
