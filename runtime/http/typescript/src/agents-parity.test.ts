import { describe, expect, test } from 'bun:test';
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { bestPolicy, budgetCrossing, defaultAutonomy, effectiveAutonomy, narrowPermissions, narrowReaches, paramsHash, redactTokens, trustOf, type Autonomy, type OperationClass } from './agents';
import { emailAuthenticated } from './inbound';

/*
The agent orchestration's pure parts (D158, D183) and the inbound path's
(D157), which the Rust runtime's orm::agents and orm::inbound run too
(D193): a class's default autonomy, a message's trust, the autonomy a run
has over an operation, a party's paths narrowing a run's, an agent's
permissions narrowed to whom it acts for, the hash an approval is bound to,
the policy row that wins, what a use crosses of a budget, a transcript
entry's redaction, and whether an email's authentication passes. This test
computes every want from the inputs below and holds
runtime/http/testdata/agents_parity.json to them; AGENTS_PARITY_WRITE=1
writes the file instead. The Rust tests read the file (orm::agents' and
orm::inbound's the_parity_vectors_hold).
*/

const CORPUS = fileURLToPath(new URL('../../testdata/agents_parity.json', import.meta.url));

const classes: OperationClass[] = ['read', 'internalWrite', 'messageParty', 'moneyMovement'];
const modes: Autonomy[] = ['auto', 'tell', 'ask'];
const trusts: (string | null)[] = [null, 'verified', 'entered'];

const reaches: { role: Record<string, string[]>; party: Record<string, string[]>[] }[] = [
  { role: { TaskNote: ['task'], Invoice: ['task'] }, party: [] },
  { role: { TaskNote: ['task'], Invoice: ['task'] }, party: [{}] },
  { role: { TaskNote: ['task'], Invoice: ['task'] }, party: [{ TaskNote: ['task'] }] },
  { role: { TaskNote: ['task', 'task.parent'] }, party: [{ TaskNote: ['task.parent'] }, { TaskNote: ['task'] }] },
  { role: { Report: ['job', 'job'] }, party: [{ Report: ['job'] }] },
  { role: { Report: ['*'] }, party: [{ Report: ['job'] }] },
  { role: {}, party: [{ Report: ['job'] }] },
];

const permissions: { agent: string[]; actor: string[] }[] = [
  { agent: ['tasks.read', 'tasks.write', 'invoices.pay', 'parties.message'], actor: ['tasks.read', 'parties.message'] },
  { agent: ['tasks'], actor: ['tasks.read'] },
  { agent: ['tasks.read'], actor: ['tasks'] },
  { agent: ['tasks.read', 'reports'], actor: ['reports.export', 'tasks.read'] },
  { agent: ['invoices.pay'], actor: [] },
  { agent: [], actor: ['tasks'] },
];

const hashes: { operation: string; params: unknown }[] = [
  { operation: 'pay_invoice', params: { invoice: 'x', amount: 100 } },
  { operation: 'pay_invoice', params: { amount: 100, invoice: 'x' } },
  { operation: 'pay_invoice', params: { invoice: 'x', amount: 250 } },
  { operation: 'send_message', params: { party: { table: 'Customer', key: 'k' }, channel: 'sms', body: 'Booked' } },
  { operation: 'agents.overBudget', params: { agent: 'reviewer', run: 'r1' } },
  { operation: 'note_task', params: {} },
  { operation: 'note_task', params: { text: 'é ü 中', list: [1, 'two', null, true] } },
];

const policies: { rows: { agent?: string | null; operation?: string | null; operationClass?: string | null; mode: string }[]; operation: string; class: OperationClass }[] = [
  { rows: [], operation: 'read_task', class: 'read' },
  { rows: [{ agent: null, operationClass: 'read', mode: 'ask' }], operation: 'read_task', class: 'read' },
  { rows: [{ agent: null, operationClass: 'internalWrite', mode: 'ask' }], operation: 'read_task', class: 'read' },
  { rows: [{ agent: null, operationClass: 'read', mode: 'ask' }, { agent: 'a1', operationClass: 'read', mode: 'auto' }], operation: 'read_task', class: 'read' },
  { rows: [{ agent: 'a1', operationClass: 'read', mode: 'auto' }, { agent: null, operation: 'read_task', mode: 'ask' }], operation: 'read_task', class: 'read' },
  { rows: [{ agent: 'a1', operation: 'other', mode: 'auto' }, { agent: null, operation: 'read_task', mode: 'tell' }], operation: 'read_task', class: 'read' },
  { rows: [{ agent: 'a1', operation: 'read_task', mode: 'ask' }, { agent: 'a1', operationClass: 'read', mode: 'auto' }], operation: 'read_task', class: 'read' },
  { rows: [{ agent: 'a1', operationClass: 'read', mode: 'auto' }, { agent: 'a1', operationClass: 'read', mode: 'tell' }], operation: 'read_task', class: 'read' },
];

const budgets: { before: number; after: number; limit: number | null; warnAt: number }[] = [
  { before: 0, after: 3, limit: 6, warnAt: 50 },
  { before: 3, after: 4, limit: 6, warnAt: 50 },
  { before: 0, after: 2, limit: 6, warnAt: 50 },
  { before: 4, after: 9, limit: 6, warnAt: 50 },
  { before: 0, after: 80, limit: 100, warnAt: 80 },
  { before: 79, after: 81, limit: 100, warnAt: 80 },
  { before: 0, after: 5, limit: null, warnAt: 80 },
  { before: 0, after: 1, limit: 0, warnAt: 80 },
  { before: 99, after: 100, limit: 100, warnAt: 100 },
];

const redactions: { text: string; secrets: string[] }[] = [
  { text: 'no secret here', secrets: [] },
  { text: 'token grant_abcdefghijklmnopqrstuvwxyz0123 used', secrets: [] },
  { text: 'grant_short stays', secrets: [] },
  { text: 'grant_0123456789abcdef', secrets: [] },
  { text: 'Authorization: Bearer abc.def~ghi+/= then', secrets: [] },
  { text: 'Bearer', secrets: [] },
  { text: 'Bearer  ', secrets: [] },
  { text: 'turn 1: the search sent Bearer abc.def', secrets: [] },
  { text: 'Bearer grant_abcdefghijklmnopqrstuvwxyz', secrets: [] },
  { text: '{"thinking":"Bearer x\\"y","sig":"s"}', secrets: [] },
  { text: 'the key site-key-4b9e17c2 twice site-key-4b9e17c2', secrets: ['site-key-4b9e17c2'] },
  { text: 'grant_grant_abcdefghijklmnopqrst', secrets: [] },
  { text: 'é Bearer\ttok é', secrets: [''] },
];

const emails: ({ spf?: string; dkim?: string; dmarc?: string } | null)[] = [
  null,
  {},
  { spf: 'pass', dkim: 'pass', dmarc: 'pass' },
  { spf: 'pass', dkim: 'pass', dmarc: 'fail' },
  { spf: 'fail', dkim: 'pass', dmarc: 'pass' },
  { spf: 'softfail', dmarc: 'pass' },
  { dkim: 'permerror', dmarc: 'pass' },
  { spf: 'neutral', dkim: 'none', dmarc: 'pass' },
  { spf: 'PASS', dkim: ' Pass ', dmarc: ' PASS ' },
  { dmarc: 'none' },
  { spf: 'temperror', dmarc: 'pass' },
];

function corpus() {
  return {
    comment:
      'The agent orchestration\'s and the inbound path\'s pure parts (D157, D158, D183, D193). Every want is computed by runtime/http/typescript/src/agents-parity.test.ts from the inputs here; AGENTS_PARITY_WRITE=1 bun test src/agents-parity.test.ts writes this file. The Rust runtime\'s orm::agents and orm::inbound read it.',
    defaultAutonomy: classes.map(cls => ({ class: cls, want: defaultAutonomy(cls) })),
    trustOf: [null, 'verified', 'entered', 'unverified', 'VERIFIED', ''].map(recorded => ({ recorded, want: trustOf(recorded) })),
    effectiveAutonomy: modes.flatMap(mode =>
      classes.flatMap(cls => trusts.map(trust => ({ mode, class: cls, trust, want: effectiveAutonomy(mode, cls, trust as 'verified' | 'entered' | null) })))
    ),
    narrowReaches: reaches.map(c => ({ ...c, want: narrowReaches(c.role, c.party) })),
    narrowPermissions: permissions.map(c => ({ ...c, want: narrowPermissions(c.agent, c.actor) })),
    paramsHash: hashes.map(c => ({ ...c, want: paramsHash(c.operation, c.params) })),
    bestPolicy: policies.map(c => {
      const best = bestPolicy(c.rows, c.operation, c.class);
      return { ...c, want: best === undefined ? null : c.rows.indexOf(best) };
    }),
    budgetCrossing: budgets.map(c => ({ ...c, ...budgetCrossing(c.before, c.after, c.limit ?? undefined, c.warnAt) })),
    redactText: redactions.map(c => ({ ...c, want: redactTokens(c.text, c.secrets) })),
    emailAuthenticated: emails.map(authentication => ({ authentication, want: emailAuthenticated(authentication ?? undefined) })),
  };
}

describe('the agents parity corpus', () => {
  test('agents_parity.json holds what the TypeScript runtime computes', () => {
    const want = `${JSON.stringify(corpus(), null, 2)}\n`;
    if (process.env.AGENTS_PARITY_WRITE === '1') {
      writeFileSync(CORPUS, want);
      return;
    }
    expect(readFileSync(CORPUS, 'utf8')).toBe(want);
  });
});
