// The same feed item as go/types_test.go, python/types_test.py and
// rust/src/types_test.rs, decoded with the generated TypeScript types.
import { expect, test } from 'bun:test';
import { parseFeedItemJson, parseFeedItemJsonNonStrict, parsePriceJsonNonStrict } from '@acme/shop-common-types';

const feedItem = {
  sku: 'darjeeling-first-flush',
  name: 'Darjeeling first flush',
  price: { amountCents: 1450, currency: 'GBP' },
  tags: ['black', 'india'],
  attributes: { origin: 'Darjeeling', harvest: '2026 spring' },
};

test('a feed item decodes, map and all', () => {
  const item = parseFeedItemJson(JSON.stringify(feedItem));
  expect(item.price.amountCents).toBe(1450);
  expect(item.attributes.origin).toBe('Darjeeling');
});

test('@strictJSON refuses an undeclared key, even when decoding non-strictly', () => {
  expect(() => parseFeedItemJsonNonStrict(JSON.stringify({ ...feedItem, colour: 'amber' }))).toThrow();
  // Price is not @strictJSON: its non-strict parser skips the key.
  expect(parsePriceJsonNonStrict('{"amountCents": 1450, "currency": "GBP", "note": "sale"}').amountCents).toBe(1450);
});
