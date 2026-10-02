/*
The SQL a behavior may run: one statement at a time, on its own tables.
Before a behavior's statement reaches SQLite, the engine reads its tokens
(skipping comments, blob and number literals and parameters) and refuses:

- more than one statement;
- any identifier, bare or quoted, that starts with a prefix the engine
  reserves (engine_, sqlite_, pragma_, bhv_) unless it starts with the
  behavior's own prefix, bhv_<key>__. SQLite matches identifiers without
  regard to ASCII case, so the check does too. A table is reached only by
  naming it, so this keeps a behavior off the instances table, the event
  log, the schema catalog, SQLite's own tables and every other behavior's
  storage. A string literal is checked the same way, since SQLite reads
  one as a name where a name is expected (`FROM 'engine_instances'`); a
  behavior passes data as parameters;
- load_extension;
- a statement kind the mode does not allow. A read runs SELECT, VALUES or
  WITH ... SELECT; a write adds INSERT, UPDATE, DELETE and REPLACE; a
  migration adds CREATE TABLE, CREATE [UNIQUE] INDEX, CREATE VIRTUAL TABLE,
  ALTER TABLE, DROP TABLE and DROP INDEX. PRAGMA, ATTACH, transaction
  control, triggers, views and temporary objects are never allowed;
- a virtual table of any module but fts5, or under a name that is not the
  behavior's own. Another module can reach past the behavior's tables
  (dbstat reports on every table in the file), and fts5 is the one
  full-text search needs.

A name without a reserved prefix passes (it may be a column or an alias),
so the objects a migration leaves are checked again against sqlite_master
after it runs (storage.ts): each must be a table or index of its own.
*/

/** The prefixes the engine reserves for its own tables, SQLite's and the behaviors'. */
export const RESERVED_PREFIXES: readonly string[] = ['engine_', 'sqlite_', 'pragma_', 'bhv_'];

/** What a statement may do: read, write, or change the behavior's schema. */
export type SqlMode = 'read' | 'write' | 'migrate';

type TokenKind = 'word' | 'quoted' | 'string' | 'semicolon' | 'open' | 'close' | 'other';

interface Token {
  kind: TokenKind;
  text: string;
}

const READ_VERBS = new Set(['SELECT', 'VALUES']);
const WRITE_VERBS = new Set(['INSERT', 'UPDATE', 'DELETE', 'REPLACE']);
const MAIN_VERBS = new Set([...READ_VERBS, ...WRITE_VERBS]);

/**
 * sqlRefusal returns why a behavior may not run sql, or undefined when it
 * may. prefix is the behavior's own, `bhv_<key>__`.
 */
export function sqlRefusal(sql: string, prefix: string, mode: SqlMode): string | undefined {
  if (typeof sql !== 'string') {
    return 'SQL is a string';
  }
  const tokens = tokenize(sql);
  let end = tokens.length;
  while (end > 0 && tokens[end - 1].kind === 'semicolon') {
    end -= 1;
  }
  const statement = tokens.slice(0, end);
  if (statement.length === 0) {
    return 'the SQL holds no statement';
  }
  if (statement.some((token) => token.kind === 'semicolon')) {
    return 'the SQL holds more than one statement; run one at a time';
  }
  for (const token of statement) {
    if (token.kind !== 'word' && token.kind !== 'quoted' && token.kind !== 'string') {
      continue;
    }
    const name = token.text.toLowerCase();
    if (RESERVED_PREFIXES.some((reserved) => name.startsWith(reserved)) && !name.startsWith(prefix)) {
      return `it names ${token.text}, which is not the behavior's own storage (${prefix}*)`;
    }
    if (token.kind === 'word' && name === 'load_extension') {
      return 'it calls load_extension';
    }
  }
  return verbRefusal(statement, prefix, mode);
}

function verbRefusal(statement: Token[], prefix: string, mode: SqlMode): string | undefined {
  const first = statement[0];
  const verb = first.kind === 'word' ? first.text.toUpperCase() : '';
  if (verb === 'WITH') {
    const main = mainVerb(statement);
    if (main === undefined) {
      return 'its WITH clause is followed by no statement';
    }
    return WRITE_VERBS.has(main) && mode === 'read' ? `a read runs SELECT, VALUES or WITH ... SELECT, not WITH ... ${main}` : undefined;
  }
  if (READ_VERBS.has(verb)) {
    return undefined;
  }
  if (WRITE_VERBS.has(verb)) {
    return mode === 'read' ? `a read runs SELECT, VALUES or WITH ... SELECT, not ${verb}` : undefined;
  }
  if (mode === 'migrate' && (verb === 'CREATE' || verb === 'DROP' || verb === 'ALTER')) {
    return ddlRefusal(verb, statement, prefix);
  }
  const allowed =
    mode === 'read'
      ? 'SELECT, VALUES or WITH ... SELECT'
      : mode === 'write'
        ? 'SELECT, VALUES, WITH, INSERT, UPDATE, DELETE or REPLACE'
        : 'CREATE TABLE, CREATE INDEX, CREATE VIRTUAL TABLE, ALTER TABLE, DROP TABLE, DROP INDEX or a write';
  return `a ${mode === 'migrate' ? 'migration' : mode} runs ${allowed}, not ${verb || first.text}`;
}

// The statement a WITH clause prefixes: the first of SELECT, VALUES,
// INSERT, UPDATE, DELETE or REPLACE outside parentheses. Data-changing
// statements cannot nest in SQLite, so it decides whether the whole
// statement writes.
function mainVerb(statement: Token[]): string | undefined {
  let depth = 0;
  for (const token of statement.slice(1)) {
    if (token.kind === 'open') {
      depth += 1;
    } else if (token.kind === 'close') {
      depth -= 1;
    } else if (depth === 0 && token.kind === 'word' && MAIN_VERBS.has(token.text.toUpperCase())) {
      return token.text.toUpperCase();
    }
  }
  return undefined;
}

function ddlRefusal(verb: string, statement: Token[], prefix: string): string | undefined {
  const words = statement.slice(1, 3).map((token) => (token.kind === 'word' ? token.text.toUpperCase() : ''));
  const [second, third] = words;
  if (verb === 'CREATE' && second === 'VIRTUAL' && third === 'TABLE') {
    return virtualTableRefusal(statement.slice(3), prefix);
  }
  const ok =
    verb === 'CREATE'
      ? second === 'TABLE' ||
        second === 'INDEX' ||
        (second === 'UNIQUE' && third === 'INDEX') ||
        (second === 'VIRTUAL' && third === 'TABLE')
      : verb === 'DROP'
        ? second === 'TABLE' || second === 'INDEX'
        : second === 'TABLE';
  if (ok) {
    return undefined;
  }
  return `a migration may ${verb} only tables and indexes of its own, not ${[verb, second, third].filter(Boolean).join(' ')}`;
}

// virtualTableRefusal holds CREATE VIRTUAL TABLE to `[IF NOT EXISTS] <own
// name> USING fts5`: one of the behavior's own names, unqualified, and the
// fts5 module. rest is what follows CREATE VIRTUAL TABLE.
function virtualTableRefusal(rest: Token[], prefix: string): string | undefined {
  const upper = (token: Token | undefined): string => (token?.kind === 'word' ? token.text.toUpperCase() : '');
  let at = 0;
  if (upper(rest[0]) === 'IF' && upper(rest[1]) === 'NOT' && upper(rest[2]) === 'EXISTS') {
    at = 3;
  }
  const name = rest[at];
  const own = name !== undefined && (name.kind === 'word' || name.kind === 'quoted') && name.text.toLowerCase().startsWith(prefix);
  if (!own || upper(rest[at + 1]) !== 'USING') {
    return `a migration creates a virtual table only under a name of its own (${prefix}*, unqualified), with sql.table(name)`;
  }
  const module = rest[at + 2];
  if (module?.kind !== 'word' || module.text.toLowerCase() !== 'fts5') {
    return `a migration creates a virtual table only with the fts5 module, not ${module?.text ?? 'none'}`;
  }
  return undefined;
}

// tokenize splits SQL into the tokens the checks read. Blob and number
// literals and parameters become `other`.
// A malformed statement (an unterminated literal, a number glued to a word)
// is SQLite's to refuse; the tokenizer only keeps it from hiding a name.
function tokenize(sql: string): Token[] {
  const tokens: Token[] = [];
  const n = sql.length;
  let i = 0;
  while (i < n) {
    const c = sql[i];
    const next = sql[i + 1];
    if (/\s/.test(c)) {
      i += 1;
    } else if (c === '-' && next === '-') {
      const end = sql.indexOf('\n', i);
      i = end < 0 ? n : end + 1;
    } else if (c === '/' && next === '*') {
      const end = sql.indexOf('*/', i + 2);
      i = end < 0 ? n : end + 2;
    } else if (c === "'") {
      const end = quotedEnd(sql, i, "'");
      tokens.push({
        kind: 'string',
        text: sql
          .slice(i + 1, end - 1)
          .split("''")
          .join("'"),
      });
      i = end;
    } else if ((c === 'x' || c === 'X') && next === "'") {
      i = quotedEnd(sql, i + 1, "'");
      tokens.push({ kind: 'other', text: "x'" });
    } else if (c === '"' || c === '`') {
      const end = quotedEnd(sql, i, c);
      tokens.push({
        kind: 'quoted',
        text: sql
          .slice(i + 1, end - 1)
          .split(c + c)
          .join(c),
      });
      i = end;
    } else if (c === '[') {
      const close = sql.indexOf(']', i + 1);
      const end = close < 0 ? n : close;
      tokens.push({ kind: 'quoted', text: sql.slice(i + 1, end) });
      i = end + 1;
    } else if (c === ';') {
      tokens.push({ kind: 'semicolon', text: c });
      i += 1;
    } else if (c === '(' || c === ')') {
      tokens.push({ kind: c === '(' ? 'open' : 'close', text: c });
      i += 1;
    } else if (identifierStart(c)) {
      let j = i + 1;
      while (j < n && identifierPart(sql[j])) {
        j += 1;
      }
      tokens.push({ kind: 'word', text: sql.slice(i, j) });
      i = j;
    } else if (/[0-9]/.test(c) || (c === '.' && /[0-9]/.test(next ?? ''))) {
      let j = i + 1;
      while (j < n && (/[0-9A-Za-z_.]/.test(sql[j]) || ((sql[j] === '+' || sql[j] === '-') && /[eE]/.test(sql[j - 1])))) {
        j += 1;
      }
      tokens.push({ kind: 'other', text: '0' });
      i = j;
    } else if (c === '?' || c === ':' || c === '@' || c === '$') {
      let j = i + 1;
      while (j < n && (identifierPart(sql[j]) || sql[j] === ':')) {
        j += 1;
      }
      tokens.push({ kind: 'other', text: '?' });
      i = j;
    } else {
      tokens.push({ kind: 'other', text: c });
      i += 1;
    }
  }
  return tokens;
}

// quotedEnd returns the index after the literal that opens at start with
// quote; a doubled quote is part of the literal.
function quotedEnd(sql: string, start: number, quote: string): number {
  let i = start + 1;
  while (i < sql.length) {
    if (sql[i] === quote) {
      if (sql[i + 1] === quote) {
        i += 2;
        continue;
      }
      return i + 1;
    }
    i += 1;
  }
  return sql.length;
}

function identifierStart(c: string): boolean {
  return /[A-Za-z_]/.test(c) || c.charCodeAt(0) >= 0x80;
}

function identifierPart(c: string): boolean {
  return /[A-Za-z0-9_$]/.test(c) || c.charCodeAt(0) >= 0x80;
}
