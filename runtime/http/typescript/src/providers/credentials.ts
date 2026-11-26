import { SECRET_MASK, type Revealable } from '../secret.js';

/*
The credential provider (D159). Core never holds a credential's value: list
gives the titles and sites of the items a run may use, and resolve gives an
Injection, which carries the value sealed. Only applyInjection opens it,
handing it to the runner that uses it, a browser that fills a field or a
client that sets a request header, inside the process that runs the step.
JSON, util.inspect and string conversion of an Injection print its
reference and target alone, so a log or a run record that holds one holds
no value. useCredential does the three steps and returns what a run record
keeps: the reference, the provider, the target and the time it was used.
*/

/** An item a credential provider holds, as list gives it: never its value. */
export interface CredentialListing {
  /** The secret reference the item is named by, in the provider's own form, such as "vault/item/field". */
  readonly ref: string;
  /** What a person calls the item. */
  readonly title: string;
  /** The sites the item may be used on, by origin or host. */
  readonly sites: readonly string[];
}

/** What list narrows to: the items usable on one site. */
export interface CredentialQuery {
  readonly site?: string;
}

/** Where an injection goes: a field a browser fills, or a header a request carries, on one site. */
export type InjectionTarget =
  | { readonly kind: 'fill'; readonly site: string; readonly selector: string }
  | { readonly kind: 'header'; readonly site: string; readonly name: string };

/** What applies an injection: the runner of a step, inside the process that uses the value. */
export interface InjectionRunner {
  /** Fills a browser field with the value. */
  fill(selector: string, value: string): Promise<void> | void;
  /** Sets a header of the request the runner sends. */
  setHeader(name: string, value: string): Promise<void> | void;
}

/** What a run record keeps of a credential's use (D159): its reference, never its value. */
export interface CredentialUse {
  readonly ref: string;
  /** The provider's name. */
  readonly provider: string;
  readonly target: InjectionTarget;
  readonly usedAt: Date;
}

/**
 * A credential provider: password managers and secret brokers implement it.
 * Neither operation gives core a value. A provider refuses a reference its
 * scope does not hold, and one whose item is not for the target's site.
 */
export interface CredentialProvider {
  /** The items a run may use, narrowed to a site: titles and sites only. */
  list(query?: CredentialQuery): Promise<readonly CredentialListing[]>;
  /** The item ref names, sealed in an injection into target. */
  resolve(ref: string, target: InjectionTarget): Promise<Injection>;
}

const sealed = new WeakMap<Injection, Revealable<string>>();
const inspectCustom = Symbol.for('nodejs.util.inspect.custom');

/**
 * A credential's value on its way to the runner, sealed: no member of it
 * returns the value, and only applyInjection, of this module, opens it. A
 * provider makes one with Injection.fill or Injection.header from a
 * SecretValue, so the value is never a plain string in its hands either.
 * Its private field makes it nominal: no object literal passes for one.
 */
export class Injection {
  readonly ref: string;
  readonly target: InjectionTarget;
  readonly #kind: InjectionTarget['kind'];

  private constructor(ref: string, target: InjectionTarget, value: Revealable<string>) {
    this.ref = ref;
    this.target = Object.freeze({ ...target });
    this.#kind = target.kind;
    sealed.set(this, value);
    Object.freeze(this);
  }

  /** An injection that fills the field selector names on site with value. */
  static fill(ref: string, target: { readonly site: string; readonly selector: string }, value: Revealable<string>): Injection {
    return new Injection(ref, { kind: 'fill', site: target.site, selector: target.selector }, value);
  }

  /** An injection that sets the header name of a request to site to value. */
  static header(ref: string, target: { readonly site: string; readonly name: string }, value: Revealable<string>): Injection {
    return new Injection(ref, { kind: 'header', site: target.site, name: target.name }, value);
  }

  toJSON(): { ref: string; target: InjectionTarget; value: string } {
    return { ref: this.ref, target: this.target, value: SECRET_MASK };
  }

  toString(): string {
    return `Injection(${this.#kind} ${this.ref})`;
  }

  [inspectCustom](): string {
    return this.toString();
  }
}

/** Refuses an injection: not one this module sealed, or not the reference and target asked for. */
export class InjectionError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'InjectionError';
  }
}

function sameTarget(a: InjectionTarget, b: InjectionTarget): boolean {
  if (a.kind !== b.kind || a.site !== b.site) return false;
  return a.kind === 'fill' ? a.selector === (b as typeof a).selector : a.name === (b as typeof a).name;
}

/** Hands injection's value to runner, the one place it is opened. */
export async function applyInjection(injection: Injection, runner: InjectionRunner): Promise<void> {
  const value = sealed.get(injection);
  if (value === undefined) throw new InjectionError('not an injection a credential provider sealed');
  const target = injection.target;
  if (target.kind === 'fill') await runner.fill(target.selector, value.reveal());
  else await runner.setHeader(target.name, value.reveal());
}

/** What useCredential takes. */
export interface UseCredential {
  /** The provider, as Providers.credentials gives it. */
  readonly provider: CredentialProvider;
  /** The provider's name, which the use records. */
  readonly providerName: string;
  readonly ref: string;
  readonly target: InjectionTarget;
  readonly runner: InjectionRunner;
  /** Records the use, a run record's credential log; the record holds the reference and the time. */
  readonly record?: (use: CredentialUse) => Promise<void> | void;
  readonly now?: () => Date;
}

/**
 * Resolves ref into target through the provider, applies it in the runner
 * and records the use. The caller gets the use, which holds no value. An
 * injection for another reference or another target is refused before the
 * runner sees it.
 */
export async function useCredential(use: UseCredential): Promise<CredentialUse> {
  const injection = await use.provider.resolve(use.ref, use.target);
  if (!(injection instanceof Injection) || !sealed.has(injection)) {
    throw new InjectionError(`credential provider ${use.providerName} answered ${use.ref} with no sealed injection`);
  }
  if (injection.ref !== use.ref || !sameTarget(injection.target, use.target)) {
    throw new InjectionError(`credential provider ${use.providerName} answered ${use.ref} with an injection for ${injection.ref} into another target`);
  }
  await applyInjection(injection, use.runner);
  const record: CredentialUse = Object.freeze({
    ref: use.ref,
    provider: use.providerName,
    target: injection.target,
    usedAt: (use.now ?? (() => new Date()))(),
  });
  await use.record?.(record);
  return record;
}
