/**
 * Scalar parse and normalize registries: map canonical scalar names (e.g.
 * "Contact.Email") to generated superscalar implementations. Validation has its
 * own registry in runtime/validation/registry.ts.
 */

import { backend } from 'superscalar/backend';
import * as generatedScalars from 'superscalar/scalars';
import { SCALAR_METADATA, scalarIdByCanonical } from 'superscalar/scalars';
import type { ValidationError } from 'superscalar/validation';
import { BUILTIN_SCALARS } from '../builtin-scalars.generated';
import type { Schema } from '../validation/types';

export type ScalarParseFunc = (input: string) => [string, ValidationError[]];
export type ScalarNormalizeFunc = (input: string) => string;

function normalizeScalarRegistryKey(name: string): string {
  const trimmed = name.trim();
  if (!trimmed || trimmed.includes('.')) {
    return trimmed;
  }
  return trimmed.includes('_') ? trimmed.split('_').join('.') : trimmed;
}

export class ScalarParseRegistry {
  private readonly fns = new Map<string, ScalarParseFunc>();

  constructor(initial?: Record<string, ScalarParseFunc>) {
    if (!initial) return;
    for (const [name, fn] of Object.entries(initial)) {
      this.fns.set(normalizeScalarRegistryKey(name), fn);
    }
  }

  register(name: string, fn: ScalarParseFunc): void {
    this.fns.set(normalizeScalarRegistryKey(name), fn);
  }

  unregister(name: string): boolean {
    return this.fns.delete(normalizeScalarRegistryKey(name));
  }

  get(name: string): ScalarParseFunc | undefined {
    return this.fns.get(normalizeScalarRegistryKey(name));
  }

  has(name: string): boolean {
    return this.fns.has(normalizeScalarRegistryKey(name));
  }

  names(): string[] {
    return [...this.fns.keys()].sort((a, b) => a.localeCompare(b));
  }

  missingParsers(schema: Schema): string[] {
    const missing: string[] = [];
    for (const scalar of Object.values(schema.scalars || {})) {
      if (scalar.hasCustomParse && !this.has(scalar.name)) {
        missing.push(scalar.name);
      }
    }
    return missing.sort((a, b) => a.localeCompare(b));
  }
}

export class ScalarNormalizeRegistry {
  private readonly fns = new Map<string, ScalarNormalizeFunc>();

  constructor(initial?: Record<string, ScalarNormalizeFunc>) {
    if (!initial) return;
    for (const [name, fn] of Object.entries(initial)) {
      this.fns.set(normalizeScalarRegistryKey(name), fn);
    }
  }

  register(name: string, fn: ScalarNormalizeFunc): void {
    this.fns.set(normalizeScalarRegistryKey(name), fn);
  }

  unregister(name: string): boolean {
    return this.fns.delete(normalizeScalarRegistryKey(name));
  }

  get(name: string): ScalarNormalizeFunc | undefined {
    return this.fns.get(normalizeScalarRegistryKey(name));
  }

  has(name: string): boolean {
    return this.fns.has(normalizeScalarRegistryKey(name));
  }

  names(): string[] {
    return [...this.fns.keys()].sort((a, b) => a.localeCompare(b));
  }

  missingNormalizers(schema: Schema): string[] {
    const missing: string[] = [];
    for (const scalar of Object.values(schema.scalars || {})) {
      if (scalar.hasCustomNormalize && !this.has(scalar.name)) {
        missing.push(scalar.name);
      }
    }
    return missing.sort((a, b) => a.localeCompare(b));
  }
}

function wrapParse(
  fn: (input: unknown) => unknown | null,
  label: string,
): ScalarParseFunc {
  return (input: string) => {
    const parsed = fn(input);
    if (parsed === null) {
      return [input, [{ validator: 'parse', message: `invalid ${label}` }]];
    }
    return [String(parsed), []];
  };
}

function wrapNormalize(
  fn: (input: unknown) => unknown | null,
): ScalarNormalizeFunc {
  return (input: string) => {
    const normalized = fn(input);
    if (normalized === null) {
      return input;
    }
    return String(normalized);
  };
}

// Route through the core and keep its canonical text. Temporal.DateTime gets
// the exact RFC3339Nano form (sub-seconds preserved/trimmed, numeric offset
// kept), not JS `toISOString()`, which forces `.000` padding and UTC. A
// JSON-shaped scalar (json_schema type `object`, such as Generic.StringMap)
// gets canonical JSON text: its generated parse function returns the decoded
// map, which wrapParse would stringify as `[object Object]`.
function coreTextAdapter(canonicalName: string, label: string): ScalarParseFunc {
  const id = scalarIdByCanonical[canonicalName];
  return (input: string) => {
    try {
      return [backend.parse(id, input), []];
    } catch {
      return [input, [{ validator: 'parse', message: `invalid ${label}` }]];
    }
  };
}

type GeneratedScalarFn = (input: unknown) => unknown | null;

/**
 * Looks up the generated superscalar `parse<Symbol>` / `normalize<Symbol>`
 * function for a canonical name, using the symbol the metadata table carries.
 */
function generatedScalarFn(prefix: 'parse' | 'normalize', symbol: string): GeneratedScalarFn | undefined {
  const candidate = (generatedScalars as Record<string, unknown>)[prefix + symbol];
  if (typeof candidate !== 'function') {
    return undefined;
  }
  return candidate as GeneratedScalarFn;
}

/**
 * Default parse registry: every scalar whose generated catalog entry
 * (builtin-scalars.generated.ts, written by internal/tools/scalarcatalog) marks a
 * custom parse step, adapted over the generated superscalar parse function. The
 * walker only consults the registry for hasCustomParse scalars, so those are
 * the only names registered; the set follows the catalog, not a hand list.
 * Temporal.DateTime and JSON-shaped scalars route through the core directly
 * and return its canonical text (coreTextAdapter).
 */
export function createDefaultScalarParseRegistry(): ScalarParseRegistry {
  const r = new ScalarParseRegistry();
  for (const meta of SCALAR_METADATA) {
    const def = BUILTIN_SCALARS[meta.canonicalName.replace(/\./g, '_')];
    if (!def || !def.hasCustomParse) continue;
    const label = meta.canonicalName.slice(meta.canonicalName.indexOf('.') + 1);
    if (meta.canonicalName === 'Temporal.DateTime' || def.typeMappings.json_schema === 'object') {
      r.register(meta.canonicalName, coreTextAdapter(meta.canonicalName, label));
      continue;
    }
    const fn = generatedScalarFn('parse', meta.symbol);
    if (!fn) continue;
    r.register(meta.canonicalName, wrapParse(fn, label));
  }
  return r;
}

/**
 * Default normalize registry: every scalar whose generated catalog entry marks
 * a custom normalize step, adapted over the generated superscalar normalize
 * function.
 */
export function createDefaultScalarNormalizeRegistry(): ScalarNormalizeRegistry {
  const r = new ScalarNormalizeRegistry();
  for (const meta of SCALAR_METADATA) {
    const def = BUILTIN_SCALARS[meta.canonicalName.replace(/\./g, '_')];
    if (!def || !def.hasCustomNormalize) continue;
    const fn = generatedScalarFn('normalize', meta.symbol);
    if (!fn) continue;
    r.register(meta.canonicalName, wrapNormalize(fn));
  }
  return r;
}
