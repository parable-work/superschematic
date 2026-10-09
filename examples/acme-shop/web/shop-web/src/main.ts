// The shop's static site (docs/stack-model.md, section 8.10): it lists
// shop-api's products in stock from the browser. One build serves every
// environment: loadApis() reads shop-api's public address from the config
// the environment serves at /__superschematic/config.json, and shop-api
// answers CORS for this site's origin, which its CORS field lists. The
// browser carries the shopper's session token itself.
//
// It calls shop-api with fetch rather than the generated SDK: a browser
// bundle of the SDK takes superscalar's Node backend today (D55, amended).
import { loadApis, type ApiEndpoint } from '../config.generated';

interface ProductView {
  readonly id: string;
  readonly name: string;
  readonly priceCents: number;
}

const form = document.getElementById('sign-in') as HTMLFormElement | null;
const list = document.getElementById('products');
const status = document.getElementById('status');

/** Lists the products in stock, as the shopper whose session token is token. */
async function listProducts(api: ApiEndpoint, token: string): Promise<ProductView[]> {
  const response = await fetch(`${api.baseUrl}/api/products?inStock=true`, {
    headers: { Accept: 'application/json', Authorization: `Bearer ${token}` },
  });
  if (!response.ok) throw new Error(`shop-api answered ${response.status}`);
  const body = (await response.json()) as { data: ProductView[] };
  return body.data;
}

function show(products: readonly ProductView[]): void {
  list?.replaceChildren(
    ...products.map(product => {
      const item = document.createElement('li');
      item.textContent = `${product.name}: ${(product.priceCents / 100).toFixed(2)}`;
      return item;
    })
  );
}

function say(text: string): void {
  if (status) status.textContent = text;
}

loadApis().then(
  ({ shopApi }) => {
    say(`shop-api is at ${shopApi.baseUrl}; enter a session token`);
    form?.addEventListener('submit', event => {
      event.preventDefault();
      const token = String(new FormData(form).get('token') ?? '');
      listProducts(shopApi, token).then(
        products => {
          show(products);
          say(`${products.length} products in stock`);
        },
        (error: unknown) => say(error instanceof Error ? error.message : String(error))
      );
    });
  },
  (error: unknown) => say(error instanceof Error ? error.message : String(error))
);
