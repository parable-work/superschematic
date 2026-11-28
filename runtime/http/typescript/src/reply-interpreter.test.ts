import { describe, expect, test } from 'bun:test';
import { referenceInText } from './reply-interpreter';

describe('a reply the keyword reader does not read (#602)', () => {
  test('names its notice anywhere in its words as #n', () => {
    expect(referenceInText('the pump on #142 is fixed')).toBe(142);
    expect(referenceInText('#7, all done')).toBe(7);
    expect(referenceInText('fixed it.')).toBeUndefined();
    expect(referenceInText('room 142 is fixed')).toBeUndefined();
    expect(referenceInText('see a#142')).toBeUndefined();
  });
});
