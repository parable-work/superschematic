import { describe, expect, test } from 'bun:test';
import { sqliteDecodeRow, sqliteDecodeValue, sqliteEncodeParam, sqliteTimestamp } from './sqlitecodec';

/*
The SQLite value codec (D116): each field type as the SQLite DDL keeps it,
and read back as the Postgres codec reads it, so a repository's row is the
same on either.
*/

const UUID = '0f8b6c4e-1b2a-4c3d-9e8f-123456789abc';
const UUID62 = 'TKbHsHDO6x9RCjbs5c2u4';

describe('sqliteEncodeParam', () => {
  test('writes each field type as the SQLite DDL keeps it', () => {
    expect(sqliteEncodeParam(UUID62, { uuid: true })).toBe(UUID);
    expect(sqliteEncodeParam(UUID, { uuid: true })).toBe(UUID);
    expect(sqliteEncodeParam(true, { boolean: true })).toBe(1);
    expect(sqliteEncodeParam(false, { boolean: true })).toBe(0);
    expect(sqliteEncodeParam(new Date('2026-10-25T09:15:00.5Z'), { timestamp: true })).toBe('2026-10-25T09:15:00.500Z');
    // A timestamp's text moves to UTC, its fraction cut to the millisecond.
    expect(sqliteEncodeParam('2026-10-25T11:15:00.123456+02:00', { timestamp: true })).toBe('2026-10-25T09:15:00.123Z');
    expect(sqliteEncodeParam('2026-10-25', { date: true })).toBe('2026-10-25');
    expect(sqliteEncodeParam('90m', { interval: true })).toBe('1h30m0s');
    expect(sqliteEncodeParam('12.50', { numeric: true })).toBe('12.50');
    expect(sqliteEncodeParam(42, { int64: true })).toBe(42);
    expect(sqliteEncodeParam({ b: 1, a: [1, 'x'] }, { json: true })).toBe('{"b":1,"a":[1,"x"]}');
    expect(sqliteEncodeParam('x', { json: true })).toBe('"x"');
    expect(sqliteEncodeParam(['a', 'b'], { list: true })).toBe('["a","b"]');
    expect(sqliteEncodeParam([UUID62, null], { list: true, uuid: true })).toBe(`["${UUID}",null]`);
    expect(sqliteEncodeParam([true, false], { list: true, boolean: true })).toBe('[true,false]');
  });

  test('writes null as SQL NULL, but the JSON null in a JSON column that may not be null', () => {
    expect(sqliteEncodeParam(undefined, { json: true })).toBeNull();
    expect(sqliteEncodeParam(null, { json: true })).toBeNull();
    expect(sqliteEncodeParam(null, { json: true, required: true })).toBe('null');
    expect(sqliteEncodeParam(null, { uuid: true })).toBeNull();
  });

  test('refuses what SQLite has no storage for, and a value no column of its kind holds', () => {
    expect(() => sqliteEncodeParam({ lat: 1, lon: 2 }, { point: true })).toThrow('no storage for a location');
    expect(() => sqliteEncodeParam([0.5, 1], { vector: true })).toThrow('no storage for a vector');
    expect(() => sqliteEncodeParam('yes', { boolean: true })).toThrow('is no boolean');
    expect(() => sqliteEncodeParam(12, { timestamp: true })).toThrow('is no timestamp');
    expect(() => sqliteEncodeParam(2n ** 60n, { int64: true })).toThrow('out of the range');
  });

  test('a timestamp is the text a column default writes', () => {
    expect(sqliteTimestamp('2026-10-25T09:15:00Z')).toBe('2026-10-25T09:15:00.000Z');
    expect(() => sqliteTimestamp(new Date(Number.NaN))).toThrow('invalid');
  });
});

describe('sqliteDecodeValue', () => {
  test('reads each field type back as the Postgres codec reads it', () => {
    expect(sqliteDecodeValue(UUID, { uuid: true })).toBe(UUID62);
    expect(sqliteDecodeValue(1, { boolean: true })).toBe(true);
    expect(sqliteDecodeValue(0, { boolean: true })).toBe(false);
    expect(sqliteDecodeValue('2026-10-25T09:15:00.000Z', { timestamp: true })).toEqual(new Date('2026-10-25T09:15:00Z'));
    expect(sqliteDecodeValue('2026-10-25', { date: true })).toBe('2026-10-25');
    // NUMERIC affinity stores 12.50 as the number 12.5: a decimal's canonical text either way.
    expect(sqliteDecodeValue(12.5, { numeric: true })).toBe('12.5');
    expect(sqliteDecodeValue('12.50', { numeric: true })).toBe('12.5');
    expect(sqliteDecodeValue(7, { numeric: true })).toBe('7');
    expect(sqliteDecodeValue('1h30m0s', { interval: true })).toBe('1h30m0s');
    expect(sqliteDecodeValue('{"a":1}', { json: true })).toEqual({ a: 1 });
    expect(sqliteDecodeValue('"1"', { json: true })).toBe('1');
    expect(sqliteDecodeValue('null', { json: true, required: true })).toBeNull();
    expect(sqliteDecodeValue('["a","b"]', { list: true })).toEqual(['a', 'b']);
    expect(sqliteDecodeValue(`["${UUID}"]`, { list: true, uuid: true })).toEqual([UUID62]);
    expect(sqliteDecodeValue('["2026-10-25T09:15:00.000Z"]', { list: true, timestamp: true })).toEqual([new Date('2026-10-25T09:15:00Z')]);
    expect(sqliteDecodeValue(null, { uuid: true })).toBeNull();
  });

  test('a row is its fields by member, and a column the row does not hold is left out', () => {
    const fields = [
      { name: 'id', column: 'id', uuid: true },
      { name: 'paid', column: 'paid', boolean: true },
      { name: 'note', column: '"note"', json: true },
      { name: 'skipped', column: 'skipped' },
    ];
    expect(sqliteDecodeRow({ id: UUID, paid: 1, note: '"x"' }, fields)).toEqual({ id: UUID62, paid: true, note: 'x' });
    expect(() => sqliteDecodeRow({ note: 'x' }, fields)).toThrow('column note, field note');
  });
});
