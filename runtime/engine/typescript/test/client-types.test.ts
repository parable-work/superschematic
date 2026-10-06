// The client declares the wire's shapes itself, so it imports nothing of
// the server: the type checks below hold each to the server's type, and
// tsc -p tsconfig.test.json fails when one drifts from the other. The
// test proper reads the built client and finds no import of a module
// outside it, so a browser or a worker runtime loads none.
import assert from 'node:assert/strict';
import { readFileSync, readdirSync } from 'node:fs';
import { test } from 'node:test';

import type { ServiceTokenSource } from '@superschematic/http-runtime';

import type * as client from '../dist/client/index.js';
import type * as server from '../dist/index.js';

type Equal<A, B> = (<T>() => T extends A ? 1 : 2) extends <T>() => T extends B ? 1 : 2 ? true : false;
type Holds<T extends true> = T;

// The same members, and what the server sends fits what the client reads.
type SameShape<Server, Client> = Equal<keyof Server, keyof Client> extends true ? ([Server] extends [Client] ? true : false) : false;

export type Checks = [
  Holds<SameShape<server.InstanceRecord, client.Instance>>,
  Holds<SameShape<server.InstancePage, client.InstancePage>>,
  Holds<Equal<keyof Omit<server.SchemaRecord, 'canonical'>, keyof client.SchemaVersion>>,
  Holds<SameShape<Omit<server.SchemaRecord, 'canonical' | 'document'>, Omit<client.SchemaVersion, 'document'>>>,
  Holds<SameShape<server.SchemaSummary, client.SchemaSummary>>,
  Holds<SameShape<server.PublishResult, client.PublishResult>>,
  Holds<Equal<server.EventKind, client.EventKind>>,
  Holds<SameShape<server.EngineEvent, client.EngineEvent>>,
  Holds<SameShape<server.EventCause, client.EventCause>>,
  Holds<SameShape<server.OperationChange, client.OperationChange>>,
  Holds<SameShape<server.EventPage, client.EventPage>>,
  Holds<SameShape<server.DescribeDocument, client.DescribeDocument>>,
  Holds<SameShape<server.DescribedBehavior, client.DescribedBehavior>>,
  Holds<SameShape<server.DescribedOperation, client.DescribedOperation>>,
  Holds<SameShape<server.BehaviorSummary, client.BehaviorSummary>>,
  Holds<SameShape<server.BehaviorDocument, client.BehaviorDocument>>,
  Holds<SameShape<server.BehaviorOperationDocument, client.BehaviorOperationDocument>>,
  Holds<SameShape<server.SchemaSearchHit, client.SearchHit>>,
  Holds<SameShape<server.ToolManifest, client.ToolManifest>>,
  Holds<SameShape<server.ToolDefinition, client.ToolDefinition>>,
  // The HTTP runtime's credential sources fill the client's service credential.
  Holds<[ServiceTokenSource] extends [client.ServiceCredential['token']] ? true : false>,
];

test('the built client imports only its own modules', () => {
  const directory = new URL('../dist/client/', import.meta.url);
  const files = readdirSync(directory).filter((name) => name.endsWith('.js'));
  assert.ok(files.includes('index.js'));
  for (const file of files) {
    const source = readFileSync(new URL(file, directory), 'utf8');
    const specifiers = [...source.matchAll(/(?:^|\n)\s*(?:import|export)\b[^'"]*?from\s*['"]([^'"]+)['"]|import\(\s*['"]([^'"]+)['"]\s*\)/gu)].map(
      (match) => match[1] ?? match[2]
    );
    for (const specifier of specifiers) {
      assert.match(specifier, /^\.\/[a-z-]+\.js$/u, `${file} imports ${specifier}`);
    }
  }
});
