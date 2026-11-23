// examples/serve.ts serves: it starts on a port of its own over a database
// in memory, takes a define, a publish and a create from a caller its
// header names, and answers a request that names none as developer.
import { expect, test } from 'bun:test';
import { fileURLToPath } from 'node:url';

test('the example Bun.serve mounts the routes', async () => {
  const example = fileURLToPath(new URL('../examples/serve.ts', import.meta.url));
  const child = Bun.spawn([process.execPath, example], { env: { ...process.env, DEV_DB: ':memory:', PORT: '0' }, stdout: 'pipe', stderr: 'inherit' });
  try {
    const reader = child.stdout.getReader();
    let text = '';
    while (!/serving (\S+)/.test(text)) {
      const { done, value } = await reader.read();
      if (done) throw new Error(`the example exited: ${text}`);
      text += new TextDecoder().decode(value);
    }
    const base = text.match(/serving (\S+)/)![1]!;
    const post = (path: string, body?: unknown) =>
      fetch(`${base}${path}`, { method: 'POST', headers: { 'content-type': 'application/json', 'x-superschematic-subject': 'alice' }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
    expect((await post('/schemas', { name: 'Note', fields: [{ name: 'title', typeRef: { name: 'string' } }] })).status).toBe(200);
    expect((await post('/schemas/Note/publish')).status).toBe(200);
    const created = await post('/schemas/Note/instances', { id: 'n1', data: { title: 'Kept' } });
    expect(created.status).toBe(201);
    const listed = (await (await fetch(`${base}/schemas/Note/instances`)).json()) as { data: { items: { title: string }[] } };
    expect(listed.data.items.map(item => item.title)).toEqual(['Kept']);
  } finally {
    child.kill('SIGTERM');
    await child.exited;
  }
}, 60_000);
