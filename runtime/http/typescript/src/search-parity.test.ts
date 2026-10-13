import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { MAX_SEARCH_TERMS, foldText, likePattern, searchMatches, searchTerms } from './index';

/*
The search parity vectors (D62), which the Go runtime writes (`go test
./search -run TestWriteParityVectors -update` in runtime/http/go) and the
Go, TypeScript and Rust runtimes each answer with their search helpers.
*/

const CORPUS = fileURLToPath(new URL('../../testdata/search_parity.json', import.meta.url));

interface ParityFile {
  readonly terms: readonly { readonly name: string; readonly text: string; readonly terms: readonly string[] }[];
  readonly patterns: readonly { readonly name: string; readonly term: string; readonly pattern: string }[];
  readonly folds: readonly { readonly name: string; readonly text: string; readonly fold: string }[];
  readonly matches: readonly { readonly name: string; readonly text: string; readonly query: string; readonly match: boolean }[];
}

const corpus = JSON.parse(readFileSync(CORPUS, 'utf8')) as ParityFile;

describe('search parity vectors', () => {
  test('every section has vectors with distinct names', () => {
    for (const section of [corpus.terms, corpus.patterns, corpus.folds, corpus.matches]) {
      expect(section.length).toBeGreaterThan(0);
      expect(new Set(section.map(vector => vector.name)).size).toBe(section.length);
    }
  });

  for (const vector of corpus.terms) {
    test(`terms: ${vector.name}`, () => {
      expect(searchTerms(vector.text)).toEqual([...vector.terms]);
    });
  }
  for (const vector of corpus.patterns) {
    test(`patterns: ${vector.name}`, () => {
      expect(likePattern(vector.term)).toBe(vector.pattern);
    });
  }
  for (const vector of corpus.folds) {
    test(`folds: ${vector.name}`, () => {
      expect(foldText(vector.text)).toBe(vector.fold);
    });
  }
  for (const vector of corpus.matches) {
    test(`matches: ${vector.name}`, () => {
      expect(searchMatches(vector.text, searchTerms(vector.query))).toBe(vector.match);
    });
  }
});

describe('search rules', () => {
  test('\\s is not the separator', () => {
    expect(searchTerms('red﻿anvil')).toEqual(['red﻿anvil']);
    expect(searchTerms('red\u0085anvil')).toEqual(['red', 'anvil']);
  });

  test('the first eight words count', () => {
    expect(searchTerms('a b c d e f g h i')).toHaveLength(MAX_SEARCH_TERMS);
  });
});
