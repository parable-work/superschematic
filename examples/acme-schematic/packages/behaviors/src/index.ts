// @acme/behaviors: what acme's declared behaviors do when an engine runs
// them. A deployment passes `behaviors` to openEngine, with the meta-schema
// `acme-schematic json-schema` writes.
import { rating } from './rating.ts';

export { rating };
export type { RatingConfig } from './rating.ts';

/** Every behavior acme declares, for EngineOptions.behaviors. */
export const behaviors = [rating];
