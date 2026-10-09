import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import {
  capabilitiesOf,
  clearCookie,
  crossOriginAllowed,
  effectivePermissions,
  extractCredential,
  hashPasswordWithSalt,
  hashToken,
  isPassword,
  newToken,
  parseIdentityConfig,
  parseIdentityConfigJSON,
  sessionCookie,
  uncovered,
  validPermission,
  verifyPassword,
  type Argon2Params,
  type IdentityRoute,
} from './index';

/*
The identity parity vectors (D50), which the Go runtime writes (`go test
./identity -run TestWriteParityVectors -update` in runtime/http/go) and the
Go, TypeScript and Rust runtimes each run. runtime/http/testdata/README.md
states each section's harness; every case of every section runs here, and
a section this file does not know fails, so a new one is not skipped.
*/

const CORPUS = fileURLToPath(new URL('../../../testdata/identity_parity.json', import.meta.url));
const corpus = JSON.parse(readFileSync(CORPUS, 'utf8')) as Record<string, unknown>;

interface Named {
  readonly name: string;
}

function section<T>(name: string): T[] {
  const cases = corpus[name];
  if (!Array.isArray(cases)) throw new Error(`the corpus has no section ${name}`);
  return cases as T[];
}

function refused(run: () => unknown): boolean {
  try {
    run();
    return false;
  } catch {
    return true;
  }
}

function base64Bytes(text: string): Uint8Array {
  return new Uint8Array(Buffer.from(text, 'base64'));
}

const SECTIONS = [
  'config',
  'passwordRule',
  'hashes',
  'verify',
  'tokens',
  'cookies',
  'credentials',
  'crossOrigin',
  'permissionNames',
  'effectivePermissions',
  'grants',
  'capabilities',
];

test('the corpus has the sections this harness runs, and no other', () => {
  expect(Object.keys(corpus).filter(key => key !== 'comment').sort()).toEqual([...SECTIONS].sort());
});

describe('config', () => {
  for (const c of section<Named & { input: unknown; want: unknown }>('config')) {
    test(c.name, () => {
      if (c.want === null) {
        expect(refused(() => parseIdentityConfig(c.input))).toBe(true);
        expect(refused(() => parseIdentityConfigJSON(JSON.stringify(c.input)))).toBe(true);
        return;
      }
      expect(parseIdentityConfig(c.input)).toEqual(c.want as never);
      expect(parseIdentityConfigJSON(JSON.stringify(c.input))).toEqual(c.want as never);
    });
  }
});

describe('passwordRule', () => {
  for (const c of section<Named & { password: string; valid: boolean }>('passwordRule')) {
    test(c.name, () => {
      expect(isPassword(c.password)).toBe(c.valid);
    });
  }
});

describe('hashes', () => {
  for (const c of section<Named & { password: string; salt: string; params: Argon2Params; phc: string }>('hashes')) {
    test(c.name, async () => {
      expect(await hashPasswordWithSalt(c.password, base64Bytes(c.salt), c.params)).toBe(c.phc);
    });
  }
});

describe('verify', () => {
  for (const c of section<Named & { phc: string; password: string; current: Argon2Params; want: unknown }>('verify')) {
    test(c.name, async () => {
      expect(await verifyPassword(c.phc, c.password, c.current)).toEqual(c.want as never);
    });
  }
});

describe('tokens', () => {
  for (const c of section<Named & { bytes: string; token: string; hash: string }>('tokens')) {
    test(c.name, () => {
      expect(newToken(() => new Uint8Array(Buffer.from(c.bytes, 'hex')))).toBe(c.token);
      expect(hashToken(c.token)).toBe(c.hash);
    });
  }
});

describe('cookies', () => {
  for (const c of section<Named & { config: unknown; token: string; maxAgeSeconds: number; want: { set: string; clear: string } }>('cookies')) {
    test(c.name, () => {
      const config = parseIdentityConfig(c.config);
      expect({ set: sessionCookie(config, c.token, c.maxAgeSeconds), clear: clearCookie(config) }).toEqual(c.want);
    });
  }
});

describe('credentials', () => {
  for (const c of section<Named & { cookieName: string; headers: [string, string][]; want: unknown }>('credentials')) {
    test(c.name, () => {
      expect(extractCredential(c.headers, c.cookieName)).toEqual(c.want as never);
    });
  }
});

describe('crossOrigin', () => {
  for (const c of section<Named & { method: string; host: string; headers: Record<string, string>; trustedOrigins: string[]; want: string }>(
    'crossOrigin'
  )) {
    test(c.name, () => {
      const pairs = crossOriginAllowed(c.trustedOrigins, { method: c.method, host: c.host, headers: Object.entries(c.headers) });
      // The same request as fetch's Headers carries it.
      const fetched = crossOriginAllowed(c.trustedOrigins, { method: c.method, host: c.host, headers: new Headers(c.headers) });
      expect([pairs, fetched].map(allowed => (allowed ? 'allow' : 'refuse'))).toEqual([c.want, c.want]);
    });
  }
});

describe('permissionNames', () => {
  for (const c of section<{ permission: string; valid: boolean }>('permissionNames')) {
    test(JSON.stringify(c.permission), () => {
      expect(validPermission(c.permission)).toBe(c.valid);
    });
  }
});

describe('effectivePermissions', () => {
  for (const c of section<Named & { roles: { name: string; permissions: string[] }[]; want: string[] }>('effectivePermissions')) {
    test(c.name, () => {
      expect(effectivePermissions(c.roles)).toEqual(c.want);
    });
  }
});

describe('grants', () => {
  for (const c of section<Named & { held: string[]; given: string[]; want: { allowed: boolean; uncovered: string[] } }>('grants')) {
    test(c.name, () => {
      const missing = uncovered(c.held, c.given);
      expect({ allowed: missing.length === 0, uncovered: missing }).toEqual(c.want);
    });
  }
});

describe('capabilities', () => {
  const capabilities = corpus.capabilities as {
    routes: IdentityRoute[];
    callers: (Named & { permissions: string[]; want: Record<string, boolean> })[];
  };
  test('the section has routes and callers', () => {
    expect(capabilities.routes.length).toBeGreaterThan(0);
    expect(capabilities.callers.length).toBeGreaterThan(0);
  });
  for (const c of capabilities.callers) {
    test(c.name, () => {
      expect(capabilitiesOf(capabilities.routes, c.permissions)).toEqual(c.want);
    });
  }
});
