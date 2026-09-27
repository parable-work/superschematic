// The ESM build loads a schema file against the default meta-schema, which
// it imports as a JSON module from @superschematic/schema-ir.
/* eslint-disable no-console */
import assert from 'node:assert';

import { loadSchemaFile } from '../dist/esm/index.js';

const loaded = loadSchemaFile(
  '{"kind": "OperationSet", "name": "Ops", "operations": [{"name": "getOrder", "typeRef": {"name": "Order"}, "mcp": {"handle": "get_order", "hidden": false}}]}'
);
assert.strictEqual(
  loaded.canonical,
  '{"operationSets":[{"name":"Ops","operations":[{"mcp":{"handle":"get_order","hidden":false,"invocationPolicy":"auto"},"name":"getOrder","typeRef":{"name":"Order"}}]}]}'
);
console.log('schema-file-esm: ok');
