/*
Search's guidance: which fields a search reads and how they weigh, and,
with vectors, their model and dimensions and who settles them, so a
caller knows whether a vector search, staleEmbeddings or
settleEmbeddings applies on this type.
*/

import type { BehaviorGuidance, DescribeTarget, OperationGuidance } from '../../behavior.js';
import type { SearchConfig } from '../search.js';
import { list, sentences } from './text.js';

const PAGE = 'Pass next as cursor for the page after, until it is null.';

export function searchGuidance(config: SearchConfig, target: DescribeTarget): BehaviorGuidance {
  const weighed = config.fields.map((field, index) => (config.weights[index] === 1 ? field : `${field} (weight ${config.weights[index]})`));
  const vectors = config.vectors;
  const noVectors = (what: string): OperationGuidance => ({
    doNotUseWhen: `Do not use: Search on ${target.type} keeps no vectors, so ${what} is refused.`,
  });
  return {
    summary: sentences(
      `Full-text search over ${list(weighed)}.`,
      vectors === undefined
        ? 'It keeps no vectors.'
        : `With vectors of ${vectors.dimensions} dimensions from model ${vectors.model}, which an outside embedder with permission ${vectors.permission} settles.`
    ),
    operations: {
      search: {
        useWhen: sentences(
          `Use to find ${target.type} instances by words in ${list(config.fields, 'or')}: plain words match when the fields hold every one; syntax fts5 takes an FTS5 expression.`,
          vectors === undefined ? undefined : `A vector of ${vectors.dimensions} numbers from model ${vectors.model} ranks by similarity, alone or fused with a query.`
        ),
        doNotUseWhen: 'Do not use to page through every instance; call list.',
        success: `Returns hits best first, each its id, rank and a snippet of the field that matched. ${PAGE}`,
      },
      similar: {
        useWhen: sentences(
          `Use before creating a ${target.type}, to find near-duplicates to reuse or link instead: with id, the instances nearest to one you have; with text, the ones nearest to a draft's ${
            config.fields.length === 1 ? config.fields[0] : `${list(config.fields)}, joined by a blank line`
          }.`,
          vectors === undefined ? undefined : `With text, a vector of ${vectors.dimensions} numbers from model ${vectors.model} computed from the same text ranks by similarity too.`
        ),
        doNotUseWhen: 'Do not use to search by a few words; call search.',
        success: `Returns the nearest instances, leaving out the one named by id, and whether a vector ranked (embedded). ${PAGE}`,
      },
      staleEmbeddings:
        vectors === undefined
          ? noVectors('listing stale embeddings')
          : {
              useWhen: `Use as an embedder: it lists the instances whose vector from ${vectors.model} is missing or stale, with the text to embed and its hash.`,
              success: `Returns the model, the dimensions and the items. ${PAGE}`,
            },
      settleEmbeddings:
        vectors === undefined
          ? noVectors('a settle')
          : {
              useWhen: `Use as an embedder with permission ${vectors.permission}: it stores at most 100 vectors of ${vectors.dimensions} numbers, each with the hash staleEmbeddings gave.`,
              doNotUseWhen: 'Do not settle a vector computed from other text or another model: its hash no longer matches and it is skipped.',
              success: 'Returns how many it settled and each skipped item with its reason, moved or not_found.',
            },
      create: { doNotUseWhen: "Do not create a duplicate: call similar with the draft's text first, to find one to reuse." },
    },
  };
}
