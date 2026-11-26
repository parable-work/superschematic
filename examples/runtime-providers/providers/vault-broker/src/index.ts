// A credential provider over a password manager's service API (D159).
// Items are named by secret reference, vault://<vault>/<item>/<field>, and
// the broker holds a service account token limited to the vaults VAULTS
// names, which it also refuses others by. list gives each item's title and
// sites; resolve reads one field and seals it in an Injection, which only
// the runner opens. The value is never returned, logged or held as a plain
// string outside the call that seals it.
import {
  Injection,
  SecretValue,
  defineProvider,
  type CredentialListing,
  type CredentialProvider,
  type InjectionTarget,
} from '@superschematic/http-runtime/providers';

/** How the broker reaches the service API; globalThis.fetch unless a test gives another. */
export interface VaultBrokerOptions {
  readonly fetch?: typeof fetch;
}

/** An item as the service API's list answers it: no field values. */
interface ItemSummary {
  readonly id: string;
  readonly title: string;
  readonly urls?: readonly string[];
}

/** An item as the service API answers it alone, its fields with their values. */
interface Item extends ItemSummary {
  readonly fields: readonly { readonly label: string; readonly value: string }[];
}

/** A secret reference, read: vault://<vault>/<item>/<field>. */
export function parseReference(ref: string): { vault: string; item: string; field: string } {
  const match = /^vault:\/\/([^/]+)\/([^/]+)\/([^/]+)$/u.exec(ref);
  if (!match) throw new Error(`vault-broker: ${JSON.stringify(ref)} is no secret reference; one is vault://<vault>/<item>/<field>`);
  return { vault: decodeURIComponent(match[1]!), item: decodeURIComponent(match[2]!), field: decodeURIComponent(match[3]!) };
}

/** Whether an item whose sites are urls may be used on site: same origin, or same host for a bare host. */
function usableOn(urls: readonly string[], site: string): boolean {
  const host = (text: string) => {
    try {
      return new URL(text.includes('://') ? text : `https://${text}`).host;
    } catch {
      return text;
    }
  };
  return urls.some(url => host(url) === host(site));
}

/** The definition, over options.fetch. */
export function vaultBroker(options: VaultBrokerOptions = {}) {
  return defineProvider({
    name: 'vault-broker',
    implements: 'credentials',
    config: {
      URL: { description: "The password manager's service API." },
      TOKEN: { secret: true, description: 'A service account token the password manager limits to the vaults below.' },
      VAULTS: { description: 'The vaults a run may use, comma-separated; every other is refused here as well.' },
    },
    create(settings, context): CredentialProvider {
      const base = settings.text('URL').replace(/\/+$/u, '');
      const token = settings.secret('TOKEN');
      const vaults = settings
        .text('VAULTS')
        .split(',')
        .map(name => name.trim())
        .filter(Boolean);
      const call = options.fetch ?? globalThis.fetch;
      const get = async <T>(path: string): Promise<T> => {
        const response = await call(`${base}${path}`, { headers: { authorization: `Bearer ${token.reveal()}` } });
        if (!response.ok) throw new Error(`vault-broker: the service answered ${response.status} for ${path}`);
        return (await response.json()) as T;
      };
      const inScope = (vault: string) => {
        if (!vaults.includes(vault)) throw new Error(`vault-broker: vault ${vault} is not one this broker may read (${vaults.join(', ')})`);
      };

      return {
        async list(query) {
          const listings: CredentialListing[] = [];
          for (const vault of vaults) {
            const items = await get<ItemSummary[]>(`/v1/vaults/${encodeURIComponent(vault)}/items`);
            for (const item of items) {
              const sites = [...(item.urls ?? [])];
              if (query?.site && !usableOn(sites, query.site)) continue;
              // Each item's password, the field a sign-in fills.
              listings.push({ ref: `vault://${encodeURIComponent(vault)}/${encodeURIComponent(item.id)}/password`, title: item.title, sites });
            }
          }
          return listings;
        },
        async resolve(ref: string, target: InjectionTarget) {
          const { vault, item: itemId, field: label } = parseReference(ref);
          inScope(vault);
          const item = await get<Item>(`/v1/vaults/${encodeURIComponent(vault)}/items/${encodeURIComponent(itemId)}`);
          if (!usableOn(item.urls ?? [], target.site)) throw new Error(`vault-broker: ${ref} is not for ${target.site}`);
          const field = item.fields.find(entry => entry.label === label);
          if (!field) throw new Error(`vault-broker: ${ref} names no field ${label}`);
          const value = new SecretValue(field.value);
          context.log.info('credential resolved', { ref, site: target.site, kind: target.kind });
          return target.kind === 'fill' ? Injection.fill(ref, target, value) : Injection.header(ref, target, value);
        },
      };
    },
  });
}

/** The definition a server links, over the platform's fetch. */
export default vaultBroker();
