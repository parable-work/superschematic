import { describe, expect, test } from 'bun:test';
import { runDeriver, type DerivationAnswer, type DerivationItem, type DerivationSettlement, type DerivedTable, type SettleResult } from './deriver';

/**
 * A table's Derived operations in memory, as the behaviors' core keeps
 * them: a field is stale until a value settles under the hash it was
 * pulled with, a failure counts an attempt, and a field out of attempts
 * leaves the pull.
 */
class FakeTable {
  readonly fields = new Map<string, { id: string; field: string; sourceHash: string; attempts: number; value?: unknown; confidence?: number; inputs: Record<string, unknown> }>();
  pulls: Array<{ limit?: number; cursor?: string }> = [];
  settles: DerivationSettlement[][] = [];
  pullErrors = 0;
  settleErrors = 0;
  /** What a pull or a settle that fails throws. */
  failure: () => unknown = () => new Error('the database is down');
  /** Fields another behavior's guard holds: a settle skips them and counts no attempt. */
  held = new Set<string>();
  maxAttempts = 2;

  stale(id: string, field: string, inputs: Record<string, unknown>): void {
    this.fields.set(`${id}/${field}`, { id, field, sourceHash: `hash-${id}-${field}`, attempts: 0, inputs });
  }

  table(): DerivedTable {
    return {
      staleDerivations: async params => {
        this.pulls.push(params);
        if (this.pullErrors > 0) {
          this.pullErrors--;
          throw this.failure();
        }
        const stale = [...this.fields.values()].filter(f => f.value === undefined && f.attempts < this.maxAttempts);
        const from = params.cursor === undefined ? 0 : Number(params.cursor);
        const limit = params.limit ?? 50;
        const items: DerivationItem[] = stale.slice(from, from + limit).map(f => ({
          id: f.id,
          field: f.field,
          sourceHash: f.sourceHash,
          attempt: f.attempts + 1,
          instruction: `Fill ${f.field}.`,
          model: 'standard',
          inputs: f.inputs,
          output: { type: 'string' },
        }));
        // The cursor counts the stale fields read so far, as they were at
        // the pull: a settle between pages moves the list, which the loop
        // must survive, as it does the core's.
        return { items, next: from + limit < stale.length ? String(from + limit) : null };
      },
      settleDerivations: async ({ items }) => {
        if (this.settleErrors > 0) {
          this.settleErrors--;
          throw this.failure();
        }
        // The core holds the whole call to its parameters: one item of
        // another shape refuses every item.
        for (const item of items) {
          const extra = Object.keys(item).filter(key => !['id', 'field', 'sourceHash', 'value', 'confidence', 'usage', 'failure'].includes(key));
          const usage = item.usage ?? {};
          const counts = [usage.inputTokens, usage.outputTokens, usage.costMicros].filter(count => count !== undefined);
          const malformed =
            extra.length > 0 ||
            (item.value === undefined) === (item.failure === undefined) ||
            (item.confidence !== undefined && !(item.confidence >= 0 && item.confidence <= 1)) ||
            counts.some(count => !Number.isInteger(count) || (count as number) < 0) ||
            (usage.modelId ?? '').length > 200 ||
            (item.failure !== undefined && (item.failure.message.length > 2000 || (item.failure.class ?? '').length > 64));
          if (malformed) throw Object.assign(new Error(`settleDerivations: ${item.id}/${item.field} is malformed`), { code: 'invalid_argument' });
        }
        this.settles.push(items);
        const result = { settled: 0, failed: 0, skipped: [] as Array<{ id: string; field: string; reason: string }> };
        for (const item of items) {
          const held = this.fields.get(`${item.id}/${item.field}`);
          if (this.held.has(`${item.id}/${item.field}`)) {
            result.skipped.push({ id: item.id, field: item.field, reason: 'vetoed' });
          } else if (held === undefined) {
            result.skipped.push({ id: item.id, field: item.field, reason: 'not_found' });
          } else if (held.sourceHash !== item.sourceHash) {
            result.skipped.push({ id: item.id, field: item.field, reason: 'moved' });
          } else if (item.failure !== undefined) {
            held.attempts++;
            result.failed++;
          } else {
            held.value = item.value;
            held.confidence = item.confidence;
            result.settled++;
          }
        }
        return result;
      },
    };
  }
}

const sleep = (ms: number) => new Promise(resolve => setTimeout(resolve, ms));

/** Waits until done answers true, or fails after a second. */
async function until(done: () => boolean): Promise<void> {
  for (let i = 0; i < 200; i++) {
    if (done()) return;
    await sleep(5);
  }
  throw new Error('the deriver did not get there in a second');
}

/** A model that is not one: it answers each field from its inputs. */
function fakeModel(table: string, item: DerivationItem): DerivationAnswer {
  const subject = String(item.inputs.subject ?? '');
  if (subject.includes('throws')) {
    throw new Error(`the model failed on ${table} ${item.id}`);
  }
  if (subject.includes('refuses')) {
    return { failure: { class: 'refused', message: 'the model would not say' } };
  }
  return { value: subject.includes('card') ? 'billing' : 'other', confidence: 0.9, usage: { inputTokens: 12, outputTokens: 1 } };
}

describe('runDeriver', () => {
  test('pulls the stale fields of each table, hands each to the answer and settles with the hash it pulled', async () => {
    const tickets = new FakeTable();
    tickets.stale('t1', 'category', { subject: 'card declined' });
    tickets.stale('t2', 'category', { subject: 'where is my parcel' });
    const articles = new FakeTable();
    articles.stale('a1', 'summary', { subject: 'a card trick' });
    const stop = new AbortController();
    const seen: string[] = [];
    const settled: Array<[string, SettleResult]> = [];
    const running = runDeriver({
      tables: { Ticket: tickets.table(), Article: articles.table() },
      answer: (table, item) => {
        seen.push(`${table}.${item.field} ${item.id} attempt ${item.attempt} ${item.model}: ${item.instruction}`);
        return fakeModel(table, item);
      },
      idleMs: 5,
      signal: stop.signal,
      onSettled: (table, result) => settled.push([table, result]),
    });
    await until(() => tickets.fields.get('t2/category')?.value !== undefined && articles.fields.get('a1/summary')?.value !== undefined);
    stop.abort();
    await running;

    expect(seen).toEqual([
      'Ticket.category t1 attempt 1 standard: Fill category.',
      'Ticket.category t2 attempt 1 standard: Fill category.',
      'Article.summary a1 attempt 1 standard: Fill summary.',
    ]);
    expect(tickets.settles[0]).toEqual([
      { id: 't1', field: 'category', sourceHash: 'hash-t1-category', value: 'billing', confidence: 0.9, usage: { inputTokens: 12, outputTokens: 1 } },
      { id: 't2', field: 'category', sourceHash: 'hash-t2-category', value: 'other', confidence: 0.9, usage: { inputTokens: 12, outputTokens: 1 } },
    ]);
    expect(tickets.fields.get('t1/category')?.value).toBe('billing');
    expect(articles.fields.get('a1/summary')?.value).toBe('billing');
    expect(settled.slice(0, 2)).toEqual([
      ['Ticket', { settled: 2, failed: 0, skipped: [] }],
      ['Article', { settled: 1, failed: 0, skipped: [] }],
    ]);
    // The first pull of a pass carries no cursor, and each asks for the default batch.
    expect(tickets.pulls[0]).toEqual({ limit: 50 });
  });

  test('pages a table by its cursor, a batch at a time, and settles each page', async () => {
    const tickets = new FakeTable();
    for (let i = 0; i < 5; i++) tickets.stale(`t${i}`, 'category', { subject: 'card' });
    const stop = new AbortController();
    const running = runDeriver({ tables: { Ticket: tickets.table() }, answer: fakeModel, batchSize: 2, idleMs: 5, signal: stop.signal });
    await until(() => [...tickets.fields.values()].every(f => f.value !== undefined));
    stop.abort();
    await running;
    expect(tickets.pulls[0]).toEqual({ limit: 2 });
    expect(tickets.pulls[1]).toEqual({ limit: 2, cursor: '2' });
    expect(tickets.settles.every(items => items.length <= 2)).toBe(true);
    expect(tickets.settles.flat().length).toBe(5);
  });

  test('an answer that throws, and one that gives a failure, settle as failures, which count attempts', async () => {
    const tickets = new FakeTable();
    tickets.stale('t1', 'category', { subject: 'throws' });
    tickets.stale('t2', 'category', { subject: 'refuses' });
    tickets.stale('t3', 'category', { subject: 'card' });
    const stop = new AbortController();
    const running = runDeriver({ tables: { Ticket: tickets.table() }, answer: fakeModel, idleMs: 5, signal: stop.signal });
    await until(() => tickets.fields.get('t1/category')?.attempts === 2 && tickets.fields.get('t2/category')?.attempts === 2);
    stop.abort();
    await running;
    expect(tickets.settles[0]).toEqual([
      { id: 't1', field: 'category', sourceHash: 'hash-t1-category', failure: { class: 'thrown', message: 'the model failed on Ticket t1' } },
      { id: 't2', field: 'category', sourceHash: 'hash-t2-category', failure: { class: 'refused', message: 'the model would not say' } },
      { id: 't3', field: 'category', sourceHash: 'hash-t3-category', value: 'billing', confidence: 0.9, usage: { inputTokens: 12, outputTokens: 1 } },
    ]);
    // Out of attempts, the two leave the pull, and their values stay unset.
    expect(tickets.fields.get('t1/category')?.value).toBeUndefined();
    expect(tickets.fields.get('t3/category')?.value).toBe('billing');
  });

  test('runs at most its concurrency of answers at a time, and keeps their order in the settle', async () => {
    const tickets = new FakeTable();
    for (let i = 0; i < 6; i++) tickets.stale(`t${i}`, 'category', { subject: `card ${i}` });
    const stop = new AbortController();
    let running = 0;
    let most = 0;
    const done = runDeriver({
      tables: { Ticket: tickets.table() },
      answer: async (_table, item) => {
        running++;
        most = Math.max(most, running);
        await sleep(item.id === 't0' ? 20 : 2);
        running--;
        return { value: item.id };
      },
      concurrency: 2,
      idleMs: 5,
      signal: stop.signal,
    });
    await until(() => tickets.settles.length > 0);
    stop.abort();
    await done;
    expect(most).toBe(2);
    expect(tickets.settles[0]!.map(item => item.value)).toEqual(['t0', 't1', 't2', 't3', 't4', 't5']);
  });

  test('waits out a pull that fails and pulls again', async () => {
    const tickets = new FakeTable();
    tickets.stale('t1', 'category', { subject: 'card' });
    tickets.pullErrors = 1;
    const stop = new AbortController();
    const retries: Array<[string, number]> = [];
    const running = runDeriver({
      tables: { Ticket: tickets.table() },
      answer: fakeModel,
      idleMs: 5,
      signal: stop.signal,
      onRetry: (error, waitMs) => retries.push([error instanceof Error ? error.message : String(error), waitMs]),
    });
    await until(() => retries.length === 1);
    expect(retries).toEqual([['the database is down', 1000]]);
    expect(tickets.settles).toEqual([]);
    // The wait is a second: the deriver has not pulled again, and its
    // signal ends the wait at once.
    stop.abort();
    await running;
    expect(tickets.pulls.length).toBe(1);
  });

  test('stops at its signal while a pass finds nothing, and settles nothing once it aborts', async () => {
    const idle = new FakeTable();
    const stop = new AbortController();
    const running = runDeriver({ tables: { Ticket: idle.table() }, answer: fakeModel, idleMs: 60_000, signal: stop.signal });
    await until(() => idle.pulls.length === 1);
    stop.abort();
    await running;
    expect(idle.pulls.length).toBe(1);

    const tickets = new FakeTable();
    tickets.stale('t1', 'category', { subject: 'card' });
    const halt = new AbortController();
    await runDeriver({
      tables: { Ticket: tickets.table() },
      answer: (table, item) => {
        halt.abort();
        return fakeModel(table, item);
      },
      idleMs: 5,
      signal: halt.signal,
    });
    expect(tickets.settles).toEqual([]);
  });

  test('an answer of no shape a settle takes fails its own item, and the page still settles', async () => {
    const tickets = new FakeTable();
    for (const id of ['extra', 'empty', 'unsure', 'costly', 'wordy', 'text', 'fine']) tickets.stale(id, 'category', { subject: 'card' });
    const answers: Record<string, unknown> = {
      extra: { value: 'billing', confidence: 0.5, note: 'a member a settle does not take' },
      empty: {},
      unsure: { value: 'billing', confidence: 2 },
      costly: { value: 'billing', usage: { inputTokens: 12, outputTokens: -1, costMicros: 1.5, modelId: 'm'.repeat(300), region: 'eu' } },
      wordy: { failure: { class: 'c'.repeat(100), message: 'x'.repeat(5000), retry: true } },
      text: 'billing',
      fine: { value: 'other' },
    };
    const stop = new AbortController();
    const retries: unknown[] = [];
    const running = runDeriver({
      tables: { Ticket: tickets.table() },
      answer: (_table, item) => answers[item.id] as DerivationAnswer,
      idleMs: 5,
      signal: stop.signal,
      onRetry: error => retries.push(error),
    });
    await until(() => tickets.settles.length > 0);
    stop.abort();
    await running;
    const settled = Object.fromEntries(tickets.settles[0]!.map(item => [item.id, item]));
    const base = (id: string) => ({ id, field: 'category', sourceHash: `hash-${id}-category` });
    expect(settled.extra).toEqual({ ...base('extra'), value: 'billing', confidence: 0.5 });
    expect(settled.empty).toEqual({ ...base('empty'), failure: { class: 'invalid_answer', message: 'the answer holds neither a value nor a failure' } });
    expect(settled.unsure).toEqual({ ...base('unsure'), failure: { class: 'invalid_answer', message: "the answer's confidence, 2, is no number from 0 to 1" } });
    expect(settled.costly).toEqual({ ...base('costly'), value: 'billing', usage: { inputTokens: 12, costMicros: 2, modelId: 'm'.repeat(200) } });
    expect(settled.wordy).toEqual({ ...base('wordy'), failure: { class: 'c'.repeat(64), message: 'x'.repeat(2000) } });
    expect(settled.text).toEqual({ ...base('text'), failure: { class: 'invalid_answer', message: 'the answer is no object: it holds a value, or a failure with a message' } });
    expect(settled.fine).toEqual({ ...base('fine'), value: 'other' });
    // One call settled the page: the bad answers each cost their own item an attempt, and nothing was retried.
    expect(retries).toEqual([]);
    expect(tickets.fields.get('fine/category')?.value).toBe('other');
    expect(tickets.fields.get('empty/category')?.attempts).toBeGreaterThanOrEqual(1);
  });

  test('a pass that settles and fails nothing waits before it pulls again', async () => {
    const tickets = new FakeTable();
    tickets.stale('t1', 'category', { subject: 'card' });
    // Another behavior's guard holds the field: every settle skips it and counts no attempt.
    tickets.held.add('t1/category');
    const stop = new AbortController();
    let asked = 0;
    const running = runDeriver({
      tables: { Ticket: tickets.table() },
      answer: (table, item) => {
        asked++;
        return fakeModel(table, item);
      },
      idleMs: 60,
      signal: stop.signal,
    });
    await sleep(150);
    stop.abort();
    await running;
    // Back to back it would have asked hundreds of times.
    expect(asked).toBeGreaterThanOrEqual(1);
    expect(asked).toBeLessThanOrEqual(4);
  });

  test('a table that fails is waited out, each wait double the last up to the longest, while the tables after it go on', async () => {
    const down = new FakeTable();
    down.stale('d1', 'category', { subject: 'card' });
    down.pullErrors = 4;
    const up = new FakeTable();
    up.stale('u1', 'category', { subject: 'card' });
    const stop = new AbortController();
    const retries: Array<[string, number, string]> = [];
    const running = runDeriver({
      tables: { Down: down.table(), Up: up.table() },
      answer: fakeModel,
      idleMs: 5,
      retryMinMs: 10,
      retryMaxMs: 40,
      signal: stop.signal,
      onRetry: (error, waitMs, table) => retries.push([error instanceof Error ? error.message : String(error), waitMs, table]),
    });
    // The table after the failing one settles on the first pass.
    await until(() => up.fields.get('u1/category')?.value !== undefined);
    expect(down.fields.get('d1/category')?.value).toBeUndefined();
    await until(() => down.fields.get('d1/category')?.value !== undefined);
    expect(retries).toEqual([
      ['the database is down', 10, 'Down'],
      ['the database is down', 20, 'Down'],
      ['the database is down', 40, 'Down'],
      ['the database is down', 40, 'Down'],
    ]);
    // Once it answers again its wait starts over.
    down.stale('d2', 'category', { subject: 'card' });
    down.pullErrors = 1;
    await until(() => retries.length === 5);
    expect(retries[4]).toEqual(['the database is down', 10, 'Down']);
    stop.abort();
    await running;
  });

  test('a settle that throws is waited out, and its page is pulled and settled again', async () => {
    const tickets = new FakeTable();
    tickets.stale('t1', 'category', { subject: 'card' });
    tickets.settleErrors = 1;
    const stop = new AbortController();
    const retries: number[] = [];
    const running = runDeriver({
      tables: { Ticket: tickets.table() },
      answer: fakeModel,
      idleMs: 5,
      retryMinMs: 10,
      signal: stop.signal,
      onRetry: (_error, waitMs) => retries.push(waitMs),
    });
    await until(() => tickets.fields.get('t1/category')?.value !== undefined);
    stop.abort();
    await running;
    expect(retries).toEqual([10]);
    expect(tickets.settles.length).toBe(1);
  });

  test('a refusal time does not cure, and a programming error, end the loop with the table named', async () => {
    const tickets = new FakeTable();
    tickets.pullErrors = 1;
    tickets.failure = () => Object.assign(new Error('the caller does not hold tickets.derive'), { code: 'forbidden' });
    const retries: unknown[] = [];
    const forbidden = runDeriver({ tables: { Ticket: tickets.table() }, answer: fakeModel, idleMs: 5, onRetry: error => retries.push(error) });
    await expect(forbidden).rejects.toThrow('deriver: table Ticket: the caller does not hold tickets.derive');
    expect(retries).toEqual([]);

    const broken = new FakeTable();
    broken.pullErrors = 1;
    broken.failure = () => new TypeError('undefined is not a function');
    await expect(runDeriver({ tables: { Ticket: broken.table() }, answer: fakeModel, idleMs: 5 })).rejects.toThrow('deriver: table Ticket: undefined is not a function');

    // A refusal as busy is one time cures.
    const busy = new FakeTable();
    busy.stale('t1', 'category', { subject: 'card' });
    busy.pullErrors = 1;
    busy.failure = () => Object.assign(new Error('busy'), { code: 'busy' });
    const stop = new AbortController();
    const running = runDeriver({ tables: { Ticket: busy.table() }, answer: fakeModel, idleMs: 5, retryMinMs: 10, signal: stop.signal });
    await until(() => busy.fields.get('t1/category')?.value !== undefined);
    stop.abort();
    await running;
  });

  test('starts no answer after its signal aborts in the middle of a page', async () => {
    const tickets = new FakeTable();
    for (let i = 0; i < 6; i++) tickets.stale(`t${i}`, 'category', { subject: 'card' });
    const stop = new AbortController();
    const started: string[] = [];
    await runDeriver({
      tables: { Ticket: tickets.table() },
      answer: async (table, item) => {
        started.push(item.id);
        if (started.length === 2) stop.abort();
        await sleep(5);
        return fakeModel(table, item);
      },
      concurrency: 2,
      idleMs: 5,
      signal: stop.signal,
    });
    // The two lanes each started one answer; neither took another, and nothing settled.
    expect(started).toEqual(['t0', 't1']);
    expect(tickets.settles).toEqual([]);
  });
});
