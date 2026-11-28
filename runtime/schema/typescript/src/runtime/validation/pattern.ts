// A schema pattern is RE2 syntax, as Go's regexp reads it. This rewrites
// one so a RegExp with the u flag matches what RE2 matches: a port of
// internal/generator/patterndialect (its JavaScript target), whose
// comment lists what changes. In short: \s is [\t\n\f\r ], "." refuses
// \n alone, ^ and $ are the text's ends (a line's under (?m), and a line
// ends at \n alone), \A and \z are written ^ and $, (?m), (?s) and (?U)
// are applied by rewriting, (?i) becomes a scoped (?i:...) group (the i
// flag when the pattern opens with it), \pL is \p{L} and \p{Greek}
// \p{Script=Greek}, [[:alpha:]] is ASCII, and \Q...\E, \x{...}, octal,
// \a, escaped punctuation and a { that opens no repetition are literals.
// runtime/schema/testdata/pattern_dialect.json holds the cases every
// runtime checks.

const MAX_CODE_POINT = 0x10ffff;

type Range = readonly [number, number];

interface Flags {
  i: boolean;
  m: boolean;
  s: boolean;
  ungreedy: boolean;
}

interface Frame {
  flags: Flags;
  baseI: boolean;
  mods: number;
}

const POSIX_CLASSES: Record<string, Range[]> = {
  alnum: [[0x30, 0x39], [0x41, 0x5a], [0x61, 0x7a]],
  alpha: [[0x41, 0x5a], [0x61, 0x7a]],
  ascii: [[0, 0x7f]],
  blank: [[0x09, 0x09], [0x20, 0x20]],
  cntrl: [[0, 0x1f], [0x7f, 0x7f]],
  digit: [[0x30, 0x39]],
  graph: [[0x21, 0x7e]],
  lower: [[0x61, 0x7a]],
  print: [[0x20, 0x7e]],
  punct: [[0x21, 0x2f], [0x3a, 0x40], [0x5b, 0x60], [0x7b, 0x7e]],
  space: [[0x09, 0x0d], [0x20, 0x20]],
  upper: [[0x41, 0x5a]],
  word: [[0x30, 0x39], [0x41, 0x5a], [0x5f, 0x5f], [0x61, 0x7a]],
  xdigit: [[0x30, 0x39], [0x41, 0x46], [0x61, 0x66]],
};

const SPACE: Range[] = [[0x09, 0x0a], [0x0c, 0x0d], [0x20, 0x20]];

// The general categories RE2 names; any other name but Any is a script.
const CATEGORIES = new Set(
  'C Cc Cf Cn Co Cs L LC Ll Lm Lo Lt Lu M Mc Me Mn N Nd Nl No P Pc Pd Pe Pf Pi Po Ps S Sc Sk Sm So Z Zl Zp Zs'.split(' '),
);

const cache = new Map<string, { source: string; flags: string }>();

/** The source and flags of a RegExp that matches what RE2 matches for pattern. */
export function translatePattern(pattern: string): { source: string; flags: string } {
  let translated = cache.get(pattern);
  if (translated === undefined) {
    translated = new Translator(pattern).run();
    cache.set(pattern, translated);
  }
  return translated;
}

/** The pattern compiled to match as RE2 does; throws when it does not compile. */
export function compilePattern(pattern: string): RegExp {
  const { source, flags } = translatePattern(pattern);
  return new RegExp(source, flags);
}

function perlClass(c: string): Range[] {
  switch (c.toLowerCase()) {
    case 'd':
      return POSIX_CLASSES.digit;
    case 'w':
      return POSIX_CLASSES.word;
    default:
      return SPACE;
  }
}

function complement(ranges: Range[]): Range[] {
  const out: Range[] = [];
  let next = 0;
  for (const [lo, hi] of ranges) {
    if (lo > next) out.push([next, lo - 1]);
    next = hi + 1;
  }
  if (next <= MAX_CODE_POINT) out.push([next, MAX_CODE_POINT]);
  return out;
}

const isHex = (c: string | undefined): boolean => c !== undefined && /^[0-9A-Fa-f]$/u.test(c);
const isOctal = (c: string | undefined): boolean => c !== undefined && c >= '0' && c <= '7';
const isAlnum = (c: string): boolean => /^[0-9A-Za-z]$/u.test(c);

/** Whether RegExp's u flag reads the escaped punctuation mark \\c as the mark: its syntax characters, / and, in a class, -. */
const keepsEscape = (c: string, inClass: boolean): boolean => '^$\\.*+?()[]{}|/'.includes(c) || (inClass && c === '-');

/** The length of the {n}, {n,} or {n,m} that rest (after its {) opens, through its }, or 0. */
function repetition(rest: string[]): number {
  let i = 0;
  const digits = (): number => {
    const start = i;
    while (i < rest.length && rest[i] >= '0' && rest[i] <= '9') i++;
    return i - start;
  };
  if (digits() === 0) return 0;
  if (rest[i] === ',') {
    i++;
    digits();
  }
  return rest[i] === '}' ? i + 1 : 0;
}

class Translator {
  private readonly src: string[];
  private pos = 0;
  private out = '';
  private readonly stack: Frame[] = [{ flags: { i: false, m: false, s: false, ungreedy: false }, baseI: false, mods: 0 }];
  private caseless = false;

  constructor(pattern: string) {
    this.src = Array.from(pattern);
  }

  run(): { source: string; flags: string } {
    // A pattern that opens with (?i) is caseless throughout: the i flag.
    if (this.src[0] === '(' && this.src[1] === '?') {
      const leading = this.parseFlags(2, this.stack[0].flags);
      if (leading && leading.closer === ')' && leading.flags.i) {
        this.caseless = true;
        this.stack[0].flags.i = true;
        this.stack[0].baseI = true;
      }
    }
    while (this.pos < this.src.length) {
      const c = this.src[this.pos++];
      const top = this.stack[this.stack.length - 1];
      switch (c) {
        case '\\':
          this.escape();
          break;
        case '[':
          this.cls();
          break;
        case '(':
          this.open();
          break;
        case ')':
          this.close();
          break;
        case '|':
          this.closeMods(top);
          this.out += '|';
          if (top.flags.i !== top.baseI) this.openMod(top);
          break;
        case '.':
          this.out += top.flags.s ? '[\\s\\S]' : '[^\\n]';
          break;
        case '^':
          this.out += top.flags.m ? '(?<![^\\n])' : '^';
          break;
        case '$':
          this.out += top.flags.m ? '(?![^\\n])' : '$';
          break;
        case '*':
        case '+':
        case '?':
          this.out += c;
          this.quantifierSuffix(top);
          break;
        case '{': {
          const n = repetition(this.src.slice(this.pos));
          if (n > 0) {
            this.out += '{' + this.src.slice(this.pos, this.pos + n).join('');
            this.pos += n;
            this.quantifierSuffix(top);
          } else {
            this.out += '\\{';
          }
          break;
        }
        default:
          this.literal(c);
      }
    }
    this.closeMods(this.stack[0]);
    return { source: this.out, flags: this.caseless ? 'iu' : 'u' };
  }

  private quantifierSuffix(top: Frame): void {
    let lazy = this.src[this.pos] === '?';
    if (lazy) this.pos++;
    if (top.flags.ungreedy) lazy = !lazy;
    if (lazy) this.out += '?';
  }

  private parseFlags(at: number, base: Flags): { flags: Flags; end: number; closer: string } | null {
    const flags = { ...base };
    let on = true;
    let letters = 0;
    for (let i = at; i < this.src.length; i++) {
      const c = this.src[i];
      switch (c) {
        case 'i':
          flags.i = on;
          break;
        case 'm':
          flags.m = on;
          break;
        case 's':
          flags.s = on;
          break;
        case 'U':
          flags.ungreedy = on;
          break;
        case '-':
          if (!on) return null;
          on = false;
          letters = 0;
          continue;
        case ')':
        case ':':
          if ((c === ')' && letters === 0 && on) || (!on && letters === 0)) return null;
          return { flags, end: i + 1, closer: c };
        default:
          return null;
      }
      letters++;
    }
    return null;
  }

  private open(): void {
    const parent = this.stack[this.stack.length - 1];
    const rest = this.src.slice(this.pos).join('');
    if (rest.startsWith('?P<') || (rest.startsWith('?<') && !rest.startsWith('?<=') && !rest.startsWith('?<!'))) {
      const start = rest.indexOf('<') + 1;
      const end = rest.indexOf('>');
      if (end > start) {
        this.out += '(?<' + rest.slice(start, end) + '>';
        this.pos += Array.from(rest.slice(0, end + 1)).length;
        this.stack.push({ flags: { ...parent.flags }, baseI: parent.flags.i, mods: 0 });
        return;
      }
    }
    if (rest.startsWith('?')) {
      const group = this.parseFlags(this.pos + 1, parent.flags);
      if (group) {
        this.pos = group.end;
        if (group.closer === ')') {
          const was = parent.flags.i;
          parent.flags = group.flags;
          if (group.flags.i !== was) this.openMod(parent);
          return;
        }
        this.out += group.flags.i === parent.flags.i ? '(?:' : group.flags.i ? '(?i:' : '(?-i:';
        this.stack.push({ flags: group.flags, baseI: group.flags.i, mods: 0 });
        return;
      }
    }
    this.out += '(';
    this.stack.push({ flags: { ...parent.flags }, baseI: parent.flags.i, mods: 0 });
  }

  private openMod(top: Frame): void {
    this.out += top.flags.i ? '(?i:' : '(?-i:';
    top.mods++;
  }

  private closeMods(top: Frame): void {
    this.out += ')'.repeat(top.mods);
    top.mods = 0;
  }

  private close(): void {
    if (this.stack.length > 1) {
      this.closeMods(this.stack[this.stack.length - 1]);
      this.stack.pop();
    }
    this.out += ')';
  }

  private escape(): void {
    if (this.pos >= this.src.length) {
      this.out += '\\';
      return;
    }
    const c = this.src[this.pos++];
    switch (c) {
      case 'd':
      case 'w':
      case 'D':
      case 'W':
      case 'b':
      case 'B':
        this.out += '\\' + c;
        return;
      case 's':
      case 'S':
        this.set(SPACE, c === 'S');
        return;
      case 'A':
        this.out += '^';
        return;
      case 'z':
        this.out += '$';
        return;
      case 'p':
      case 'P':
        this.property(c === 'P');
        return;
      case 'Q':
        while (this.pos < this.src.length) {
          if (this.src[this.pos] === '\\' && this.src[this.pos + 1] === 'E') {
            this.pos += 2;
            return;
          }
          this.literal(this.src[this.pos++]);
        }
        return;
      default: {
        if (keepsEscape(c, false)) {
          this.out += '\\' + c;
          return;
        }
        const cp = this.escapedChar(c);
        if (cp === null) this.out += '\\' + c;
        else this.literal(String.fromCodePoint(cp));
      }
    }
  }

  /** The one character the escape \c (c read) stands for, or null. */
  private escapedChar(c: string): number | null {
    if (c === 'x') {
      if (this.src[this.pos] === '{') {
        let end = this.pos + 1;
        while (isHex(this.src[end])) end++;
        if (end === this.pos + 1 || this.src[end] !== '}') return null;
        const cp = parseInt(this.src.slice(this.pos + 1, end).join(''), 16);
        this.pos = end + 1;
        return cp <= MAX_CODE_POINT ? cp : null;
      }
      if (isHex(this.src[this.pos]) && isHex(this.src[this.pos + 1])) {
        const cp = parseInt(this.src[this.pos] + this.src[this.pos + 1], 16);
        this.pos += 2;
        return cp;
      }
      return null;
    }
    if (isOctal(c)) {
      // A lone \1 to \7 is a backreference, which RE2 refuses.
      if (c !== '0' && !isOctal(this.src[this.pos])) return null;
      let cp = Number(c);
      for (let n = 0; n < 2 && isOctal(this.src[this.pos]); n++) cp = cp * 8 + Number(this.src[this.pos++]);
      return cp;
    }
    const control = 'aftnrv'.indexOf(c);
    if (control >= 0) return [7, 12, 9, 10, 13, 11][control];
    if (c.length === 1 && c.charCodeAt(0) < 0x80 && !isAlnum(c)) return c.charCodeAt(0);
    return null;
  }

  private literal(c: string): void {
    if ('\\.+*?()|[]{}^$'.includes(c)) this.out += '\\' + c;
    else this.out += this.char(c.codePointAt(0)!, false);
  }

  private char(cp: number, escape: boolean): string {
    if (cp < 0x20 || cp === 0x7f || (cp > 0x7e && escape)) return codePoint(cp);
    return String.fromCodePoint(cp);
  }

  private classChar(cp: number, escape: boolean): string {
    const c = String.fromCodePoint(cp);
    if ('\\]-[^'.includes(c)) return '\\' + c;
    return this.char(cp, escape);
  }

  private set(ranges: Range[], negated: boolean): void {
    this.out += (negated ? '[^' : '[') + this.ranges(ranges) + ']';
  }

  private ranges(ranges: Range[]): string {
    let text = '';
    for (const [lo, hi] of ranges) {
      text += this.classChar(lo, true);
      if (hi > lo) text += (hi > lo + 1 ? '-' : '') + this.classChar(hi, true);
    }
    return text;
  }

  private cls(): void {
    this.out += '[';
    if (this.src[this.pos] === '^') {
      this.out += '^';
      this.pos++;
    }
    let first = true;
    while (this.pos < this.src.length) {
      if (this.src[this.pos] === ']' && !first) {
        this.pos++;
        this.out += ']';
        return;
      }
      // A - first or last is a literal as it stands.
      if (this.src[this.pos] === '-' && (first || this.src[this.pos + 1] === ']')) {
        this.pos++;
        this.out += '-';
        first = false;
        continue;
      }
      first = false;
      if (this.classSet()) continue;
      this.out += this.classMember();
      if (this.src[this.pos] === '-' && this.pos + 1 < this.src.length && this.src[this.pos + 1] !== ']') {
        // A range. RE2 refuses one that ends in a class, as RegExp refuses
        // what this writes for it.
        this.pos++;
        this.out += '-' + this.classMember();
      }
    }
  }

  private classMember(): string {
    const c = this.src[this.pos++];
    if (c !== '\\') return this.classChar(c.codePointAt(0)!, false);
    if (this.pos >= this.src.length) return '\\';
    const e = this.src[this.pos++];
    if (keepsEscape(e, true)) return '\\' + e;
    const cp = this.escapedChar(e);
    return cp === null ? '\\' + e : this.classChar(cp, false);
  }

  /** Writes the class inside a class at pos, if one starts there. */
  private classSet(): boolean {
    const c0 = this.src[this.pos];
    const c1 = this.src[this.pos + 1];
    if (c0 === '[' && c1 === ':') {
      const rest = this.src.slice(this.pos + 2).join('');
      const end = rest.indexOf(':]');
      if (end < 0) return false;
      const name = rest.slice(0, end);
      const negated = name.startsWith('^');
      const key = negated ? name.slice(1) : name;
      if (!Object.prototype.hasOwnProperty.call(POSIX_CLASSES, key)) return false;
      this.pos += 2 + Array.from(name).length + 2;
      this.out += this.ranges(negated ? complement(POSIX_CLASSES[key]) : POSIX_CLASSES[key]);
      return true;
    }
    if (c0 !== '\\' || c1 === undefined) return false;
    switch (c1) {
      case 'd':
      case 'w':
      case 'D':
      case 'W':
        this.pos += 2;
        this.out += '\\' + c1;
        return true;
      case 's':
      case 'S':
        this.pos += 2;
        this.out += this.ranges(c1 === 'S' ? complement(SPACE) : SPACE);
        return true;
      case 'p':
      case 'P':
        this.pos += 2;
        this.property(c1 === 'P');
        return true;
      default:
        return false;
    }
  }

  /** \pX, \p{Name} or \p{^Name}, its \p or \P read. */
  private property(negated: boolean): void {
    if (this.pos >= this.src.length) {
      this.out += '\\p';
      return;
    }
    let name: string;
    if (this.src[this.pos] === '{') {
      const rest = this.src.slice(this.pos).join('');
      const end = rest.indexOf('}');
      if (end < 0) {
        this.out += '\\p';
        return;
      }
      name = rest.slice(1, end);
      this.pos += Array.from(rest.slice(0, end + 1)).length;
    } else {
      name = this.src[this.pos++];
    }
    if (name.startsWith('^')) {
      negated = !negated;
      name = name.slice(1);
    }
    if (name !== 'Any' && !CATEGORIES.has(name)) name = 'Script=' + name;
    this.out += (negated ? '\\P{' : '\\p{') + name + '}';
  }
}

function codePoint(cp: number): string {
  const short = [9, 10, 11, 12, 13].indexOf(cp);
  if (short >= 0) return '\\' + 'tnvfr'[short];
  const hex = cp.toString(16);
  if (cp < 0x100) return '\\x' + hex.padStart(2, '0');
  if (cp < 0x10000) return '\\u' + hex.padStart(4, '0');
  return '\\u{' + hex + '}';
}
