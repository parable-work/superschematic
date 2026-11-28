// The SQL text the stores write alike for Postgres and SQLite: a quoted
// identifier, and placeholders rewritten from one dialect's form to the
// other's. A rewrite leaves quoted names and string literals alone, so a
// configured table or column name, or a literal, may hold ? or $1. A
// doubled quote inside one closes it and opens it again, so it needs no
// case of its own.

/** Quotes an identifier as Postgres and SQLite both read it: the name in double quotes, each double quote doubled. */
export function quoteIdentifier(name: string): string {
  return `"${name.replaceAll('"', '""')}"`;
}

/** Rewrites each run of sql outside quoted names and literals with outside, in order, and keeps the quoted runs as written. */
function rewriteOutsideQuotes(sql: string, outside: (text: string) => string): string {
  let out = '';
  let start = 0;
  let quoteChar = '';
  for (let i = 0; i < sql.length; i++) {
    const c = sql[i]!;
    if (quoteChar !== '') {
      if (c === quoteChar) {
        quoteChar = '';
        out += sql.slice(start, i + 1);
        start = i + 1;
      }
    } else if (c === '"' || c === "'") {
      out += outside(sql.slice(start, i));
      quoteChar = c;
      start = i;
    }
  }
  const rest = sql.slice(start);
  return out + (quoteChar === '' ? outside(rest) : rest);
}

/** Writes each ? placeholder as Postgres numbers them, $1, $2 and on. */
export function postgresPlaceholders(sql: string): string {
  let n = 0;
  return rewriteOutsideQuotes(sql, text => text.replace(/\?/gu, () => `$${++n}`));
}

/** Writes each $n placeholder as SQLite and D1 read a numbered one, ?n. */
export function sqlitePlaceholders(sql: string): string {
  return rewriteOutsideQuotes(sql, text => text.replace(/\$(\d+)/gu, '?$1'));
}
