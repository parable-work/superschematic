import { describe, expect, test } from 'bun:test';
import { NotifierError, createNotifier, normaliseAddress, quietDelaySeconds, sameId } from './notify.js';

// The notifier's pure parts (D153). Its routing, deliveries and replies run
// against Postgres in TestNotificationsEndToEnd (internal/generator/tsgen).

describe('normaliseAddress', () => {
  test('an email address is lower case, a phone number + and its digits, a handle trimmed', () => {
    expect(normaliseAddress('email', ' Ann@Example.TEST ')).toBe('ann@example.test');
    expect(normaliseAddress('phone', '+1 (555) 010-0001')).toBe('+15550100001');
    expect(normaliseAddress('phone', '0044 20 7946 0000')).toBe('+442079460000');
    expect(normaliseAddress('handle', ' @ann ')).toBe('@ann');
  });
});

describe('quietDelaySeconds', () => {
  const at = (iso: string) => new Date(iso);
  test('a time inside a window across midnight waits for its end, in the zone', () => {
    expect(quietDelaySeconds(at('2027-03-10T23:30:00Z'), 'UTC', '22:00', '07:00')).toBe(7.5 * 3600);
    expect(quietDelaySeconds(at('2027-03-11T06:59:00Z'), 'UTC', '22:00', '07:00')).toBe(60);
    // 23:30 UTC is 00:30 in Paris in March, still quiet there.
    expect(quietDelaySeconds(at('2027-03-10T23:30:00Z'), 'Europe/Paris', '22:00', '07:00')).toBe(6.5 * 3600);
  });
  test('a time outside the window, or no window, waits for nothing', () => {
    expect(quietDelaySeconds(at('2027-03-10T12:00:00Z'), 'UTC', '22:00', '07:00')).toBe(0);
    expect(quietDelaySeconds(at('2027-03-10T12:00:00Z'), 'UTC', '12:00', '13:00')).toBe(3600);
    expect(quietDelaySeconds(at('2027-03-10T12:00:00Z'), 'UTC', null, null)).toBe(0);
    expect(quietDelaySeconds(at('2027-03-10T12:00:00Z'), 'UTC', '09:00', '09:00')).toBe(0);
  });
  test('a window stored as a Temporal.Time, HH:MM:SS, reads as its HH:MM', () => {
    expect(quietDelaySeconds(at('2027-03-10T23:30:00Z'), 'UTC', '22:00:00', '07:00:00')).toBe(7.5 * 3600);
    expect(quietDelaySeconds(at('2027-03-10T12:00:00Z'), 'UTC', '12:00:00', '13:00:00')).toBe(3600);
  });
});

describe('sameId', () => {
  test('a UUID reads alike in either form', () => {
    const hyphenated = '0f8b6c4e-1b2a-4c3d-9e8f-123456789abc';
    const base62 = sameId(hyphenated);
    expect(base62).not.toBe(hyphenated);
    expect(sameId(base62)).toBe(base62);
    expect(sameId(hyphenated.toUpperCase())).toBe(base62);
    expect(sameId('SMS')).toBe('SMS');
  });
});

describe('createNotifier', () => {
  test('an ORM without the parts routing writes is refused', () => {
    expect(() => createNotifier({ orm: {}, dispatch: {} as never })).toThrow('@notifier("inbox")');
  });
  test('its errors are NotifierErrors', () => {
    expect(new NotifierError('x').name).toBe('NotifierError');
  });
});
