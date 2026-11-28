import type { Principal } from './auth.js';
import { requestIdOf } from './envelope.js';
import type { Inbound } from './inbound.js';
import { INAPP, NotifierError, type Notifier, type RecipientRef } from './notify.js';
import { badRequest, forbidden, methodNotAllowed, notFound, problemResponse, unauthorized, type HttpProblem } from './problem.js';
import type { WebhookRequest } from './providers/channel.js';

/*
The notifier's routes (D153, D157, #702), which a generated server mounts for a
database that declares the notifier: each channel slot's relay callbacks,
what its people send back and how each message fared, and the in-app
inbox of the caller's own notices. A relay's callback carries no caller:
the channel adapter checks the relay's signature, and the core decides who
sent each message, a user's reply, a party's run, a party's timeline or
triage. The inbox is the caller's alone.
*/

/** What notifierRoutes serves. */
export interface NotifierRoutesOptions {
  readonly notifier: Notifier;
  /**
   * The inbound path (D157): every message a relay receives goes down it,
   * which hands a user's reply to the notifier. Absent, a message goes to
   * the notifier's reply alone.
   */
  readonly inbound?: Inbound;
  /** The API's channel slots, as its @providers names them: SMS, MAIL. Each has its relay's two callbacks. */
  readonly slots: readonly string[];
  /** Establishes the caller of an inbox route; null answers 401. */
  readonly authenticate: (request: Request) => Promise<Principal | null>;
  /** Where the relays' callbacks are: /webhooks unless set, each slot's under its path segment (slotSegment). */
  readonly webhooks?: string;
  /** Where the inbox is: /api/inbox unless set. */
  readonly inbox?: string;
}

/** The path segment a slot's callbacks are under: the slot in lower case, an underscore a hyphen. SMS is sms, CHAT_BRIDGE chat-bridge. */
export function slotSegment(slot: string): string {
  return slot.toLowerCase().replace(/_/gu, '-');
}

/** The refusal of a request, through the shared problem envelope, which carries its requestId and code. */
function problem(request: Request, refusal: HttpProblem): Response {
  return problemResponse(refusal, requestIdOf(request));
}

function json(value: unknown): Response {
  return new Response(JSON.stringify(value), { status: 200, headers: { 'content-type': 'application/json' } });
}

async function webhookOf(request: Request): Promise<WebhookRequest> {
  const headers: Record<string, string> = {};
  request.headers.forEach((value, name) => {
    headers[name] = value;
  });
  return { headers, body: await request.text() };
}

/** The recipient the caller is in the inbox: a user's, or none for any other principal. */
function recipientOf(principal: Principal): RecipientRef | undefined {
  return principal.kind === undefined || principal.kind === 'user' ? { kind: 'user', id: principal.subject } : undefined;
}

/**
 * The notifier's routes, as a fetch handler that answers null for a
 * request it does not serve:
 *
 * - POST {webhooks}/{slot}/inbound: what the slot's relay received, down
 *   the inbound path, or to the notifier's reply; answers the outcomes.
 * - POST {webhooks}/{slot}/status: the delivery statuses the relay posts;
 *   answers how many deliveries moved.
 * - GET {inbox}?state=&limit=: the caller's notices, newest first.
 * - POST {inbox}/{id}/read and /ack: marks one read, or acknowledges it,
 *   which stops its escalation.
 */
export function notifierRoutes(options: NotifierRoutesOptions): (request: Request) => Promise<Response | null> {
  const webhooks = (options.webhooks ?? '/webhooks').replace(/\/$/u, '');
  const inbox = (options.inbox ?? '/api/inbox').replace(/\/$/u, '');
  const bySegment = new Map(options.slots.map(slot => [slotSegment(slot), slot] as const));
  return async (request: Request) => {
    const url = new URL(request.url);
    const path = url.pathname;
    if (path.startsWith(`${webhooks}/`)) {
      const callback = /^\/([^/]+)\/(inbound|status)$/u.exec(path.slice(webhooks.length));
      const slot = callback ? bySegment.get(callback[1]!) : undefined;
      if (!callback || slot === undefined) return null;
      if (request.method !== 'POST') return problem(request, methodNotAllowed('a relay posts its callbacks'));
      try {
        const webhook = await webhookOf(request);
        if (callback[2] === 'status') return json({ moved: await options.notifier.statuses(slot, webhook) });
        const outcomes = options.inbound ? await options.inbound.inbound(slot, webhook) : await options.notifier.inbound(slot, webhook);
        return json(outcomes);
      } catch (error) {
        if (error instanceof NotifierError) return problem(request, badRequest(error.message));
        throw error;
      }
    }
    if (path !== inbox && !path.startsWith(`${inbox}/`)) return null;
    const principal = await options.authenticate(request);
    if (!principal) return problem(request, unauthorized());
    const recipient = recipientOf(principal);
    if (!recipient) return problem(request, forbidden("the inbox is a user's"));
    const rest = path.slice(inbox.length);
    try {
      if (rest === '' || rest === '/') {
        if (request.method !== 'GET') return problem(request, methodNotAllowed('the inbox is a GET'));
        const state = url.searchParams.getAll('state').flatMap(value => value.split(',')).filter(value => value !== '');
        const limit = url.searchParams.has('limit') ? Number(url.searchParams.get('limit')) : undefined;
        return json(await options.notifier.inbox(recipient, { ...(state.length > 0 ? { state } : {}), ...(limit !== undefined && Number.isInteger(limit) ? { limit } : {}) }));
      }
      const action = /^\/([^/]+)\/(read|ack)$/u.exec(rest);
      if (!action) return problem(request, notFound('no such inbox route'));
      if (request.method !== 'POST') return problem(request, methodNotAllowed(`${action[2]} is a POST`));
      const notice = decodeURIComponent(action[1]!);
      if (action[2] === 'read') await options.notifier.markRead(recipient, notice);
      else await options.notifier.acknowledge(recipient, notice, INAPP);
      return new Response(null, { status: 204 });
    } catch (error) {
      if (error instanceof NotifierError) return problem(request, notFound(error.message));
      throw error;
    }
  };
}
