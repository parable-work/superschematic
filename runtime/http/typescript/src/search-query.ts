/*
The query syntax of search across tables (#740, D155): what searchTables
reads a query's text as, the same in the Go and Rust runtimes, and what it
asks Postgres and SQLite's FTS5 for.

A query is read once into a plan:

- A term is a quoted phrase ("walnut desk") or a run of characters that is
  neither white space nor a quote (walnut). A bare term that ends in *
  is a prefix (wal*). A term after - is excluded (-oak, -"oak chair"); a
  + before one says it is required, as every term is.
- OR, upper case, between two terms puts them in one group: a row holds
  one of a group's terms. A row matches when it holds every group and no
  excluded term. OR binds the terms beside it before the groups are
  joined: walnut oak OR pine is walnut, and oak or pine.
- A term with no letter or digit, an OR with no term before it, and an OR
  beside an excluded term are dropped. The first MAX_QUERY_TERMS terms
  count, each its first 200 characters.

A term's words are the database's, as before: on Postgres a term is
phraseto_tsquery of its text and a prefix to_tsquery of its text quoted
with :*, under the index's text search configuration; on SQLite each is a
quoted FTS5 phrase, a prefix with * after it. Every text is a bound value.
*/

/** One term of a query: its text, a phrase of the words the database finds in it, and whether its last word is a prefix. */
export interface SearchTerm {
  readonly text: string;
  readonly prefix: boolean;
}

/** A query read: groups a row must each hold one term of, and terms it must hold none of. */
export interface SearchPlan {
  readonly all: readonly (readonly SearchTerm[])[];
  readonly none: readonly SearchTerm[];
}

/** The most terms of a query that count, and the most characters of one. */
export const MAX_QUERY_TERMS = 16;
const MAX_TERM = 200;

const MARKS = /[\u0000\u0002\u0003]/gu;
const WHITE = /^\p{White_Space}$/u;
const LETTER_OR_DIGIT = /[\p{L}\p{N}]/u;

interface Token {
  readonly or?: true;
  readonly text: string;
  readonly prefix: boolean;
  readonly exclude: boolean;
}

function tokens(q: string): Token[] {
  const chars = Array.from(q.replace(MARKS, ''));
  const white = (c: string | undefined) => c !== undefined && WHITE.test(c);
  const out: Token[] = [];
  let i = 0;
  while (i < chars.length) {
    if (white(chars[i])) {
      i++;
      continue;
    }
    let sign = '';
    if ((chars[i] === '-' || chars[i] === '+') && i + 1 < chars.length && !white(chars[i + 1])) {
      sign = chars[i]!;
      i++;
    }
    let text = '';
    if (chars[i] === '"') {
      i++;
      while (i < chars.length && chars[i] !== '"') text += chars[i++];
      i++;
      out.push({ text, prefix: false, exclude: sign === '-' });
      continue;
    }
    while (i < chars.length && !white(chars[i]) && chars[i] !== '"') text += chars[i++];
    if (sign === '' && text === 'OR') {
      out.push({ or: true, text, prefix: false, exclude: false });
      continue;
    }
    const bare = text.replace(/\*+$/u, '');
    out.push({ text: bare, prefix: bare !== text, exclude: sign === '-' });
  }
  return out;
}

/** Reads a query's text into its plan (#740). */
export function searchPlan(q: string): SearchPlan {
  const all: SearchTerm[][] = [];
  const none: SearchTerm[] = [];
  let count = 0;
  let afterTerm = false;
  let join = false;
  for (const token of tokens(q)) {
    if (token.or) {
      if (afterTerm) join = true;
      continue;
    }
    if (count === MAX_QUERY_TERMS) break;
    if (!LETTER_OR_DIGIT.test(token.text)) continue;
    count++;
    const term: SearchTerm = { text: Array.from(token.text).slice(0, MAX_TERM).join(''), prefix: token.prefix };
    if (token.exclude) {
      none.push(term);
      afterTerm = false;
    } else {
      if (join && all.length > 0) all[all.length - 1]!.push(term);
      else all.push([term]);
      afterTerm = true;
    }
    join = false;
  }
  return { all, none };
}

/** A term as to_tsquery takes a prefix: its text quoted, a quote doubled and a backslash escaped, then :*. */
function prefixOperand(text: string): string {
  return `'${text.replaceAll('\\', '\\\\').replaceAll("'", "''")}':*`;
}

/**
 * The plan as a tsquery expression of Postgres's under config, a quoted
 * regconfig name: each term phraseto_tsquery or to_tsquery of a value
 * param binds, a group || of its terms, the groups && and each excluded
 * term && !!. The plan's none alone when it has no group, which a search
 * by vector alone excludes by.
 */
export function postgresQuery(plan: SearchPlan, config: string, param: (value: unknown) => string): string {
  const term = (t: SearchTerm) => (t.prefix ? `to_tsquery(${config}, ${param(prefixOperand(t.text))}::text)` : `phraseto_tsquery(${config}, ${param(t.text)}::text)`);
  const groups = plan.all.map(group => (group.length === 1 ? term(group[0]!) : `(${group.map(term).join(' || ')})`));
  if (groups.length === 0) return plan.none.map(term).join(' || ');
  return [...groups, ...plan.none.map(t => `!!${term(t)}`)].join(' && ');
}

/** A term as an FTS5 phrase: its text quoted, a quote doubled, and * after a prefix. */
function ftsPhrase(t: SearchTerm): string {
  return `"${t.text.replaceAll('"', '""')}"${t.prefix ? '*' : ''}`;
}

/**
 * The plan as an FTS5 expression: each term a quoted phrase, a group OR
 * of its terms, the groups AND and each excluded term NOT; null for a
 * plan with no group. columns, when given, is the column filter the whole
 * expression is under.
 */
export function ftsQuery(plan: SearchPlan, columns?: readonly string[]): string | null {
  if (plan.all.length === 0) return null;
  let expression = plan.all.map(group => (group.length === 1 ? ftsPhrase(group[0]!) : `(${group.map(ftsPhrase).join(' OR ')})`)).join(' AND ');
  if (plan.none.length > 0) expression = `(${expression})${plan.none.map(t => ` NOT ${ftsPhrase(t)}`).join('')}`;
  return columns ? `{${columns.join(' ')}} : (${expression})` : expression;
}
