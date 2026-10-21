// The implementation of the shop-wishlist API, which wishlist-stack's
// generated server builds from its Deps (docs/stack-model.md, sections 8.6
// and 18.6). shop-wishlist-db lists sqlite first, so Deps.db is a
// SqlDatabase, D1's interface: a SQLite file under stack dev, the D1
// database's binding on Cloudflare Workers, and the same statements on
// both. Placeholders are ?N, which D1 and SQLite both read; D1 has no
// interactive transaction, so what must be atomic is one batch.
import type { SqlDatabase } from '@superschematic/http-runtime/d1';
import type { Constructor, Deps, WishlistImplementation } from '@acme/shop-wishlist-api';
import type { WishlistEntry, WishlistView } from '@acme/shop-wishlist-types/types';

/** A row of wishlist_item, as SQLite and D1 answer it. */
interface ItemRow {
  sku: string;
  note: string | null;
  added_at: string;
}

/** Reads a shopper's wishlist, oldest first. */
async function wishlistOf(db: SqlDatabase, shopper: string): Promise<WishlistView> {
  const { results } = await db
    .prepare('SELECT sku, note, added_at FROM wishlist_item WHERE shopper = ?1 ORDER BY added_at, sku')
    .bind(shopper)
    .all<ItemRow>();
  return {
    shopper: shopper as WishlistView['shopper'],
    items: results.map(row => ({
      sku: row.sku as WishlistEntry['sku'],
      note: row.note,
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
      // One statement keeps the product, or changes its note when the
      // shopper keeps it already.
      await db
        .prepare(
          'INSERT INTO wishlist_item (shopper, sku, note) VALUES (?1, ?2, ?3) ON CONFLICT (shopper, sku) DO UPDATE SET note = excluded.note'
        )
        .bind(shopper, sku, input.note ?? null)
        .run();
      logger.info('kept', { shopper, sku });
      return wishlistOf(db, shopper);
    },
    async dropItem({ shopper, sku }) {
      await db.prepare('DELETE FROM wishlist_item WHERE shopper = ?1 AND sku = ?2').bind(shopper, sku).run();
      return wishlistOf(db, shopper);
    },
  };
}
