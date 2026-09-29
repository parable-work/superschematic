// Run by TestTypeScriptEnvLoader against the fixture-env types package that
// superschematic generated into a temp tree. The import goes through the
// package's "./config" export, the way a service imports its loader.
import { describe, expect, test } from 'bun:test';
import { format, inspect } from 'node:util';
import {
  EnvConfigError,
  SECRET_MASK,
  SecretValue,
  loadFixtureEnvConfig,
} from '@schemas/fixture-env-types/config';

// Fake values only: nothing here is a credential.
const FAKE_SIGNING_KEY = 'fake-signing-key-for-loader-test';
const FAKE_PROVIDER_KEY = 'fake-provider-key-for-loader-test';

const required = {
  SERVICE_NAME: 'fixture',
  SIGNING_KEY: FAKE_SIGNING_KEY,
  UPSTREAM_URL: 'http://upstream.internal:9000',
};

function refusal(env: Record<string, string | undefined>): EnvConfigError {
  try {
    loadFixtureEnvConfig(env);
  } catch (error) {
    expect(error).toBeInstanceOf(EnvConfigError);
    return error as EnvConfigError;
  }
  throw new Error('expected loadFixtureEnvConfig to refuse the environment');
}

describe('loadFixtureEnvConfig', () => {
  test('parses every declared variable by its schema type', () => {
    const config = loadFixtureEnvConfig({
      ...required,
      PROVIDER_API_KEY: FAKE_PROVIDER_KEY,
      CALLBACK_URL: 'https://callback.internal/hook',
      PORT: '9001',
      MAX_TABS: '-3',
      ALLOW_VIDEO: 'true',
      PREVIEW_IMAGES: 'false',
      MODE: 'queued',
      VERSION: 'v1.2.3',
      UNDECLARED: 'ignored',
    });
    expect(config.SERVICE_NAME).toBe('fixture');
    expect(config.UPSTREAM_URL).toBe('http://upstream.internal:9000');
    expect(config.CALLBACK_URL).toBe('https://callback.internal/hook');
    expect(config.PORT).toBe(9001);
    expect(config.MAX_TABS).toBe(-3);
    expect(config.ALLOW_VIDEO).toBe(true);
    expect(config.PREVIEW_IMAGES).toBe(false);
    expect(config.MODE).toBe('queued');
    expect(config.VERSION).toBe('v1.2.3');
    expect(config.SIGNING_KEY.reveal()).toBe(FAKE_SIGNING_KEY);
    expect(config.PROVIDER_API_KEY?.reveal()).toBe(FAKE_PROVIDER_KEY);
    expect(Object.keys(config)).not.toContain('UNDECLARED');
  });

  test('applies schema defaults to unset and empty variables, and null to the rest', () => {
    const config = loadFixtureEnvConfig({ ...required, PORT: '', MODE: '' });
    expect(config.PORT).toBe(8095);
    expect(config.MODE).toBe('direct');
    expect(config.ALLOW_VIDEO).toBe(false);
    expect(config.CALLBACK_URL).toBe('http://localhost:8080');
    expect(config.MAX_TABS).toBeNull();
    expect(config.PREVIEW_IMAGES).toBeNull();
    expect(config.VERSION).toBeNull();
    expect(config.PROVIDER_API_KEY).toBeNull();
  });

  test('a missing required variable fails, naming the variable', () => {
    const error = refusal({ SERVICE_NAME: 'fixture', UPSTREAM_URL: '' });
    expect(error.problems.map(problem => problem.variable).sort()).toEqual([
      'SIGNING_KEY',
      'UPSTREAM_URL',
    ]);
    expect(error.message).toContain('required environment variable SIGNING_KEY is not set');
    expect(error.message).toContain('required environment variable UPSTREAM_URL is not set');
  });

  test('refuses invalid values, naming each variable and never echoing a value', () => {
    const invalid = {
      PORT: '80x',
      MAX_TABS: '1.5',
      ALLOW_VIDEO: 'yes',
      MODE: 'fast',
      CALLBACK_URL: 'not a url',
    };
    const error = refusal({ ...required, ...invalid });
    expect(error.problems.map(problem => problem.variable).sort()).toEqual(
      Object.keys(invalid).sort()
    );
    expect(error.message).toContain('environment variable PORT must be an integer');
    expect(error.message).toContain('environment variable ALLOW_VIDEO must be true or false');
    expect(error.message).toContain('environment variable MODE must be one of: direct, queued');
    expect(error.message).toContain('environment variable CALLBACK_URL is invalid');
    for (const value of Object.values(invalid)) {
      expect(error.message).not.toContain(value);
    }
  });

  test('a secret never shows in serialized, inspected or formatted config', () => {
    const config = loadFixtureEnvConfig({ ...required, PROVIDER_API_KEY: FAKE_PROVIDER_KEY });
    const renderings = [
      JSON.stringify(config),
      JSON.stringify({ config }),
      inspect(config, { depth: null, showHidden: true, getters: true }),
      format('%o %O %j', config, config, config),
      format('%s', config.SIGNING_KEY),
      String(config.SIGNING_KEY),
      `${config.SIGNING_KEY}`,
      `${config.PROVIDER_API_KEY}`,
      Bun.inspect(config),
      JSON.stringify(Object.entries(config)),
      JSON.stringify({ ...config }),
      JSON.stringify(Object.keys(config.SIGNING_KEY)),
    ];
    for (const rendering of renderings) {
      expect(rendering).not.toContain(FAKE_SIGNING_KEY);
      expect(rendering).not.toContain(FAKE_PROVIDER_KEY);
    }
    expect(JSON.parse(JSON.stringify(config)).SIGNING_KEY).toBe(SECRET_MASK);
    expect(inspect(config)).toContain(SECRET_MASK);
    expect(config.SIGNING_KEY).toBeInstanceOf(SecretValue);
    expect(config.SIGNING_KEY.reveal()).toBe(FAKE_SIGNING_KEY);
  });

  test('the loaded config is frozen', () => {
    const config = loadFixtureEnvConfig(required);
    expect(Object.isFrozen(config)).toBe(true);
    expect(() => {
      (config as { PORT: number }).PORT = 1;
    }).toThrow();
  });

  test('reads process.env when no map is given', () => {
    const saved = { ...process.env };
    try {
      Object.assign(process.env, required, { PORT: '7000' });
      expect(loadFixtureEnvConfig().PORT).toBe(7000);
    } finally {
      for (const key of Object.keys(process.env)) {
        if (!(key in saved)) delete process.env[key];
      }
      Object.assign(process.env, saved);
    }
  });
});
