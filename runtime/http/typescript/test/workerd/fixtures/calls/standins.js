// The stand-in for the r2_bucket binding workerd does not provide on its
// own, which the calls fixture's config.capnp binds as a wrapped binding:
// workerd calls the entrypoint and binds what it returns. It holds objects
// in memory, with the methods of R2 that the runtime's ./r2 entry calls,
// and keeps each object's content type, as R2's httpMetadata does.

/** An R2 bucket over a map. */
export function r2() {
  const objects = new Map();
  const meta = key => {
    const object = objects.get(key);
    return { key, size: object.bytes.length, uploaded: new Date(0), httpMetadata: object.httpMetadata };
  };
  return {
    put: async (key, value, options) => {
      const bytes =
        typeof value === 'string'
          ? new TextEncoder().encode(value)
          : ArrayBuffer.isView(value)
            ? new Uint8Array(value.buffer, value.byteOffset, value.byteLength).slice()
            : new Uint8Array(value).slice();
      objects.set(key, { bytes, httpMetadata: options?.httpMetadata ?? {} });
      return meta(key);
    },
    get: async key => (objects.has(key) ? { ...meta(key), body: new Response(objects.get(key).bytes).body } : null),
    head: async key => (objects.has(key) ? meta(key) : null),
    delete: async keys => {
      for (const key of [keys].flat()) objects.delete(key);
    },
    list: async options => ({
      objects: [...objects.keys()]
        .filter(key => key.startsWith(options?.prefix ?? ''))
        .sort()
        .map(meta),
      truncated: false,
    }),
  };
}
