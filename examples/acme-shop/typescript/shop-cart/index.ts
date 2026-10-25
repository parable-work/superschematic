// The implementation of the shop-cart API, which shop-stack's generated
// shop-cart server builds from its Deps (docs/stack-model.md, section
// 8.6). A cart is shop-db's Checkout in cart (D70), which the engine keeps:
// this package calls the engine in-process, through the Background its
// Deps holds, as shop-cart for the shopper the identity runtime
// authenticated, the actor each change records. shop-db's engine policy
// lets shop-cart read and write checkouts and read the stock and the
// collection points; the engine answers for a type, not an instance, so
// this package checks a cart is the caller's own. shop-orders starts the
// checkout, which holds what the cart asks, and takes payment for it.
import { randomUUID } from 'node:crypto';

import { normalizeIdentityUUIDStrict } from 'superscalar';
import { HttpProblem, OperationResult, notFound, type RequestContext } from '@superschematic/http-runtime';
import { BehaviorVetoError, EngineError, type Principal } from '@superschematic/engine';
import type { AsyncEngine } from '@superschematic/engine/server';
import type { CartsImplementation, Constructor, Deps, DerivedAnswer, DeriversConstructor, PickupSlotsImplementation, TicketsImplementation } from '@acme/shop-cart-api';
import type { CartLine, CartView, IdentityUUID, PickupSlot, TicketView } from '@acme/shop-cart-types/types';
import { TicketCategory, TicketPriority } from '@acme/shop-db-types';

// The engine namespace shop-db's engine types live in, and the deployable
// shop-cart's calls name to its policy (D37).
const NAMESPACE = 'shop-db';
const SERVICE = 'shop-cart';
const inNamespace = { namespace: NAMESPACE };

// An engine id is a UUID's canonical text, as Identity.UUID writes it:
// the engine keeps a checkout's customerId and its lines' inventoryId so,
// and an Inventory's id is its product's.
const engineId = (id: string): IdentityUUID => normalizeIdentityUUIDStrict(id);

// A pickup slot is fifteen minutes, the CollectionPoint's grid.
const SLOT_MS = 15 * 60 * 1000;
const DAY_MS = 24 * 60 * 60 * 1000;

/** Builds the implementation of shop-cart from its dependencies. Its type is the generated Constructor. */
export const create: Constructor = deps => ({
  carts: carts(deps),
  pickupSlots: pickupSlots(deps),
  tickets: tickets(deps),
});

/** The shopper's call through shop-cart: the shopper is the actor, shop-cart the service. */
function shopper(ctx: RequestContext): Principal {
  const subject = ctx.principal?.subject;
  if (!subject) throw new HttpProblem(401, 'sign in to use a cart', { code: 'unauthorized' });
  return { subject: engineId(subject), permissions: [], service: { deployable: SERVICE, serves: [SERVICE], subject: SERVICE, standsIn: false } };
}

/** shop-cart itself, for what no shopper asks: the pickup slots anyone reads. */
const self: Principal = { subject: SERVICE, permissions: [], service: { deployable: SERVICE, serves: [SERVICE], subject: SERVICE, standsIn: false } };

/** A Checkout as the engine reads it. */
interface CheckoutRecord {
  id: IdentityUUID;
  data: { customerId: string; lines: Array<{ inventoryId: IdentityUUID; quantity: number }>; pickupAt?: string };
  behaviors: {
    Workflow?: { status: string };
    Holds?: { items?: Array<{ hold: string; item: number; id: string; quantity?: number; state: string; unitPrice?: number }> };
    Links?: { targets?: { counter?: { id: IdentityUUID } } };
  };
}

/** A Checkout the engine answered with. */
function checkout(record: unknown): CheckoutRecord {
  return record as CheckoutRecord;
}

/** The engine, once it opens; 503 until then. */
async function engineOf(deps: Deps): Promise<AsyncEngine> {
  try {
    return await deps.engine.wait(AbortSignal.timeout(10_000));
  } catch {
    throw new HttpProblem(503, 'the carts are not open yet', { code: 'unavailable' });
  }
}

/**
 * Runs an engine call, answering a refusal as the API's: a behavior's veto
 * 409 with its code and details, a refused argument 400, a missing
 * instance 404, a busy engine 503.
 */
async function engineCall<T>(deps: Deps, fn: (engine: AsyncEngine) => Promise<T>): Promise<T> {
  const engine = await engineOf(deps);
  try {
    return await fn(engine);
  } catch (err) {
    if (err instanceof BehaviorVetoError) {
      throw new HttpProblem(409, err.message, { code: err.vetoCode ?? 'vetoed', details: err.vetoDetails, cause: err });
    }
    if (err instanceof EngineError) {
      const status = { invalid_argument: 400, invalid_instance: 400, not_found: 404, conflict: 409, busy: 503, unavailable: 503 }[err.code as string];
      if (status) throw new HttpProblem(status, err.message, { code: err.code, cause: err });
    }
    throw err;
  }
}

/** The caller's cart: another shopper's is the 404 of one never opened. */
async function callersCart(deps: Deps, ctx: RequestContext, id: string): Promise<{ record: CheckoutRecord; principal: Principal }> {
  const principal = shopper(ctx);
  const found = await engineCall(deps, engine => engine.instances.get(principal, 'Checkout', engineId(id), inNamespace));
  const record = found ? checkout(found) : undefined;
  if (!record || record.data.customerId !== principal.subject) throw notFound(`Cart ${id} does not exist`);
  return { record, principal };
}

/** A cart in cart: one shop-orders started can no longer change. */
function inCart(record: CheckoutRecord): void {
  const status = record.behaviors.Workflow?.status;
  if (status !== 'cart') throw new HttpProblem(409, `the cart is ${status}: start a new one`, { code: 'not_in_cart' });
}

function view(record: CheckoutRecord): CartView {
  const held = new Map(
    (record.behaviors.Holds?.items ?? []).filter(item => item.hold === 'lines').map(item => [item.item, item] as const)
  );
  const lines: CartLine[] = record.data.lines.map((line, index) => ({
    productId: line.inventoryId,
    quantity: line.quantity,
    state: held.get(index)?.state ?? 'pending',
    unitPriceCents: held.get(index)?.unitPrice,
  }));
  return {
    id: record.id,
    status: record.behaviors.Workflow?.status ?? 'cart',
    lines,
    collectionPointId: record.behaviors.Links?.targets?.counter?.id,
    pickupAt: record.data.pickupAt !== undefined ? new Date(record.data.pickupAt) : undefined,
  };
}

function carts(deps: Deps): CartsImplementation {
  return {
    // A new, empty cart of the caller's, whose id the engine's Checkout
    // takes.
    openCart: async (_args, ctx) => {
      const principal = shopper(ctx);
      const record = checkout(
        await engineCall(deps, engine =>
          engine.instances.create(principal, 'Checkout', { customerId: principal.subject, lines: [] }, { ...inNamespace, id: engineId(randomUUID()) })
        )
      );
      deps.logger.info('cart opened', { cart: record.id, shopper: principal.subject });
      return new OperationResult(view(record), 201);
    },
    getCart: async ({ id }, ctx) => view((await callersCart(deps, ctx, id)).record),
    // Adds units of a product, to its line when the cart has one. A
    // product the warehouse never stocked has no Inventory: 422.
    addCartLine: async ({ id, input }, ctx) => {
      const { record, principal } = await callersCart(deps, ctx, id);
      inCart(record);
      const product = engineId(input.productId);
      const inventory = await engineCall(deps, engine => engine.instances.get(principal, 'Inventory', product, inNamespace));
      if (!inventory) throw new HttpProblem(422, `the shop does not stock product ${input.productId}`, { code: 'unknown_product' });
      const lines = record.data.lines.map(line => ({ ...line }));
      const line = lines.find(existing => existing.inventoryId === product);
      if (line) line.quantity += input.quantity;
      else lines.push({ inventoryId: product, quantity: input.quantity });
      return view(checkout(await engineCall(deps, engine => engine.instances.update(principal, 'Checkout', record.id, { lines }, inNamespace))));
    },
    // Links the counter and records the slot, which startCheckout holds.
    choosePickup: async ({ id, input }, ctx) => {
      const { record, principal } = await callersCart(deps, ctx, id);
      inCart(record);
      if (record.behaviors.Links?.targets?.counter !== undefined) {
        await engineCall(deps, engine => engine.instances.invoke(principal, 'Checkout', record.id, 'unlink', { name: 'counter' }, inNamespace));
      }
      await engineCall(deps, engine =>
        engine.instances.invoke(principal, 'Checkout', record.id, 'link', { name: 'counter', id: engineId(input.collectionPointId) }, inNamespace)
      );
      return view(
        checkout(await engineCall(deps, engine => engine.instances.update(principal, 'Checkout', record.id, { pickupAt: input.startsAt.toISOString() }, inNamespace)))
      );
    },
  };
}

function pickupSlots(deps: Deps): PickupSlotsImplementation {
  return {
    // Each counter's free fifteen-minute slots, by the Calendar's own
    // rules: its hours, closures, lead time, horizon and capacity.
    listPickupSlots: async ({ from, days }) => {
      const startMs = from.getTime();
      const found = (await engineCall(deps, engine =>
        engine.instances.invokeSchema(
          self,
          'CollectionPoint',
          'findAvailability',
          { startMs, endMs: startMs + (days ?? 1) * DAY_MS, durationMs: SLOT_MS, limit: 500 },
          inNamespace
        )
      )) as { slots: Array<{ id: IdentityUUID; startMs: number; endMs: number }> };
      return found.slots.map(
        (slot): PickupSlot => ({ collectionPointId: slot.id, startsAt: new Date(slot.startMs), endsAt: new Date(slot.endMs) })
      );
    },
  };
}

/** A Ticket as the engine reads it. */
interface TicketRecord {
  id: IdentityUUID;
  data: { customerId: string; subject: string; body?: string; orderId?: string; category?: string; priority?: string; summary?: string };
  behaviors: { Workflow?: { status: string }; Provenance?: { inReview?: boolean } };
}

function ticketView(found: unknown): TicketView {
  const record = found as unknown as TicketRecord;
  return {
    id: record.id,
    status: record.behaviors.Workflow?.status ?? 'new',
    subject: record.data.subject,
    body: record.data.body,
    orderId: record.data.orderId,
    category: record.data.category,
    priority: record.data.priority,
    summary: record.data.summary,
    inReview: record.behaviors.Provenance?.inReview ?? false,
  };
}

function tickets(deps: Deps): TicketsImplementation {
  return {
    // A new ticket of the caller's in new: shop-cart's deriver fills in its
    // category, priority and summary once it commits (D74).
    openTicket: async ({ input }, ctx) => {
      const principal = shopper(ctx);
      const data: Record<string, unknown> = { customerId: principal.subject, subject: input.subject };
      if (input.body != null) data.body = input.body;
      if (input.orderId != null) data.orderId = input.orderId;
      const record = await engineCall(deps, engine =>
        engine.instances.create(principal, 'Ticket', data, { ...inNamespace, id: engineId(randomUUID()) })
      );
      deps.logger.info('ticket opened', { ticket: (record as unknown as TicketRecord).id, shopper: principal.subject });
      return new OperationResult(ticketView(record), 201);
    },
    getTicket: async ({ id }, ctx) => {
      const principal = shopper(ctx);
      const found = await engineCall(deps, engine => engine.instances.get(principal, 'Ticket', engineId(id), inNamespace));
      if (!found || (found as unknown as TicketRecord).data.customerId !== principal.subject) throw notFound(`Ticket ${id} does not exist`);
      return ticketView(found);
    },
  };
}

/**
 * The deriver TriageTickets' answers (D74): keyword rules over the
 * ticket's subject and body, which stand in for a model so stack dev and
 * CI need no provider. A rule that matches is sure (0.9); a ticket no rule
 * matches is "other" at 0.5, which Provenance holds in review for support
 * staff. A project calls its model here, mapping item.model ("standard",
 * "simple") to a provider's model and holding the answer to item.output.
 */
export const createDerivers: DeriversConstructor = () => ({
  triageTickets: {
    async ticketCategory(item) {
      const text = words(item.inputs.subject, item.inputs.body);
      const rules: Array<[RegExp, TicketCategory]> = [
        [/\b(refund|charge[d]?|card|payment|paid|invoice)\b/, TicketCategory.Payments],
        [/\b(deliver(y|ed)?|courier|pickup|collect(ion)?|late|arrive[d]?)\b/, TicketCategory.Delivery],
        [/\b(order|cancel|cart|checkout)\b/, TicketCategory.Orders],
        [/\b(broken|faulty|size|colour|color|product|item)\b/, TicketCategory.Product],
      ];
      const hit = rules.find(([pattern]) => pattern.test(text));
      return hit ? sure(hit[1]) : { value: TicketCategory.Other, confidence: 0.5 };
    },
    async ticketPriority(item) {
      const text = words(item.inputs.subject, item.inputs.body);
      if (/\b(urgent|asap|today|immediately|wrong (card|amount)|charged twice)\b/.test(text)) return sure(TicketPriority.Urgent);
      if (/\b(when you can|no rush|question|wondering)\b/.test(text)) return sure(TicketPriority.Low);
      return sure(TicketPriority.Normal);
    },
    async ticketSummary(item) {
      const body = (item.inputs.body ?? '').trim();
      const first = body.split(/(?<=[.!?])\s/)[0] ?? body;
      return { value: first.length > 200 ? `${first.slice(0, 197)}...` : first, confidence: 0.9 };
    },
  },
});

/** An answer the rules are sure of. */
function sure<T>(value: T): DerivedAnswer<T> {
  return { value, confidence: 0.9 };
}

/** The ticket's text, lowercased, for the rules. */
function words(...parts: Array<string | null | undefined>): string {
  return parts.filter(part => part != null).join(' ').toLowerCase();
}
