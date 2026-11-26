// What the types of a credential provider hold (D159), checked by tsc
// (scripts/check.sh): an Injection gives no value, no object literal passes
// for one, a provider makes one only from a secret, and list's items carry
// no value either.
import {
  Injection,
  SecretValue,
  type CredentialListing,
  type CredentialProvider,
  type InjectionTarget,
} from '@superschematic/http-runtime/providers';

declare const injection: Injection;
declare const target: InjectionTarget & { kind: 'header' };

// @ts-expect-error an Injection has no value to read.
void injection.value;

// @ts-expect-error nor a reveal.
void injection.reveal();

// @ts-expect-error a provider seals a SecretValue, never a plain string.
Injection.header('vault://ops/item/password', target, 'plain-text');

Injection.header('vault://ops/item/password', target, new SecretValue('sealed'));

export const forged: CredentialProvider = {
  async list() {
    return [];
  },
  // @ts-expect-error an object literal is no Injection: its private field makes the class nominal.
  async resolve(ref, into) {
    return { ref, target: into, toJSON: () => ({}) };
  },
};

export const listing: CredentialListing = {
  ref: 'vault://ops/item/password',
  title: 'Item',
  sites: ['https://example.test'],
  // @ts-expect-error a listing has titles and sites, no value.
  value: 'secret',
};
