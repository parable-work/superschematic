/*
A typed implementation of fixture-paged-api's catalog (D60), staged in the
generated package before it is type-checked (TestGeneratedPagedRouter), so
tsc holds a paged method to its generated signature: listItems takes
args.page, a PageRequest, beside its query arguments and answers a
Page<ItemView>, and listTags, which declares no argument, takes args.page
alone and answers a Page<string>. paged_runtime.test.ts serves it through
the generated router.

The catalog keeps its items in a fixed order, each named in a cursor by
its id, and its tags in their text's order. Each list reads up to
limit + 1 rows after the cursor's position under the request's filters,
and pageFromRows keeps limit of them and sets next to the last one's key.
A cursor that decodes to no position of the order is refused with the
runtime's 400, as one that does not decode at all is.
*/
import { decodeCursor, lastPage, notFound, pageFromRows, refusedCursor } from '@superschematic/http-runtime';
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

/** The catalog, in its order. */
export const items: readonly ItemView[] = [
  item('00000000-0000-4000-8000-000000000001', 'Anvil', 'anvil', true),
  item('00000000-0000-4000-8000-000000000002', 'Bucket', 'bucket', false),
  item('00000000-0000-4000-8000-000000000003', 'Chisel', 'chisel', true),
  item('00000000-0000-4000-8000-000000000004', 'Drill', 'drill', true),
  item('00000000-0000-4000-8000-000000000005', 'Easel', 'easel', false),
];

/** The tags, in their order. */
export const tags: readonly string[] = ['blue', 'green', 'red'];

/** An item's position in the catalog's order. */
interface ItemKey {
  readonly id: string;
}

/** Where the page after an item cursor starts in the catalog, or the 400 for a cursor that names no item. */
function startAfter(cursor: string): number {
  const key = decodeCursor<ItemKey>(cursor);
  const at = typeof key === 'object' && key !== null ? items.findIndex(row => row.id === key.id) : -1;
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
    const start = args.page.cursor === undefined ? 0 : startAfter(args.page.cursor);
    const rows = items
      .slice(start)
      .filter(row => (args.inStock === undefined || row.inStock === args.inStock) && (args.ids === undefined || args.ids.includes(row.id)))
      .slice(0, args.page.limit + 1);
    return pageFromRows(rows, args.page.limit, (row): ItemKey => ({ id: row.id }));
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
