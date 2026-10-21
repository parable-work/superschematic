// The callee of the storefront fixture: an internal Worker that only a
// service binding reaches, which verifies the caller's signed token
// against its callers field (D37, D64, section 18.10 of
// docs/stack-model.md) and quotes twice the cents it is asked.
import { env } from 'cloudflare:workers';
import { loadCallers, serviceAuthenticator } from '../../../../dist/index.js';
import { stackEnvOf } from '../../../../dist/workers.js';

const authenticate = serviceAuthenticator(loadCallers('PRICING_CALLERS', stackEnvOf(env)));

export default {
  async fetch(request: Request): Promise<Response> {
    const caller = await authenticate({ headers: request.headers } as Parameters<typeof authenticate>[0]).catch(() => null);
    if (caller === null) return new Response('no service credential', { status: 401 });
    const cents = Number(new URL(request.url).searchParams.get('cents'));
    return Response.json({ caller: caller.deployable, serves: caller.serves, cents: cents * 2 });
  },
};
