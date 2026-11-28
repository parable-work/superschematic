/*
The runtime extension points (D159): five interfaces a running app calls,
each implemented by a provider package. The build reads the same contracts
(ir.RuntimeContracts in Go) and checks each provider package's manifest
against them; runtime/http/testdata/runtime_contracts.json holds them for
both, and a test on each side keeps them equal.
*/

/** The runtime interfaces (D159). */
export type RuntimeInterface = 'credentials' | 'channel' | 'sandbox' | 'model' | 'files';

/** What a provider of one interface implements: the operations core calls. */
export interface RuntimeContract {
  readonly interface: RuntimeInterface;
  /** What a message calls a provider of it. */
  readonly title: string;
  /** The operations every provider implements. */
  readonly required: readonly string[];
  /** The operations a provider may implement, and lists in its manifest when it does. */
  readonly optional?: readonly string[];
}

/** The five contracts, in the Go runtime's order. */
export const RUNTIME_CONTRACTS: readonly RuntimeContract[] = Object.freeze([
  { interface: 'credentials', title: 'credential provider', required: ['list', 'resolve'] },
  { interface: 'channel', title: 'channel adapter', required: ['send', 'health'], optional: ['status', 'inbound'] },
  { interface: 'sandbox', title: 'sandbox provider', required: ['start', 'cancel', 'status', 'cost'] },
  { interface: 'model', title: 'model provider', required: ['complete'] },
  { interface: 'files', title: 'file processor', required: ['extractText', 'preview'] },
]);

/** The contract of an interface, or undefined for a name that is none. */
export function contractOf(name: string): RuntimeContract | undefined {
  return RUNTIME_CONTRACTS.find(contract => contract.interface === name);
}

/** The subject of a provider's principal: extension:<name>. */
export function extensionSubject(provider: string): string {
  return `extension:${provider}`;
}

/**
 * The reserved setting of a provider that calls the app with a key of its
 * own (#744): slot SMS reads it from SMS_CALLER_KEY, a Secret<string>, and a
 * request whose Bearer token is the key runs as the provider, with its role
 * alone (extensionKeysOf, extensionAuthenticator).
 */
export const CALLER_KEY = 'CALLER_KEY';

/** The config field a provider in slot reads its setting key from: SMS and URL give SMS_URL. */
export function providerConfigField(slot: string, key: string): string {
  return `${slot}_${key}`;
}
