// Writes confirm_vectors.json beside it: confirmations the TypeScript
// runtime's McpConfirmations minted (runtime/http/typescript/src/
// mcpconfirm.ts, D93) and the canonical JSON it binds arguments as, which
// the Rust runtime's tests/mcp_confirm.rs redeems and writes in turn. A
// token's nonce is random, so a run writes new tokens.
//
//   bun runtime/http/rust/tests/testdata/mcp/confirm_vectors.ts

import { writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { McpConfirmations, canonicalJson, type McpConfirmationMode } from '../../../../typescript/src/mcpconfirm';

const KEY = 'k'.repeat(16) + 'e'.repeat(16) + 'y';
const NOW = Date.UTC(2026, 9, 26, 12, 0, 0);
const TTL_SECONDS = 60;

// Each value as the JSON text a client sends, so a number keeps the form
// it was written in. A number is a whole number below 2^53 or has at most
// 15 significant digits: past them serde_json, without its float_roundtrip
// feature, may read the double beside the one JavaScript reads.
const VALUES = [
  '{"b":[1,{"d":2,"a":null}],"a":"x"}',
  '{"id":"i1","newName":"Desk"}',
  '{"newName":"Desk","id":"i1"}',
  '{}',
  '{"n":[0,-0,1.0,-1.5,100,1e2,1E3,0.1,0.000001,1e-7,1.25e-7,1e21,1.5e+21,123456789012345e6,1e22,9007199254740991,-9007199254740991,2.5e-10]}',
  '{"text":"\\u00e9\\"\\\\\\n\\t\\u0001\\u001f\\u007f\\u2028 / <>&","flag":true,"none":null}',
  '{"\\uff5e":1,"\\ud83d\\ude00":2,"\\ue000":3,"a":4,"B":5,"":6,"10":7,"9":8}',
  '{"where":{"quantity":{"in":[3,4],"gte":2},"name":"Hex bolt"},"tags":[["a"],[],{}]}',
];

const BINDINGS: { tool: string; caller: string; scope: string; mode: McpConfirmationMode; args: string }[] = [
  { tool: 'rename_item', caller: '["ada",null]', scope: 'items-api', mode: 'token', args: VALUES[1] },
  { tool: 'rename_item', caller: '["ada",null]', scope: 'items-api', mode: 'elicit', args: VALUES[2] },
  { tool: 'purge', caller: '', scope: '', mode: 'token', args: VALUES[3] },
  { tool: 'set_numbers', caller: '[null,"janitor"]', scope: 'numbers', mode: 'token', args: VALUES[4] },
  { tool: '\u00e9crire', caller: '["zo\u00eb","worker"]', scope: '\u00fcn\u00efcode-api', mode: 'elicit', args: VALUES[5] },
  { tool: 'sorted', caller: '["ada",null]', scope: 'items-api', mode: 'token', args: VALUES[6] },
  { tool: 'list_parts', caller: '["ada",null]', scope: 'items-api', mode: 'token', args: VALUES[7] },
];

const tokens = [];
for (const binding of BINDINGS) {
  const confirmations = new McpConfirmations({ key: KEY, scope: binding.scope, ttlSeconds: TTL_SECONDS, now: () => NOW });
  const { token, expiresAt } = await confirmations.mint(binding.mode, { tool: binding.tool, caller: binding.caller, args: JSON.parse(binding.args) });
  tokens.push({ ...binding, token, expiresAt });
}

const file = {
  comment:
    'Confirmations the TypeScript runtime minted (D93) and the canonical JSON it binds arguments as. Written by confirm_vectors.ts beside this file; tests/mcp_confirm.rs redeems each token with the Rust runtime and writes each canonical text.',
  key: KEY,
  now: NOW,
  ttlSeconds: TTL_SECONDS,
  canonical: VALUES.map(json => ({ json, canonical: canonicalJson(JSON.parse(json)) })),
  tokens,
};
// The file is ASCII: every other UTF-16 code unit is written as its escape.
const text = JSON.stringify(file, null, 2).replace(/[\u007f-\uffff]/g, unit => `\\u${unit.charCodeAt(0).toString(16).padStart(4, '0')}`);
writeFileSync(fileURLToPath(new URL('./confirm_vectors.json', import.meta.url)), `${text}\n`);
