/*
Runtime check of the generated router for object-lists-api with
drawing.annotate (run by TestGeneratedUnionFieldsRouter with API_DIR set to
the materialized package). Annotation's note and notes hold Note, a union of
the @strictJSON Pin and of Flag, which kind discriminates. The router parses
the input with parse<Input>Json and parse<Input>FromJSON, which reads each
Note through parseNoteFromJSON: a Flag's raisedAt reaches the implementation
as a Date, and a Pin the body breaks, or a kind no member has, answers the
400 of an input the parser refuses and never reaches it.
*/
import { describe, expect, test } from 'bun:test';
import { Hono } from 'hono';
import { errorHandler, notFoundHandler } from '@superschematic/http-runtime/hono';

const apiDir = process.env.API_DIR;
if (!apiDir) throw new Error('API_DIR is required');
const { buildRouter } = await import(`${apiDir}/router.ts`);

const when = '2026-01-02T03:04:05Z';

// What the implementation received, for the assertions that a refused
// request never reached it.
const received: { id: string; input: { note: any; notes?: any[] } }[] = [];

function app() {
  const root = new Hono();
  const implementations = {
    drawing: {
      annotate: async (args: { id: string; input: { note: any; notes?: any[] } }) => {
        received.push(args);
        return 'ok';
      },
      saveOutline: async () => [],
    },
  };
  root.route('/', buildRouter(implementations));
  root.notFound(notFoundHandler());
  root.onError(errorHandler());
  return root;
}

async function annotate(body: unknown) {
  const response = await app().request('/api/drawings/d1/annotation', {
    method: 'PUT',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify(body),
  });
  return { status: response.status, body: await response.json() };
}

/** Sends a body the router must refuse and returns the problem's details. */
async function refused(body: unknown) {
  const before = received.length;
  const response = await annotate(body);
  expect(response.status).toBe(400);
  expect(response.body).toMatchObject({ status: 400, code: 'bad_request', detail: 'Request body does not match the declared input' });
  expect(received.length).toBe(before);
  return response.body.details;
}

describe('generated router reads an input union field through its parser', () => {
  test('a Note reaches the implementation as its member, a Flag with raisedAt a Date', async () => {
    const response = await annotate({
      note: { kind: 'flag', raisedAt: when },
      notes: [{ kind: 'pin', label: 'here' }, { kind: 'flag', raisedAt: when }],
    });
    expect(response.status).toBe(200);
    const { id, input } = received.at(-1)!;
    expect(id).toBe('d1');
    expect(input.note.kind).toBe('flag');
    expect(input.note.raisedAt).toBeInstanceOf(Date);
    expect(input.note.raisedAt.toISOString()).toBe('2026-01-02T03:04:05.000Z');
    expect(input.notes![0]).toEqual({ kind: 'pin', label: 'here' });
    expect(input.notes![1].raisedAt).toBeInstanceOf(Date);
  });

  test('a @strictJSON Pin with a key it does not declare is refused', async () => {
    expect(await refused({ note: { kind: 'pin', label: 'here', note: 'x' } })).toMatchObject({ location: 'body', reason: 'unknown fields: note' });
    expect(await refused({ note: { kind: 'flag', raisedAt: when }, notes: [{ kind: 'pin' }] })).toMatchObject({ location: 'body', reason: 'validation failed' });
  });

  test('a kind no member has, or a note that is not an object, is refused', async () => {
    expect(await refused({ note: { kind: 'star' } })).toMatchObject({ location: 'body', reason: 'unknown discriminator' });
    expect(await refused({ note: 'flag' })).toMatchObject({ location: 'body', reason: 'expected an object' });
  });
});
