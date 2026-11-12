/*
A typed implementation of fixture-paged-api's catalog (D60), staged in the
generated package before it is type-checked (TestGeneratedPagedRouter), so
tsc holds a paged method to its generated signature: listItems takes
args.page, a PageRequest with its sort (D60, amended), beside its query
arguments, and not the sort as an argument of its own, and answers a
Page<ItemView>; listTags, which declares no argument, takes args.page
alone and answers a Page<string>; listParts takes args.filter, its
ListPartsFilter (D106), beside args.page, each member typed by its field's
shape and value, and answers a Page<PartView>. paged_runtime.test.ts
serves it through the generated router.

The catalog sorts its items by the page's sort, name or sku, either way,
ties broken by id in the same direction, each named in a cursor by the
sort field's value and its id; its tags are in their text's order. Each
list reads up to limit + 1 rows after the cursor's position under the
request's filters, and pageFromRows keeps limit of them and sets next to
the last one's key, which the router binds to the sort. A cursor that
decodes to no position of the order is refused with the runtime's 400, as
one that does not decode at all is.

The catalog's parts sort by name or quantity the same way, and listParts
keeps the ones args.filter's members all match, as a repository's filter
would: eq and in by equality, ne and notIn by its absence, gt, gte, lt
and lte by the field's order, contains by the text a name holds in any
case, and isNull by whether the field is null, which ne and notIn match
too and no other member does (D112).
*/
import {
  decodeCursor,
  lastPage,
  notFound,
  pageFromRows,
  refusedCursor,
  type EqualsFilter,
  type OrderedFilter,
  type PageSort,
  type TextFilter,
} from '@superschematic/http-runtime';
import { PartKind, type BasketView, type ItemView, type PartView } from '@schemas/fixture-paged-api-types/types';
import { parseIdentityUUID, type GenericDecimal, type TemporalDate } from 'superscalar/scalars';
import type { CatalogImplementation, ListItemsArgs, ListPartsArgs, ListPartsFilter, ListTagsArgs } from './interfaces';

const item = (id: string, name: string, sku: string, inStock: boolean): ItemView => ({
  // The UUID's canonical text, as the router decodes one.
  id: parseIdentityUUID(id)!,
  name,
  sku: sku as ItemView['sku'],
  inStock,
});

/** The catalog, in the order of its names. Two items share a sku, which their ids order. */
export const items: readonly ItemView[] = [
  item('00000000-0000-4000-8000-000000000001', 'Anvil', 'sku-2', true),
  item('00000000-0000-4000-8000-000000000002', 'Bucket', 'sku-1', false),
  item('00000000-0000-4000-8000-000000000003', 'Chisel', 'sku-5', true),
  item('00000000-0000-4000-8000-000000000004', 'Drill', 'sku-2', true),
  item('00000000-0000-4000-8000-000000000005', 'Easel', 'sku-4', false),
];

/** The tags, in their order. */
export const tags: readonly string[] = ['blue', 'green', 'red'];

/** The parts' suppliers. */
export const suppliers = [
  parseIdentityUUID('00000000-0000-4000-8000-000000000201')!,
  parseIdentityUUID('00000000-0000-4000-8000-000000000202')!,
] as const;

const part = (
  n: number,
  name: string,
  kind: PartKind,
  quantity: number,
  weight: number,
  price: string,
  discontinued: boolean,
  madeAt: string,
  retiredOn: string | null,
  supplierId: PartView['supplierId']
): PartView => ({
  id: parseIdentityUUID(`00000000-0000-4000-8000-00000000010${n}`)!,
  name,
  kind,
  quantity,
  weight,
  price: price as GenericDecimal,
  discontinued,
  madeAt: new Date(madeAt),
  retiredOn: retiredOn as TemporalDate | null,
  supplierId,
});

/** The parts, in the order of their names: a field of every kind a filter compares, two of them null on some. */
export const parts: readonly PartView[] = [
  part(1, 'Anchor bolt', PartKind.Bolt, 12, 0.5, '1.50', false, '2026-01-10T08:00:00Z', null, suppliers[0]),
  part(2, 'Cap nut', PartKind.Nut, 40, 0.02, '0.25', false, '2026-02-01T00:00:00Z', null, suppliers[0]),
  part(3, 'Eye bolt', PartKind.Bolt, 3, 1.25, '4.00', true, '2025-11-30T12:30:00Z', '2026-03-01', null),
  part(4, 'Flat washer', PartKind.Washer, 40, 0.01, '0.05', false, '2026-01-10T08:00:00Z', null, suppliers[1]),
  part(5, 'Hex nut', PartKind.Nut, 7, 0.03, '0.30', true, '2025-06-15T00:00:00Z', '2025-12-31', suppliers[1]),
  part(6, 'Wing nut', PartKind.Nut, 0, 0.04, '0.75', false, '2026-03-05T17:45:00Z', null, null),
];

/** An item's position in a sort's order: the sort field's value, and its id. */
interface ItemKey {
  readonly name?: string;
  readonly sku?: string;
  readonly id: string;
}

/** The catalog in a sort's order, ties broken by id in the same direction. */
export function sortedItems(sort: PageSort): ItemView[] {
  const value = (row: ItemView): string => (sort.field === 'sku' ? row.sku : row.name);
  const order = (a: ItemView, b: ItemView) => (value(a) < value(b) ? -1 : value(a) > value(b) ? 1 : a.id < b.id ? -1 : a.id > b.id ? 1 : 0);
  return [...items].sort((a, b) => (sort.desc ? -order(a, b) : order(a, b)));
}

/** An item's key under a sort. */
export function itemKey(row: ItemView, sort: PageSort): ItemKey {
  return sort.field === 'sku' ? { sku: row.sku, id: row.id } : { name: row.name, id: row.id };
}

/** A part's position in a sort's order: the sort field's value, and its id. */
interface PartKey {
  readonly name?: string;
  readonly quantity?: number;
  readonly id: string;
}

/** The parts in a sort's order, ties broken by id in the same direction. */
export function sortedParts(sort: PageSort): PartView[] {
  const value = (row: PartView): string | number => (sort.field === 'quantity' ? row.quantity : row.name);
  const order = (a: PartView, b: PartView) => (value(a) < value(b) ? -1 : value(a) > value(b) ? 1 : a.id < b.id ? -1 : a.id > b.id ? 1 : 0);
  return [...parts].sort((a, b) => (sort.desc ? -order(a, b) : order(a, b)));
}

/** A part's key under a sort. */
export function partKey(row: PartView, sort: PageSort): PartKey {
  return sort.field === 'quantity' ? { quantity: row.quantity, id: row.id } : { name: row.name, id: row.id };
}

/**
 * Whether a value passes an ordered field's filter: every member the
 * filter holds, by the field's order. A null value passes isNull: true,
 * and ne and notIn, which keep what eq and in leave out (D112), and no
 * other member.
 */
function ordered<T>(value: T | null, filter: OrderedFilter<T> | undefined, compare: (a: T, b: T) => number): boolean {
  if (filter === undefined) return true;
  if (filter.isNull !== undefined && filter.isNull !== (value === null)) return false;
  const holds = (bound: T | undefined, test: (order: number) => boolean) =>
    bound === undefined || (value !== null && test(compare(value, bound)));
  return (
    holds(filter.eq, order => order === 0) &&
    (filter.ne === undefined || value === null || compare(value, filter.ne) !== 0) &&
    (filter.in === undefined || (value !== null && filter.in.some(item => compare(value, item) === 0))) &&
    (filter.notIn === undefined || value === null || !filter.notIn.some(item => compare(value, item) === 0)) &&
    holds(filter.gt, order => order > 0) &&
    holds(filter.gte, order => order >= 0) &&
    holds(filter.lt, order => order < 0) &&
    holds(filter.lte, order => order <= 0)
  );
}

const byOrder = <T extends string | number>(a: T, b: T): number => (a < b ? -1 : a > b ? 1 : 0);

/** Whether a value passes a field's filter of equality alone. */
function equals<T>(value: T | null, filter: EqualsFilter<T> | undefined): boolean {
  return ordered(value, filter, (a, b) => (a === b ? 0 : 1));
}

/** Whether a text passes a text field's filter: contains is the text it holds, in any case. */
function text(value: string, filter: TextFilter<string> | undefined): boolean {
  if (filter === undefined) return true;
  const { contains, ...rest } = filter;
  return ordered(value, rest, byOrder) && (contains === undefined || value.toLowerCase().includes(contains.toLowerCase()));
}

/** Whether a part passes the list's filter: every member it holds. */
export function partMatches(row: PartView, filter: ListPartsFilter): boolean {
  return (
    equals(row.id, filter.id) &&
    text(row.name, filter.name) &&
    equals(row.kind, filter.kind) &&
    ordered(row.quantity, filter.quantity, byOrder) &&
    ordered(row.weight, filter.weight, byOrder) &&
    // A decimal is its text; the catalog compares its number.
    ordered(row.price, filter.price, (a, b) => byOrder(Number(a), Number(b))) &&
    equals(row.discontinued, filter.discontinued) &&
    ordered(row.madeAt, filter.madeAt, (a, b) => byOrder(a.getTime(), b.getTime())) &&
    // A date's text orders as its date.
    ordered(row.retiredOn ?? null, filter.retiredOn, byOrder) &&
    equals(row.supplierId ?? null, filter.supplierId)
  );
}

/** Where the page after a cursor starts in a sorted list of items or parts, or the 400 for a cursor that names none of them. */
function startAfter(sorted: readonly { readonly id: string }[], cursor: string): number {
  const key = decodeCursor<ItemKey>(cursor);
  const at = typeof key === 'object' && key !== null ? sorted.findIndex(row => row.id === key.id) : -1;
  if (at < 0) throw refusedCursor();
  return at + 1;
}

/** The tag a tag cursor names, or the 400 for one that names no tag position. */
function tagAfter(cursor: string): string {
  const key = decodeCursor<unknown>(cursor);
  if (typeof key !== 'string') throw refusedCursor();
  return key;
}

/** What each list received, for the assertions on the page the router read and that a refused request never reached it. */
export const received: {
  operation: 'listItems' | 'listTags' | 'listParts';
  args: ListItemsArgs | ListTagsArgs | ListPartsArgs;
}[] = [];

export const catalog: CatalogImplementation = {
  async listItems(args) {
    received.push({ operation: 'listItems', args });
    // The router fills the default sort in.
    const sort = args.page.sort!;
    const sorted = sortedItems(sort);
    const start = args.page.cursor === undefined ? 0 : startAfter(sorted, args.page.cursor);
    const rows = sorted
      .slice(start)
      .filter(row => (args.inStock === undefined || row.inStock === args.inStock) && (args.ids === undefined || args.ids.includes(row.id)))
      .slice(0, args.page.limit + 1);
    return pageFromRows(rows, args.page.limit, row => itemKey(row, sort));
  },
  async getItem(args) {
    const found = items.find(row => row.id === args.itemId);
    if (!found) throw notFound('no such item');
    return found;
  },
  async listParts(args) {
    received.push({ operation: 'listParts', args });
    const sort = args.page.sort!;
    const sorted = sortedParts(sort);
    const start = args.page.cursor === undefined ? 0 : startAfter(sorted, args.page.cursor);
    // The page's rows are the ones after the cursor the filter keeps: a
    // cursor carries no filter, so every page is asked with the same one.
    const rows = sorted
      .slice(start)
      .filter(row => partMatches(row, args.filter))
      .slice(0, args.page.limit + 1);
    return pageFromRows(rows, args.page.limit, row => partKey(row, sort));
  },
  async listTags(args) {
    received.push({ operation: 'listTags', args });
    const after = args.page.cursor === undefined ? undefined : tagAfter(args.page.cursor);
    const rows = tags.filter(tag => after === undefined || tag > after).slice(0, args.page.limit + 1);
    return pageFromRows(rows, args.page.limit, tag => tag);
  },
  async placeBasket(args): Promise<BasketView> {
    return { id: items[0]!.id, lines: args.input.lines };
  },
};

/** A catalog whose lists record the page the router read and answer an empty last page, without reading the cursor: the reader vectors' cursors name no position. */
export const recording: CatalogImplementation = {
  ...catalog,
  async listItems(args) {
    received.push({ operation: 'listItems', args });
    return lastPage([]);
  },
  async listTags(args) {
    received.push({ operation: 'listTags', args });
    return lastPage([]);
  },
};
