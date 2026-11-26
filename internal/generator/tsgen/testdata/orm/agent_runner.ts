// The program each agent run's sandbox instance runs in agents.test.ts
// (D158): the runtime's runAgentInstance over the run's endpoint, with the
// vault broker of examples/runtime-providers as its credential provider,
// whose settings the dispatcher passed the instance, and one tool the
// sandbox runs itself: http_get, which fetches a page with a credential's
// header resolved here. The credential's value goes into the request's
// header and nowhere else; the endpoint is told its reference.
import { providersFrom, runAgentInstance, SecretValue, type SandboxToolHandler } from '@superschematic/http-runtime/providers';
import { vaultBroker } from './vault-broker';

const env = process.env;

async function main(): Promise<void> {
  let credentials: Parameters<typeof runAgentInstance>[0]['credentials'];
  if (env.CREDENTIALS_URL && env.CREDENTIALS_TOKEN) {
    const providers = providersFrom({
      slots: { CREDENTIALS: 'credentials' },
      config: {
        CREDENTIALS: 'vault-broker',
        CREDENTIALS_URL: env.CREDENTIALS_URL,
        CREDENTIALS_TOKEN: new SecretValue(env.CREDENTIALS_TOKEN),
        CREDENTIALS_VAULTS: env.CREDENTIALS_VAULTS ?? 'ops',
      },
      definitions: [vaultBroker()],
    });
    credentials = { provider: await providers.credentials('CREDENTIALS'), name: 'vault-broker' };
  }
  const httpGet: SandboxToolHandler = async (input, ctx) => {
    const url = String(input.url);
    const headers = new Headers();
    if (typeof input.credential === 'string') {
      const held = ctx.credentials.find(c => c.ref === input.credential);
      await ctx.useCredential(input.credential, { kind: 'header', site: new URL(url).origin, name: held?.header ?? 'x-api-key' }, {
        fill: () => {
          throw new Error('http_get fills no browser field');
        },
        setHeader: (name, value) => headers.set(name, value),
      });
    }
    const response = await fetch(url, { headers });
    return { status: response.status, body: await response.text() };
  };
  await runAgentInstance({
    env,
    credentials,
    tools: { http_get: httpGet },
    approvalWaitMs: Number(env.AGENT_APPROVAL_WAIT_MS ?? 10_000),
    pollMs: 100,
  });
}

main().then(
  () => process.exit(0),
  () => process.exit(1)
);
