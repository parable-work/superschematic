// Type-level checks of Aliased<T, C> (D147). Nothing imports this file:
// `bun run typecheck`, which `make ts` runs, checks it, and fails on an
// expected error (@ts-expect-error) that no longer errors.
import type { Aliased, AliasedConfig } from "./index";

// The field's type is its T, the name, which a create gives and a row
// holds, never null.
export abstract class Company {
  id: string;
  name: Aliased<string>;
  label: Aliased<string, { normalize: "slug" }>;
}

const named: Company["name"] = "Acme";
const lower: AliasedConfig = { normalize: "lower" };

// @ts-expect-error The name is never null: a create gives it.
const unnamed: Company["name"] = null;
// @ts-expect-error T is string.
type Numbered = Aliased<number>;
// @ts-expect-error normalize is lower or slug.
type Upper = Aliased<string, { normalize: "upper" }>;
// @ts-expect-error an Aliased config names no other member.
const unknownMember: AliasedConfig = { normalize: "lower", trim: true };
