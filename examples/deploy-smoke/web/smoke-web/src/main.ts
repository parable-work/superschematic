// smoke-web's page: where the site's config puts smoke-api, which the
// stack serves on the site's own origin (D91), and who is signed in there.
// It reads the config and calls the API with fetch alone, so the bundle
// carries no SDK.
const app = document.getElementById('app');

async function show(): Promise<string> {
  const response = await fetch('/__superschematic/config.json', { cache: 'no-store' });
  if (!response.ok) throw new Error(`the site's config: ${response.status}`);
  const config = (await response.json()) as { apis: Record<string, { url: string }> };
  const api = config.apis['smoke-api'];
  if (!api) throw new Error("the site's config names no smoke-api");
  const me = await fetch(`${api.url}/api/auth/me`, { credentials: 'same-origin' });
  if (me.status === 401) return `smoke-api is at ${api.url}; no one is signed in.`;
  if (!me.ok) throw new Error(`smoke-api's /api/auth/me: ${me.status}`);
  const { data } = (await me.json()) as { data: { user: { name: string } } };
  return `smoke-api is at ${api.url}; ${data.user.name} is signed in.`;
}

show().then(
  text => {
    if (app) app.textContent = text;
  },
  (error: unknown) => {
    if (app) app.textContent = error instanceof Error ? error.message : String(error);
  }
);
