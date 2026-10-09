// Calls shop-orders through the generated TypeScript SDK. go/clients_test.go
// runs it against the Go server and compares what it prints with the other
// languages' clients. Its arguments are the server's base URL and the
// shopper's bearer token, token-1 when absent, as the Rust server accepts
// it; the Go server takes a session token its sign-in issued.
import { ApiError, ShopOrdersSDK } from '@acme/shop-orders-sdk';
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

const baseUrl = process.argv[2];
const token = process.argv[3] ?? 'token-1';
const anonymous = new ShopOrdersSDK({ baseUrl });
const shopper = new ShopOrdersSDK({ baseUrl, auth: { token } });

const reviews = await anonymous.productReviews.listReviews(product);
console.log(`reviews: ${reviews.length}`);

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

console.log(`get a missing order: ${await status(() => shopper.order.getOrder(missingOrder))}`);
