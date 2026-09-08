/**
 * YAML helpers. Uses js-yaml directly; output is fed straight into the
 * lenient/strict parser.
 */

import * as yaml from 'js-yaml';
import {
  addFieldError,
  newValidationErrors,
  type ValidationErrors,
} from '@psgen/scalar-lib/validation';

export function loadYamlObject(
  source: string | Uint8Array
): { value: Record<string, unknown> | null; errors: ValidationErrors } {
  const errors = newValidationErrors();
  let text: string;
  if (typeof source === 'string') {
    text = source;
  } else {
    try {
      text = new TextDecoder().decode(source);
    } catch (err) {
      addFieldError(errors, '', 'yaml', err instanceof Error ? err.message : String(err));
      return { value: null, errors };
    }
  }
  let parsed: unknown;
  try {
    parsed = yaml.load(text);
  } catch (err) {
    addFieldError(errors, '', 'yaml', err instanceof Error ? err.message : String(err));
    return { value: null, errors };
  }
  if (parsed === null || parsed === undefined) {
    addFieldError(errors, '', 'yaml', 'expected YAML mapping payload');
    return { value: null, errors };
  }
  if (typeof parsed !== 'object' || Array.isArray(parsed)) {
    addFieldError(errors, '', 'yaml', 'expected YAML mapping payload');
    return { value: null, errors };
  }
  return { value: parsed as Record<string, unknown>, errors };
}

export function dumpYaml(value: unknown): string {
  return yaml.dump(value, { noRefs: true });
}
