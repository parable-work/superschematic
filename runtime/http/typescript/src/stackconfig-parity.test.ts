import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { StackConfigError, loadCallers, loadCors, loadDatabase, loadService, type StackEnv } from './index';

/*
The stackconfig parity vectors (D51, sections 3.4 and 8.6 of
docs/stack-model.md), which the Go runtime writes (`go test ./stackconfig
-run TestWriteParityVectors -update` in runtime/http/go) and the Go and
TypeScript runtimes each read with their own readers. Each vector holds an
ir derived value, the variables ir.DerivedVariables makes of it, edits to
them, and the loaded value or the refusal's messages.
*/

const CORPUS = fileURLToPath(new URL('../../testdata/stackconfig_parity.json', import.meta.url));

interface ParityVector {
  readonly name: string;
  readonly reader: 'database' | 'service' | 'callers' | 'cors';
  readonly field: string;
  readonly value: unknown;
  readonly variables: Readonly<Record<string, string>>;
  readonly edits?: Readonly<Record<string, string | null>>;
  readonly want: unknown;
  readonly refusals: readonly string[] | null;
}

const corpus = JSON.parse(readFileSync(CORPUS, 'utf8')) as { readonly vectors: readonly ParityVector[] };

function envOf(vector: ParityVector): StackEnv {
  const env: Record<string, string | undefined> = { ...vector.variables };
  for (const [name, value] of Object.entries(vector.edits ?? {})) {
    if (value === null) delete env[name];
    else env[name] = value;
  }
  return env;
}

function read(vector: ParityVector, env: StackEnv): unknown {
  switch (vector.reader) {
    case 'database':
      return loadDatabase(vector.field, env);
    case 'service':
      return loadService(vector.field, env);
    case 'callers':
      return loadCallers(vector.field, env);
    case 'cors':
      return { origins: loadCors(vector.field, env) };
  }
}

describe('stackconfig parity vectors', () => {
  test('the corpus has vectors with distinct names, for each reader', () => {
    expect(corpus.vectors.length).toBeGreaterThan(0);
    expect(new Set(corpus.vectors.map(vector => vector.name)).size).toBe(corpus.vectors.length);
    expect(new Set(corpus.vectors.map(vector => vector.reader))).toEqual(new Set(['database', 'service', 'callers', 'cors']));
  });

  for (const vector of corpus.vectors) {
    test(vector.name, () => {
      const env = envOf(vector);
      if (vector.refusals === null) {
        // The JSON round trip drops nothing a reader returns but undefined members.
        expect(JSON.parse(JSON.stringify(read(vector, env)))).toEqual(vector.want);
        return;
      }
      let refusal: unknown;
      try {
        read(vector, env);
      } catch (err) {
        refusal = err;
      }
      expect(refusal).toBeInstanceOf(StackConfigError);
      const lines = (refusal as StackConfigError).message.split('\n');
      expect(lines.length).toBe(vector.refusals.length);
      lines.forEach((line, i) => expect(line).toStartWith(vector.refusals![i]!));
      expect((refusal as StackConfigError).problems).toEqual(lines);
    });
  }
});
