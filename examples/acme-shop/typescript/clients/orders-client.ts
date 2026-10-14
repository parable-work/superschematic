// Calls shop-orders through the generated TypeScript SDK. go/clients_test.go
// runs it against the Go server and compares what it prints with the other
// languages' clients. Its arguments are the server's base URL and the
// shopper's bearer token, which shop-api's login issued: the Go and the
// Rust server both read its session from shop-db's users.
import { ApiError, ShopOrdersSDK } from '@acme/shop-orders-sdk';
import type { Page } from '@acme/shop-orders-sdk';
import { parseIdentityNameStrict, parseIdentityUUIDStrict } from 'superscalar/scalars';

// A scalar field's type is its brand (IdentityUUID, IdentityName), which a
// plain string does not satisfy: parse a value where it enters the program.
const product = parseIdentityUUIDStrict('00000000-0000-4000-8000-0000000000b1');
const missingOrder = parseIdentityUUIDStrict('00000000-0000-4000-8000-0000000000c1');

async function status(call: () => Promise<unknown>): Promise<number | string> {
  try {
    await call();
    return 'ok';
  } catch (error) {
    if (error instanceof ApiError) return error.statusCode;
    throw error;
  }
}

// Whether a page is the last, which its next cursor, null, says.
function lastPage(page: Page<unknown>): string {
  return page.next === null ? 'the last page' : 'more after it';
}

async function count(items: AsyncIterable<unknown>): Promise<number> {
  let n = 0;
  for await (const _ of items) n++;
  return n;
}

const baseUrl = process.argv[2];
const token = process.argv[3];
const anonymous = new ShopOrdersSDK({ baseUrl });
const shopper = new ShopOrdersSDK({ baseUrl, auth: { token } });

// A paged list answers one page; its All method yields every item, asking
// for each page with the next of the one before.
const products = await anonymous.productCatalog.listProducts({ limit: 2 });
console.log(`products: ${products.items.length}, ${lastPage(products)}`);
console.log(`every product: ${await count(anonymous.productCatalog.listProductsAll({ limit: 2 }))}`);

console.log(`get a missing product: ${await status(() => anonymous.productCatalog.getProduct(product))}`);

const reviews = await anonymous.productReviews.listReviews(product, { q: 'smoky' });
console.log(`reviews that say smoky: ${reviews.items.length}, ${lastPage(reviews)}`);

console.log(`write a review without a token: ${await status(() =>
  anonymous.productReviews.writeReview(product, { rating: 5, title: 'Lovely', body: 'Smoky and smooth.' })
)}`);

console.log(`order a product that does not exist: ${await status(() =>
  shopper.order.placeOrder({
    lines: [{ productId: product, quantity: 2 }],
    shippingAddress: {
      recipient: parseIdentityNameStrict('Ada Lovelace'),
      line1: "12 St James's Square",
      line2: null,
      city: 'London',
      postcode: 'SW1Y 4JH',
      country: 'GB',
      phone: null,
    },
  })
)}`);

const orders = await shopper.order.listOrders({ limit: 1 });
console.log(`orders: ${orders.items.length}, ${lastPage(orders)}`);
console.log(`every order: ${await count(shopper.order.listOrdersAll({ limit: 1 }))}`);

// e30 is the cursor of {}, a key that names no order.
console.log(`orders after a cursor the server never returned: ${await status(() =>
  shopper.order.listOrders({ cursor: 'e30' })
)}`);

console.log(`get a missing order: ${await status(() => shopper.order.getOrder(missingOrder))}`);
