// The implementation of the shop-cart API, which shop-stack's generated
// shop-cart server builds from its Deps (docs/stack-model.md, section
// 8.6). A cart is a row of shop-db's Checkout table in cart (D70, D103), a
// ticket a row of its Ticket table, and a shopper's own reviews and
// reorders rows of its other tables: this package reads and writes them
// all through Deps.orm, the database's generated repositories (D79), which
// act as the shopper the identity runtime authenticated and run each
// table's behaviors through the behaviors' core, with no SQL of its own. A
// cart is the caller's own, which this package checks. shop-orders starts
// the checkout, which holds what the cart asks, and takes payment for it;
// the pickup slots are what each CollectionPoint's Calendar has free.
import { HttpProblem, OperationResult, decodeCursor, encodeCursor, notFound, refusedCursor, type RequestContext } from '@superschematic/http-runtime';
import { NotFoundError, PageKeyError, enqueue } from '@superschematic/http-runtime/postgres';
import type {
  CartsImplementation,
  Constructor,
  Deps,
  DerivedAnswer,
  DeriversConstructor,
  MyReviewsImplementation,
  PickupSlotsImplementation,
  ReordersImplementation,
  TicketsImplementation,
} from '@acme/shop-cart-api';
import type { CartLine, CartView, IdentityUUID, MyReview, PickupSlot, TicketView } from '@acme/shop-cart-types/types';
import { TicketCategory, TicketPriority } from '@acme/shop-db-types';
import type { CheckoutRow, ReviewPageKey, ReviewRow, ReviewSortField, TicketRow } from '@acme/shop-db-types/orm';
import { orderPlacedQueue } from '@acme/shop-db-types/queues';

// A pickup slot is fifteen minutes, the CollectionPoint's grid.
const SLOT_MS = 15 * 60 * 1000;
const DAY_MS = 24 * 60 * 60 * 1000;

/** Builds the implementation of shop-cart from its dependencies. Its type is the generated Constructor. */
export const create: Constructor = deps => ({
  carts: carts(deps),
  myReviews: myReviews(deps),
  pickupSlots: pickupSlots(deps),
  reorders: reorders(deps),
  tickets: tickets(deps),
});

/** One of a checkout's items, as its Holds' listHolds answers. */
interface HoldItem {
  hold: string;
  item: number;
  id: string;
  quantity?: number;
  state: string;
  unitPrice?: number;
}

/** The caller's cart: another shopper's is the 404 of one never opened. */
async function callersCart(deps: Deps, ctx: RequestContext, id: IdentityUUID): Promise<CheckoutRow> {
  const shopper = callerId(ctx);
  const cart = await found(`Cart ${id}`, deps.orm.checkout.getOne(id));
  if (cart.customerId !== shopper) throw notFound(`Cart ${id} does not exist`);
  return cart;
}

/** A cart in cart: one shop-orders started can no longer change. */
function inCart(cart: CheckoutRow): void {
  if (cart.status !== 'cart') throw new HttpProblem(409, `the cart is ${cart.status}: start a new one`, { code: 'not_in_cart' });
}

/** The cart as its shopper sees it, each line with its Holds item's state and the price it was held at. */
async function view(deps: Deps, cart: CheckoutRow): Promise<CartView> {
  const listed = (await deps.orm.checkout.holds.listHolds(cart.id, {})) as { items: HoldItem[] };
  const held = new Map(listed.items.filter(item => item.hold === 'lines').map(item => [item.item, item] as const));
  const lines: CartLine[] = cart.lines.map((line, index) => ({
    productId: line.inventoryId,
    quantity: line.quantity,
    state: held.get(index)?.state ?? 'pending',
    unitPriceCents: held.get(index)?.unitPrice,
  }));
  return {
    id: cart.id,
    status: cart.status,
    lines,
    collectionPointId: cart.counterId ?? undefined,
    pickupAt: cart.pickupAt ?? undefined,
  };
}

function carts(deps: Deps): CartsImplementation {
  return {
    // A new, empty cart of the caller's.
    openCart: async (_args, ctx) => {
      const shopper = callerId(ctx);
      const cart = await deps.orm.checkout.createOne({ customerId: shopper, lines: [] });
      deps.logger.info('cart opened', { cart: cart.id, shopper });
      return new OperationResult(await view(deps, cart), 201);
    },
    getCart: async ({ id }, ctx) => view(deps, await callersCart(deps, ctx, id)),
    // Adds units of a product, to its line when the cart has one. A
    // product the warehouse never stocked has no Inventory: 422.
    addCartLine: async ({ id, input }, ctx) => {
      const cart = await callersCart(deps, ctx, id);
      inCart(cart);
      await known(deps.orm.inventory.getOne(input.productId), `the shop does not stock product ${input.productId}`, 'unknown_product');
      const lines = cart.lines.map(line => ({ ...line }));
      const line = lines.find(existing => existing.inventoryId === input.productId);
      if (line) line.quantity += input.quantity;
      else lines.push({ inventoryId: input.productId, quantity: input.quantity });
      return view(deps, await deps.orm.checkout.updateOne(cart.id, { lines }));
    },
    // Records the counter and the slot, which startCheckout holds. A
    // counter no row holds is 422.
    choosePickup: async ({ id, input }, ctx) => {
      const cart = await callersCart(deps, ctx, id);
      inCart(cart);
      await known(deps.orm.collectionPoint.getOne(input.collectionPointId), `no counter has id ${input.collectionPointId}`, 'unknown_counter');
      return view(deps, await deps.orm.checkout.updateOne(cart.id, { counterId: input.collectionPointId, pickupAt: input.startsAt }));
    },
  };
}

/** Runs a read of a row the request names, answering a key no live row holds as 422 with code. */
async function known<T>(call: Promise<T>, message: string, code: string): Promise<T> {
  try {
    return await call;
  } catch (error) {
    if (error instanceof NotFoundError) throw new HttpProblem(422, message, { code });
    throw error;
  }
}

function pickupSlots(deps: Deps): PickupSlotsImplementation {
  return {
    // Each counter's free fifteen-minute slots, by the Calendar's own
    // rules: its hours, closures, lead time, horizon and capacity.
    listPickupSlots: async ({ from, days }) => {
      const startMs = from.getTime();
      const free = await deps.orm.collectionPoint.calendar.findAvailability({
        startMs,
        endMs: startMs + (days ?? 1) * DAY_MS,
        durationMs: SLOT_MS,
        limit: 500,
      });
      return free.slots.map(
        (slot): PickupSlot => ({ collectionPointId: slot.id as IdentityUUID, startsAt: new Date(slot.startMs), endsAt: new Date(slot.endMs) })
      );
    },
  };
}

/** The caller's user id, the identity runtime's subject: its base62 Identity.UUID, as the repositories' rows hold ids. */
function callerId(ctx: RequestContext): IdentityUUID {
  const subject = ctx.principal?.subject;
  if (!subject) throw new HttpProblem(401, 'sign in first', { code: 'unauthorized' });
  return subject as IdentityUUID;
}

/** Runs a repository call, answering a key no live row holds as the API's 404. */
async function found<T>(what: string, call: Promise<T>): Promise<T> {
  try {
    return await call;
  } catch (error) {
    if (error instanceof NotFoundError) throw notFound(`${what} does not exist`);
    throw error;
  }
}

function myReview(row: ReviewRow): MyReview {
  return { id: row.id, productId: row.productId, rating: row.rating, title: row.title, body: row.body, createdAt: row.createdAt };
}

// A shopper's own reviews, rows of shop-db's review table, read and written
// through Deps.orm, the database's generated repositories (D79): a page by
// keyset with a search beside it, an upsert by the review's unique key, and
// a soft delete the repository stamps with the caller.
function myReviews(deps: Deps): MyReviewsImplementation {
  return {
    listMyReviews: async ({ q, page }, ctx) => {
      const author = callerId(ctx);
      // The cursor is the repository's key of the last review of the page
      // before, {"id": ...}, which the router has unbound from its sort.
      const after = page.cursor ? decodeCursor<ReviewPageKey>(page.cursor) : null;
      try {
        const reviews = await deps.orm.review.findPage(
          { authorId: { eq: author }, searchText: q ?? undefined },
          { sort: { field: (page.sort?.field ?? 'createdAt') as ReviewSortField, desc: page.sort?.desc }, after, limit: page.limit }
        );
        return { items: reviews.rows.map(myReview), next: reviews.next ? encodeCursor(reviews.next) : null };
      } catch (error) {
        if (error instanceof PageKeyError) throw refusedCursor();
        throw error;
      }
    },
    // One review per shopper and product, by its unique index: the first
    // write inserts it and the next changes it.
    writeMyReview: async ({ productId, input }, ctx) => {
      const author = callerId(ctx);
      await found(`Product ${productId}`, deps.orm.product.getOne(productId));
      const review = await deps.orm.as(ctx.principal).review.upsertOne({ productId, authorId: author, ...input }, ['product', 'author'], input);
      deps.logger.info('review written', { review: review.id, product: productId });
      return myReview(review);
    },
    // Another shopper's review is the 404 of one never written; the
    // repository records the caller as the one who deleted it.
    deleteMyReview: async ({ id }, ctx) => {
      const author = callerId(ctx);
      const review = await found(`Review ${id}`, deps.orm.review.getOne(id));
      if (review.authorId !== author) throw notFound(`Review ${id} does not exist`);
      await deps.orm.as(ctx.principal).review.deleteOne(id);
      return myReview(review);
    },
  };
}

// Places an order of the caller's again (D79): the order, its lines at the
// products' prices now, and its OrderPlaced message, which shop-orders' Go
// worker FulfilOrders handles, in one transaction of Deps.orm, so all of
// them commit or none.
function reorders(deps: Deps): ReordersImplementation {
  return {
    reorder: async ({ id }, ctx) => {
      const customer = callerId(ctx);
      const placed = await deps.orm.as(ctx.principal).transaction(async tx => {
        const previous = await found(`Order ${id}`, tx.order.getOne(id));
        if (previous.customerId !== customer) throw notFound(`Order ${id} does not exist`);
        const { rows: lines } = await tx.orderLine.findMany({ orderId: { eq: previous.id } }, { limit: 1000 });
        // The products, one read for every line.
        const products = await tx.product.getManyByIds(lines.map(line => line.productId));
        const priced = lines.map(line => {
          const product = products.get(line.productId);
          if (!product) throw new HttpProblem(409, `product ${line.productId} is no longer sold`, { code: 'unavailable' });
          return { productId: line.productId, quantity: line.quantity, unitPriceCents: product.priceCents };
        });
        const totalCents = priced.reduce((sum, line) => sum + line.quantity * line.unitPriceCents, 0);
        const order = await tx.order.createOne({ customerId: customer, placedAt: new Date(), shippingAddress: previous.shippingAddress, totalCents });
        await tx.orderLine.createMany(priced.map(line => ({ orderId: order.id, ...line })));
        await enqueue(tx, orderPlacedQueue, { orderId: order.id });
        return { order, lineCount: priced.length };
      });
      deps.logger.info('reordered', { order: placed.order.id, from: id });
      return new OperationResult({ orderId: placed.order.id, totalCents: placed.order.totalCents, lineCount: placed.lineCount }, 201);
    },
  };
}

function ticketView(ticket: TicketRow): TicketView {
  return {
    id: ticket.id,
    status: ticket.status,
    subject: ticket.subject,
    body: ticket.body ?? undefined,
    orderId: ticket.orderId ?? undefined,
    category: ticket.category ?? undefined,
    priority: ticket.priority ?? undefined,
    summary: ticket.summary ?? undefined,
    inReview: ticket.provenanceInReview ?? false,
  };
}

function tickets(deps: Deps): TicketsImplementation {
  return {
    // A new ticket of the caller's in new: shop-cart's deriver fills in its
    // category, priority and summary once it commits (D74).
    openTicket: async ({ input }, ctx) => {
      const shopper = callerId(ctx);
      const ticket = await deps.orm.ticket.createOne({ customerId: shopper, subject: input.subject, body: input.body ?? null, orderId: input.orderId ?? null });
      deps.logger.info('ticket opened', { ticket: ticket.id, shopper });
      return new OperationResult(ticketView(ticket), 201);
    },
    // Another shopper's ticket is the 404 of one never opened.
    getTicket: async ({ id }, ctx) => {
      const shopper = callerId(ctx);
      const ticket = await found(`Ticket ${id}`, deps.orm.ticket.getOne(id));
      if (ticket.customerId !== shopper) throw notFound(`Ticket ${id} does not exist`);
      return ticketView(ticket);
    },
  };
}

/**
 * The deriver TriageTickets' answers (D74, D146): keyword rules over the
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
