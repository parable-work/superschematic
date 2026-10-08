import { describe, expect, test } from 'bun:test';
import { createLogger, type LoggerOptions } from './index';

const NOW = Date.UTC(2026, 9, 7, 12, 30, 0, 250);

function capture(options: LoggerOptions = {}) {
  const lines: string[] = [];
  return { lines, options: { now: () => NOW, write: (line: string) => lines.push(line), ...options } };
}

describe('createLogger', () => {
  test('writes one JSON object per line: level, time and msg, then the bound fields, then the call fields', () => {
    const { lines, options } = capture();
    const logger = createLogger({ stack: 'shop-stack', server: 'storefront' }, options);
    logger.info('listening', { port: 8080 });
    expect(lines).toEqual(['{"level":"info","time":"2026-10-07T12:30:00.250Z","msg":"listening","stack":"shop-stack","server":"storefront","port":8080}']);
  });

  test('child binds more fields, and a call field replaces a bound one but never level, time or msg', () => {
    const { lines, options } = capture();
    const logger = createLogger({ server: 'storefront' }, options).child({ api: 'shop-storefront' });
    logger.warn('slow', { api: 'other', level: 'debug', msg: 'x', time: 0 });
    expect(JSON.parse(lines[0]!)).toEqual({ level: 'warn', time: '2026-10-07T12:30:00.250Z', msg: 'slow', server: 'storefront', api: 'other' });
  });

  test('writes info and above by default, and the level the options name', () => {
    const quiet = capture();
    const logger = createLogger({}, quiet.options);
    logger.debug('hidden');
    logger.info('shown');
    logger.error('shown too');
    expect(quiet.lines.map(line => JSON.parse(line).msg)).toEqual(['shown', 'shown too']);
    const loud = capture({ level: 'debug' });
    createLogger({}, loud.options).debug('shown');
    const errorsOnly = capture({ level: 'error' });
    createLogger({}, errorsOnly.options).warn('hidden');
    expect(loud.lines).toHaveLength(1);
    expect(errorsOnly.lines).toHaveLength(0);
  });

  test('an Error is its message and a bigint its decimal, and a value JSON cannot hold drops the fields, not the line', () => {
    const { lines, options } = capture();
    const logger = createLogger({}, options);
    logger.error('failed', { error: new Error('connection refused'), rows: 12n });
    const cyclic: Record<string, unknown> = {};
    cyclic.self = cyclic;
    logger.error('cyclic', { cyclic });
    expect(JSON.parse(lines[0]!)).toEqual({ level: 'error', time: '2026-10-07T12:30:00.250Z', msg: 'failed', error: 'connection refused', rows: '12' });
    const second = JSON.parse(lines[1]!);
    expect(second.msg).toBe('cyclic');
    expect(second.logError).toStartWith("the line's fields are not JSON");
  });

  test('a field named __proto__ is a member of the line', () => {
    const { lines, options } = capture();
    createLogger({}, options).info('odd', JSON.parse('{"__proto__":{"x":1}}'));
    expect(lines[0]).toContain('"__proto__":{"x":1}');
  });

  test('writes to stdout through console.log by default', () => {
    const original = console.log;
    const seen: unknown[] = [];
    console.log = (...args: unknown[]) => seen.push(...args);
    try {
      createLogger().info('hello');
    } finally {
      console.log = original;
    }
    expect(seen).toHaveLength(1);
    expect(JSON.parse(seen[0] as string)).toMatchObject({ level: 'info', msg: 'hello' });
  });
});
