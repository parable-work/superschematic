// The implementation of the wishlist's second version, which replaces
// typescript/shop-wishlist/index.ts when the second version's schemas
// replace the first's (README.md, "The wishlist's second version"). It
// reads and writes quantity and never names note, so it runs on the
// database between the phases, where both columns are, and after the
// contract phase drops note. A row the first version kept has no quantity,
// which counts one.
import type { SqlDatabase } from '@superschematic/http-runtime/d1';
import type { Constructor, Deps, WishlistImplementation } from '@acme/shop-wishlist-api';
import type { WishlistEntry, WishlistView } from '@acme/shop-wishlist-types/types';

/** A row of wishlist_item, as SQLite and D1 answer it. */
interface ItemRow {
  sku: string;
  quantity: number | null;
  added_at: string;
}

/** Reads a shopper's wishlist, oldest first. */
async function wishlistOf(db: SqlDatabase, shopper: string): Promise<WishlistView> {
  const { results } = await db
    .prepare('SELECT sku, quantity, added_at FROM wishlist_item WHERE shopper = ?1 ORDER BY added_at, sku')
    .bind(shopper)
    .all<ItemRow>();
  return {
    shopper: shopper as WishlistView['shopper'],
    items: results.map(row => ({
      sku: row.sku as WishlistEntry['sku'],
      quantity: (row.quantity ?? 1) as WishlistEntry['quantity'],
      addedAt: new Date(row.added_at),
    })),
  };
}

/** Builds the wishlist from its Deps: its rows live in shop-wishlist-db. */
export const create: Constructor = deps => ({
  wishlist: wishlist(deps.db, deps.logger),
});

function wishlist(db: SqlDatabase, logger: Deps['logger']): WishlistImplementation {
  return {
    async getWishlist({ shopper }) {
      return wishlistOf(db, shopper);
    },
    async keepItem({ shopper, sku, input }) {
      await db
        .prepare(
          'INSERT INTO wishlist_item (shopper, sku, quantity) VALUES (?1, ?2, ?3) ON CONFLICT (shopper, sku) DO UPDATE SET quantity = excluded.quantity'
        )
        .bind(shopper, sku, input.quantity ?? 1)
        .run();
      logger.info('kept', { shopper, sku, quantity: input.quantity ?? 1 });
      return wishlistOf(db, shopper);
    },
    async dropItem({ shopper, sku }) {
      await db.prepare('DELETE FROM wishlist_item WHERE shopper = ?1 AND sku = ?2').bind(shopper, sku).run();
      return wishlistOf(db, shopper);
    },
  };
}
