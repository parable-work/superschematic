import * as generatedScalars from '@psgen/scalar-lib/scalars';
import {
  validateArtifactFile,
  validateCryptoRSAPrivateKey,
  validateFile,
  validateImage,
  validateJSON,
  validateLocation,
  validateLogoImage,
  validateParableSchema,
  validateParableSchemaData,
  validatePermission,
} from '../../platform';
import type { ValidationError } from '@psgen/scalar-lib/validation';
import type { ScalarRegistry, ScalarValidateFn } from './types';

function isScalarValidationResult(value: unknown): value is [boolean, ValidationError[] | null] {
  return Array.isArray(value) && value.length === 2 && typeof value[0] === 'boolean';
}

function normalizeScalarResult(result: unknown): ReturnType<ScalarValidateFn> {
  if (isScalarValidationResult(result)) {
    const [valid, errors] = result;
    return valid || !errors || errors.length === 0 ? null : errors;
  }
  const errors = result as ValidationError[];
  return errors.length === 0 ? null : errors;
}

type ScalarValidatorFn = (value: any) => unknown;

function normalizeScalarRegistryKey(name: string): string {
  const trimmed = name.trim();
  if (!trimmed) {
    return trimmed;
  }
  if (trimmed.includes('.')) {
    return trimmed;
  }
  if (trimmed.includes('_')) {
    return trimmed.split('_').join('.');
  }
  return trimmed;
}

function isScalarValidatorExport(value: unknown): value is ScalarValidatorFn {
  return typeof value === 'function';
}

function registerGeneratedValidator(
  validators: Record<string, ScalarValidateFn>,
  canonicalName: string,
  symbol: string
): void {
  const candidate = (generatedScalars as Record<string, unknown>)['validate' + symbol];
  if (!isScalarValidatorExport(candidate)) {
    return;
  }
  validators[canonicalName] = (value: string) => normalizeScalarResult(candidate(value));
}

function registerPlatformValidator(
  validators: Record<string, ScalarValidateFn>,
  canonicalName: string,
  fn: ScalarValidatorFn
): void {
  validators[canonicalName] = (value: string) => normalizeScalarResult(fn(value));
}

function createDefaultScalarValidators(): Record<string, ScalarValidateFn> {
  const validators: Record<string, ScalarValidateFn> = {};

  for (const meta of generatedScalars.SCALAR_METADATA) {
    registerGeneratedValidator(validators, meta.canonicalName, meta.symbol);
  }

  registerPlatformValidator(validators, 'Artifact.File', validateArtifactFile);
  registerPlatformValidator(validators, 'Asset.File', validateFile);
  registerPlatformValidator(validators, 'Asset.Image', validateImage);
  registerPlatformValidator(validators, 'Asset.LogoImage', validateLogoImage);
  registerPlatformValidator(validators, 'Crypto.RSAPrivateKey', validateCryptoRSAPrivateKey);
  registerPlatformValidator(validators, 'Generic.JSON', validateJSON);
  registerPlatformValidator(validators, 'Geo.Location', validateLocation);
  registerPlatformValidator(validators, 'Parable.Permission', validatePermission);
  registerPlatformValidator(validators, 'Parable.Schema', validateParableSchema);
  registerPlatformValidator(validators, 'Parable.SchemaData', validateParableSchemaData);

  return validators;
}

export class ScalarValidatorRegistry implements ScalarRegistry {
  private readonly validators = new Map<string, ScalarValidateFn>();

  constructor(initialValidators?: Record<string, ScalarValidateFn>) {
    if (!initialValidators) {
      return;
    }

    for (const [name, validateFn] of Object.entries(initialValidators)) {
      this.validators.set(normalizeScalarRegistryKey(name), validateFn);
    }
  }

  register(name: string, fn: ScalarValidateFn): void {
    this.validators.set(normalizeScalarRegistryKey(name), fn);
  }

  unregister(name: string): boolean {
    return this.validators.delete(normalizeScalarRegistryKey(name));
  }

  get(name: string): ScalarValidateFn | undefined {
    return this.validators.get(normalizeScalarRegistryKey(name));
  }

  has(name: string): boolean {
    return this.validators.has(normalizeScalarRegistryKey(name));
  }

  names(): string[] {
    return [...this.validators.keys()].sort((a, b) => a.localeCompare(b));
  }
}

let defaultScalarValidatorsCache: Record<string, ScalarValidateFn> | null = null;

function getDefaultScalarValidators(): Record<string, ScalarValidateFn> {
  if (!defaultScalarValidatorsCache) {
    defaultScalarValidatorsCache = createDefaultScalarValidators();
  }
  return defaultScalarValidatorsCache;
}

// Lazy proxy keeps the public export while avoiding eager registry construction
// during module initialization.
export const defaultScalarValidators: Record<string, ScalarValidateFn> = new Proxy(
  {} as Record<string, ScalarValidateFn>,
  {
    get(_target, prop) {
      return getDefaultScalarValidators()[prop as string];
    },
    ownKeys() {
      return Reflect.ownKeys(getDefaultScalarValidators());
    },
    getOwnPropertyDescriptor(_target, prop) {
      return Reflect.getOwnPropertyDescriptor(getDefaultScalarValidators(), prop);
    },
    has(_target, prop) {
      return prop in getDefaultScalarValidators();
    },
  }
);

export function createDefaultScalarValidatorRegistry(): ScalarValidatorRegistry {
  return new ScalarValidatorRegistry(getDefaultScalarValidators());
}

export function createScalarValidatorRegistry(
  validators?: Record<string, ScalarValidateFn>
): ScalarValidatorRegistry {
  const registry = createDefaultScalarValidatorRegistry();
  if (!validators) {
    return registry;
  }

  for (const [name, validateFn] of Object.entries(validators)) {
    registry.register(name, validateFn);
  }

  return registry;
}
