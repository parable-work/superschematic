/*
A typed implementation of fixture-paged-api's catalog (D60), staged in the
generated package before it is type-checked (TestGeneratedPagedRouter), so
tsc holds a paged method to its generated signature: listItems takes
args.page, a PageRequest with its sort (D60, amended), beside its query
arguments, and not the sort as an argument of its own, and answers a
Page<ItemView>; listTags, which declares no argument, takes args.page
alone and answers a Page<string>. paged_runtime.test.ts serves it through
the generated router.

The catalog sorts its items by the page's sort, name or sku, either way,
ties broken by id in the same direction, each named in a cursor by the
sort field's value and its id; its tags are in their text's order. Each
list reads up to limit + 1 rows after the cursor's position under the
request's filters, and pageFromRows keeps limit of them and sets next to
the last one's key, which the router binds to the sort. A cursor that
decodes to no position of the order is refused with the runtime's 400, as
one that does not decode at all is.
*/
import { decodeCursor, lastPage, notFound, pageFromRows, refusedCursor, type PageSort } from '@superschematic/http-runtime';
import type { BasketView, ItemView } from '@schemas/fixture-paged-api-types/types';
import { parseIdentityUUID } from 'superscalar/scalars';
import type { CatalogImplementation, ListItemsArgs, ListTagsArgs } from './interfaces';

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

/** Where the page after an item cursor starts in the sorted catalog, or the 400 for a cursor that names no item. */
function startAfter(sorted: readonly ItemView[], cursor: string): number {
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
export const received: { operation: 'listItems' | 'listTags'; args: ListItemsArgs | ListTagsArgs }[] = [];

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
