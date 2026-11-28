import type { Principal } from './auth.js';
import { covers } from './auth.js';
import type { AgentTool, AgentToolContext } from './agents.js';
import type { NoticeRow, RecipientRef, ReplyAction, ReplyInput, ReplyOutcome } from './notify.js';

/*
A reply interpreter other than keywords (#602, D153, D158). Keywords stay
the default: a reply whose first word is ack, undo or a verb a reply action
takes runs as before. One whose words name none, from a user's verified
identity, about a notice of a record, goes to an agent run when the
notifier names an interpreter (interpret: { agent }):

- The reply is a row of the replies table at once, outcome interpreting,
  its interpreter agent:<name>, and a run request in the same transaction:
  trigger reply, about the notice's record (the run's one target), acting
  for the reply's sender, its task the reply's text and its input the
  reply's row. The run row names the reply; the reply names the agent.
- The run's principal is the agent's role narrowed to the sender's
  permissions (D158), its reach the notice's record and what the role
  reaches from it. The reply tool (Notifier.replyTool) is how it answers:
  one verb of those the notice takes, ack or a reply action, run on that
  notice alone, held to the action's permission within the channel's
  trust as a keyword reply is, in the run's transaction as the run. The
  tool names no notice, so no call reaches another, and a reply is acted
  on once.
- The action is repeated back on the reply's channel with its undo, and
  undo <reference> undoes it as a keyword reply's is undone.
- A run that ends without acting, refused, paused or out of budget, ends
  the reply refused, and says so on the channel (ended).
*/

type Values = Record<string, any>;

interface RepositoryShape {
  findOne(filter: Values, options?: Values): Promise<Values | null>;
  createOne(input: Values): Promise<Values>;
  updateOne(id: unknown, update: Values): Promise<Values>;
}

/** What the notifier gives the interpreter: its tables and its own steps of a reply. */
export interface InterpreterDeps {
  /** The agent whose runs interpret, or none. */
  readonly agent?: string;
  /** The agent dispatcher's request (createNotifier's agents). */
  readonly request?: (tx: any, input: Readonly<Record<string, unknown>> & { readonly agent: string }) => Promise<void>;
  readonly orm: any;
  readonly inbox: (db: any) => RepositoryShape;
  readonly replies?: (db: any) => RepositoryShape;
  readonly identities?: (db: any) => RepositoryShape;
  readonly record: (db: any, values: Values) => Promise<string | undefined>;
  readonly echo: (input: ReplyInput, address: string, text: string, recipient?: string) => Promise<void>;
  readonly trustOf: (channel: string) => Promise<{ level: string; permissions: readonly string[] | null }>;
  readonly actionOf: (kind: string, verb: string) => ReplyAction | undefined;
  readonly acknowledgeIn: (tx: any, notice: NoticeRow, via: string) => Promise<void>;
  readonly acknowledgeDeliveries: (tx: any, notice: NoticeRow) => Promise<void>;
  readonly replyAllows: (held: readonly string[], cap: readonly string[] | null, need: string) => boolean;
  readonly narrowedPermissions: (held: readonly string[], cap: readonly string[] | null) => string[];
  readonly sameId: (id: unknown) => string;
  readonly now: () => Date;
}

/** The reference a free-text reply names anywhere in its words, as #142. */
export function referenceInText(text: string): number | undefined {
  const m = /(?:^|\s)#(\d+)(?=\s|[.,;:!?]|$)/u.exec(text);
  return m ? Number(m[1]) : undefined;
}

/** A refusal the reply tool answers with, which the run records as the call's outcome. */
class InterpretError extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = 'InterpretError';
    this.status = status;
  }
}

/** The interpreter of a notifier (createNotifier makes it). */
export interface ReplyInterpreter {
  /** Whether a reply that no keyword reads, from identity to recipient (about notice, when it is known), goes to an agent run: a notice of a record. */
  takes(identity: { readonly trust: string }, recipient: RecipientRef, notice?: NoticeRow): boolean;
  /** Records the reply as interpreting and requests its run, in one transaction. */
  hand(input: { readonly reply: ReplyInput; readonly address: string; readonly recipient: RecipientRef; readonly withIdentity: Values; readonly notice: NoticeRow; readonly text: string }): Promise<ReplyOutcome>;
  /** The tool a run answers its reply with. */
  readonly tool: AgentTool;
  /** A reply run that ended: its reply, still interpreting, ends refused, and its sender is told. */
  ended(input: { readonly reply?: unknown; readonly outcome: string; readonly detail?: string | null }): Promise<void>;
}

/** Makes a notifier's reply interpreter over deps. */
export function createReplyInterpreter(deps: InterpreterDeps): ReplyInterpreter {
  const replyInputOf = (row: Values): ReplyInput => ({ channel: String(row.channelKind), ...(row.slot ? { slot: String(row.slot) } : {}), from: String(row.address), body: String(row.body ?? '') });

  const tell = async (row: Values, text: string): Promise<void> => {
    await deps.echo(replyInputOf(row), String(row.address), text, row.principal ? String(row.principal) : undefined);
  };

  /**
   * Ends row refused in the run's transaction and tells its sender why: the
   * call answers the refusal as its result, so the transaction, which on
   * SQLite is the connection's, commits it.
   */
  const refuse = async (db: any, row: Values, detail: string): Promise<{ outcome: 'refused'; detail: string }> => {
    await deps.replies!(db).updateOne(row.id, { outcome: 'refused', detail });
    await tell(row, detail);
    return { outcome: 'refused', detail };
  };

  const tool: AgentTool = {
    description:
      "Answers the reply this run interprets with one verb its notice takes: ack, or one of the verbs the task lists. It runs on that notice alone, as the reply's sender, and is repeated back on the reply's channel with its undo. A reply is answered once.",
    inputSchema: {
      type: 'object',
      required: ['verb'],
      properties: {
        verb: { type: 'string', description: 'ack, or a verb the notice takes.' },
        args: { type: 'array', items: { type: 'string' }, description: 'The words the verb takes after it.' },
      },
    },
    class: 'internalWrite',
    async run(ctx: AgentToolContext): Promise<unknown> {
      if (!deps.replies) throw new InterpretError(400, 'the database keeps no replies');
      const input = (ctx.run.input as Values | null) ?? {};
      const row = typeof input.reply === 'string' ? await deps.replies(deps.orm).findOne({ id: { eq: input.reply } }) : null;
      if (!row) throw new InterpretError(404, 'the run interprets no reply');
      if (row.outcome !== 'interpreting') throw new InterpretError(409, 'the reply was answered already');
      // The run acts for the reply's sender and for no one else.
      const actingFor = ctx.principal.actingFor;
      if (!actingFor || actingFor.kind !== 'user' || deps.sameId(actingFor.subject) !== deps.sameId(row.principal)) {
        throw new InterpretError(403, "the run does not act for the reply's sender");
      }
      const notice = (await deps.inbox(deps.orm).findOne({ id: { eq: row.notice } })) as NoticeRow | null;
      if (!notice) return refuse(ctx.tx, row, 'The notice is gone.');
      const verb = String(ctx.args.verb ?? '').trim().toLowerCase();
      const args = Array.isArray(ctx.args.args) ? (ctx.args.args as unknown[]).map(String) : [];
      const channel = String(row.channelKind);
      const trust = await deps.trustOf(channel);
      const identity = row.identity && deps.identities ? await deps.identities(deps.orm).findOne({ id: { eq: row.identity } }) : null;
      // As a keyword reply is: an identity entered by hand acknowledges and runs nothing else.
      const cap = identity?.trust === 'entered' ? [] : trust.permissions;
      // The run's permissions are the agent's role narrowed to the sender's (D158).
      const held = ctx.principal.permissions;
      const principal: Principal = { ...ctx.principal, permissions: deps.narrowedPermissions(held, cap) };
      const tx = ctx.tx;
      if (verb === 'ack' || verb === 'acknowledge') {
        if (notice.state === 'acknowledged' || notice.state === 'done') return refuse(tx, row, `${notice.reference} is acknowledged already.`);
        await deps.acknowledgeIn(tx, notice, channel);
        await deps.replies(tx).updateOne(row.id, { verb: 'ack', outcome: 'done', undo: { previous: notice.state } });
        const said = `Acknowledged ${notice.reference}: ${notice.title}. Reply UNDO ${notice.reference} to undo.`;
        await tell(row, said);
        return { verb: 'ack', reference: notice.reference, echo: said };
      }
      const action = deps.actionOf(notice.eventKind, verb);
      if (!action) return refuse(tx, row, `"${verb}" is no reply ${notice.reference} takes.`);
      if (action.permission && !deps.replyAllows(held, cap, action.permission)) {
        return refuse(
          tx,
          row,
          held.some(h => covers(h, action.permission!)) ? `A reply by ${channel} may not ${verb}: its trust is ${identity?.trust === 'entered' ? 'entered by hand' : trust.level}. Use the app.` : `You may not ${verb} ${notice.reference}.`
        );
      }
      const data = await action.run({ tx, notice, principal, args, channel });
      await deps.inbox(tx).updateOne(notice.id, { state: 'done', acknowledgedAt: notice.acknowledgedAt ?? deps.now(), acknowledgedVia: channel });
      await deps.acknowledgeDeliveries(tx, notice);
      await deps.replies(tx).updateOne(row.id, { verb, outcome: 'done', undo: action.undo ? { data: data ?? null, previous: notice.state } : null });
      const said = `${action.describe ?? `Done: ${verb}`} ${notice.reference}: ${notice.title}.${action.undo ? ` Reply UNDO ${notice.reference} to undo.` : ' This cannot be undone here.'}`;
      await tell(row, said);
      return { verb, reference: notice.reference, echo: said };
    },
  };

  return {
    takes(identity, recipient, notice) {
      if (!deps.agent || !deps.request || !deps.replies || identity.trust !== 'verified' || recipient.kind !== 'user') return false;
      return notice === undefined || Boolean(notice.subjectTable && notice.subjectKey);
    },
    async hand({ reply, address, recipient, withIdentity, notice, text }) {
      let id: string | undefined;
      await deps.orm.transaction(async (tx: any) => {
        id = await deps.record(tx, { ...withIdentity, interpreter: `agent:${deps.agent}`, notice: notice.id, reference: notice.reference, outcome: 'interpreting' });
        await deps.request!(tx, {
          agent: deps.agent!,
          trigger: 'reply',
          subjectTable: String(notice.subjectTable),
          subjectKey: String(notice.subjectKey),
          requestedBy: { kind: recipient.kind, subject: recipient.id },
          actingFor: { kind: recipient.kind, subject: recipient.id },
          input: { task: text, reply: id, notice: notice.id, reference: notice.reference, channel: reply.channel },
        });
      });
      return { outcome: 'interpreting', ...(id ? { reply: id } : {}), notice: notice.id, principal: recipient };
    },
    tool,
    async ended({ reply, outcome, detail }) {
      if (!deps.replies || typeof reply !== 'string') return;
      const row = await deps.replies(deps.orm).findOne({ id: { eq: reply } });
      if (!row || row.outcome !== 'interpreting') return;
      const why = outcome === 'done' ? 'the interpreter did not act on it' : `the interpreter's run ended ${outcome}${detail ? `: ${detail}` : ''}`;
      await deps.replies(deps.orm).updateOne(row.id, { outcome: 'refused', detail: why });
      const notice = row.notice ? ((await deps.inbox(deps.orm).findOne({ id: { eq: row.notice } })) as NoticeRow | null) : null;
      await tell(row, `Your reply was not acted on${notice ? ` (${notice.reference})` : ''}. Reply with a word it takes, or use the app.`);
    },
  };
}
