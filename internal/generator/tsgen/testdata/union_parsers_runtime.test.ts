/*
Runtime check of the union parsers of the union-parsers types package (run by
TestUnionParsersBehavior inside the package). Figure picks its member by its
keys, Marker by its discriminator, and Contact by a tag between two
@strictJSON members; each member is read by its own parser, so a date-time
field is a Date and a @strictJSON member is checked. Drawing's parser reads
each union field the same way: a field, a list, a list of lists, a map and a
map of lists, and Shape, a union the shapes package declares.
*/
import { describe, expect, test } from 'bun:test';
import {
  ParseError,
  parseContactFromJSON,
  parseDrawingFromJSON,
  parseFigureFromJSON,
  parseMarkerFromJSON,
} from './index';

const when = '2026-01-02T03:04:05Z';

/** Runs parse and returns the ParseError it throws, failing when it throws none. */
function refusal(parse: () => unknown): ParseError {
  try {
    parse();
  } catch (error) {
    expect(error).toBeInstanceOf(ParseError);
    return error as ParseError;
  }
  throw new Error('expected a ParseError');
}

describe('a union without a discriminator picks the member that declares every key', () => {
  test('an imported member reads its date-time field as a Date', () => {
    const circle = parseFigureFromJSON({ radius: 1, drawnAt: when }) as { radius: number; drawnAt: Date };
    expect(circle.radius).toBe(1);
    expect(circle.drawnAt).toBeInstanceOf(Date);
    expect(circle.drawnAt.toISOString()).toBe('2026-01-02T03:04:05.000Z');
  });

  test('a later member is picked when an earlier one lacks a key', () => {
    expect(parseFigureFromJSON({ side: 2 })).toEqual({ side: 2 });
    const stamp = parseFigureFromJSON({ text: 'paid', at: when }) as { text: string; at: Date };
    expect(stamp.text).toBe('paid');
    expect(stamp.at).toBeInstanceOf(Date);
  });

  test('null and undefined are returned as they are, and any other non-object is refused', () => {
    expect(parseFigureFromJSON(null)).toBeNull();
    expect(parseFigureFromJSON(undefined)).toBeUndefined();
    expect(refusal(() => parseFigureFromJSON('circle')).reason).toBe('expected an object');
    expect(refusal(() => parseFigureFromJSON([{ side: 2 }])).reason).toBe('expected an object');
  });
});

describe('a tag tells apart members with the same fields', () => {
  test('the member whose tag the payload states is picked', () => {
    expect(parseContactFromJSON({ kind: 'sms', address: '555' })).toEqual({ kind: 'sms', address: '555' });
    expect(parseContactFromJSON({ kind: 'email', address: 'a@b' })).toEqual({ kind: 'email', address: 'a@b' });
  });

  test('a payload without the tag takes the first member, with its default, and is not changed', () => {
    const payload = { address: 'a@b' };
    expect(parseContactFromJSON(payload)).toEqual({ kind: 'email', address: 'a@b' });
    expect(payload).toEqual({ address: 'a@b' });
  });

  test('a tag no member fixes, or a key a @strictJSON member refuses, matches no member', () => {
    const fax = refusal(() => parseContactFromJSON({ kind: 'fax', address: '555' }));
    expect(fax.reason).toBe('no matching member');
    const extra = refusal(() => parseContactFromJSON({ kind: 'sms', address: '555', note: 'x' }));
    expect(extra.reason).toBe('no matching member');
  });
});

describe('a discriminated union reads the member its discriminator names', () => {
  test('a member reads its date-time field as a Date', () => {
    const flag = parseMarkerFromJSON({ kind: 'flag', raisedAt: when }) as { kind: string; raisedAt: Date };
    expect(flag.kind).toBe('flag');
    expect(flag.raisedAt).toBeInstanceOf(Date);
    expect(parseMarkerFromJSON({ kind: 'pin', label: 'here' })).toEqual({ kind: 'pin', label: 'here' });
  });

  test('a @strictJSON member refuses an unknown key and a missing required field', () => {
    const unknown = refusal(() => parseMarkerFromJSON({ kind: 'pin', label: 'here', note: 'x' }));
    expect(unknown.reason).toBe('unknown fields: note');
    const missing = refusal(() => parseMarkerFromJSON({ kind: 'pin' }));
    expect(missing.reason).toBe('validation failed');
    expect(missing.errors).toEqual({ label: [{ validator: 'required', message: 'required field' }] });
  });

  test('an absent, non-string or unknown discriminator is refused', () => {
    for (const payload of [{}, { kind: 1 }, { kind: 'star' }]) {
      expect(refusal(() => parseMarkerFromJSON(payload)).reason).toBe('unknown discriminator');
    }
    expect(refusal(() => parseMarkerFromJSON({ kind: 'star' })).message).toBe(
      'parseMarker: discriminator kind is "star", not one of "pin", "flag"'
    );
  });
});

describe("a model's parser reads every union field through the union", () => {
  const drawing = parseDrawingFromJSON({
    shape: { radius: 2, drawnAt: when },
    figures: [{ radius: 1, drawnAt: when }, { text: 'paid', at: when }],
    rows: [[{ text: 'a', at: when }], [], [{ side: 3 }]],
    byName: { round: { radius: 1, drawnAt: when }, none: null },
    listsByName: { stamps: [{ text: 'b', at: when }] },
    marker: { kind: 'flag', raisedAt: when },
    contacts: [{ kind: 'sms', address: '555' }],
  }) as any;

  test('a field of a union another package declares', () => {
    expect(drawing.shape.drawnAt).toBeInstanceOf(Date);
  });

  test('an absent optional field stays undefined, and a null one null', () => {
    expect(drawing.figure).toBeUndefined();
    expect(parseDrawingFromJSON({ shape: { side: 1 }, figures: [], figure: null }).figure).toBeNull();
  });

  test('a list, a list of lists, a map and a map of lists', () => {
    expect(drawing.figures[0].drawnAt).toBeInstanceOf(Date);
    expect(drawing.figures[1].at).toBeInstanceOf(Date);
    expect(drawing.rows[0][0].at).toBeInstanceOf(Date);
    expect(drawing.rows[1]).toEqual([]);
    expect(drawing.rows[2][0]).toEqual({ side: 3 });
    expect(drawing.byName.round.drawnAt).toBeInstanceOf(Date);
    expect(drawing.byName.none).toBeNull();
    expect(drawing.listsByName.stamps[0].at).toBeInstanceOf(Date);
  });

  test('a discriminated union field and a list of a tagged union', () => {
    expect(drawing.marker.raisedAt).toBeInstanceOf(Date);
    expect(drawing.contacts).toEqual([{ kind: 'sms', address: '555' }]);
  });

  test('a union value no member reads refuses the model', () => {
    const error = refusal(() => parseDrawingFromJSON({ shape: { side: 1 }, figures: [], contacts: [{ kind: 'fax', address: '555' }] }));
    expect(error.reason).toBe('no matching member');
    expect(refusal(() => parseDrawingFromJSON({ shape: { side: 1 }, figures: [], marker: { kind: 'pin', label: 'x', note: 'y' } })).reason).toBe(
      'unknown fields: note'
    );
  });
});
