// @acme/behaviors: what acme's declared behaviors do when an engine runs
// them. A deployment passes `behaviors` and `tools` to openEngine, with the
// meta-schema `acme-schematic json-schema` writes.
import type { ToolOptions } from '@superschematic/engine';

import { rating } from './rating.ts';

export { rating };
export type { RatingConfig } from './rating.ts';

/** Every behavior acme declares, for EngineOptions.behaviors. */
export const behaviors = [rating];

/**
 * The invocation policy and vendor keys acme's binary registers
 * (ext/mcp.go), for EngineOptions.tools: the engine writes acme's tools as
 * acme's generated SDKs write theirs, and takes acme.Rating's rate, whose
 * declaration asks confirm "always".
 */
export const tools: ToolOptions = {
  invocationPolicy: { key: 'confirm', values: ['never', 'always'], default: 'never' },
  keys: { scalar: 'x-acme-scalar', guidance: 'acme/operation-guidance', parameters: [{ key: 'x-acme-arguments', value: 1 }] },
};
