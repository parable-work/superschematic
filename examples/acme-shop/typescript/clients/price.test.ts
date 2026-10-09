// The generated shop-common types package from TypeScript: every object type
// gets a parser that decodes and validates in one step.
import { expect, test } from 'bun:test';
import { Currency, parsePriceJson, validatePrice } from '@acme/shop-common-types';

test('parsePriceJson decodes a price', () => {
  const price = parsePriceJson('{"amountCents": 1999, "currency": "EUR"}');
  expect(price).toEqual({ amountCents: 1999, currency: Currency.EUR });
});

test('a value of the wrong JSON type is rejected', () => {
  expect(() => parsePriceJson('{"amountCents": "19.99", "currency": "EUR"}')).toThrow();
  expect(validatePrice({ amountCents: 1999, currency: 'YEN' as Currency })).toEqual({
    currency: [{ validator: 'enum', message: expect.any(String) }],
  });
});
