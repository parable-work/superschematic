// Picks the part of an example file a docs page quotes. A page names a
// top-level declaration (`symbol`) or a pair of text anchors (`from`, `to`);
// a name that no longer matches throws, which fails the site build, so a
// quoted snippet cannot drift from the file it comes from.
//
// Region comments are not an option in schema files: the TypeScript loader
// attaches every comment above a declaration to it as its description.

export interface Selection {
  /** Path under examples/acme-shop, for error messages. */
  file: string;
  /** One or more top-level declarations: `Price`, `NewHandler`, or a Go method as `Products.GetProduct`. */
  symbol?: string | string[];
  /** The first line containing this text starts the snippet. */
  from?: string;
  /** The first line at or after `from` containing this text ends it. */
  to?: string;
}

const escape = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

function headerPattern(name: string): RegExp {
  const dot = name.indexOf('.');
  if (dot > 0) {
    const receiver = escape(name.slice(0, dot));
    const method = escape(name.slice(dot + 1));
    return new RegExp(`^func \\(\\s*\\w+\\s+\\*?${receiver}\\s*\\)\\s+${method}\\b`);
  }
  return new RegExp(
    `^(export\\s+)?(default\\s+)?(declare\\s+)?(abstract\\s+)?(async\\s+)?` +
      `(class|interface|enum|type|function|const|let|var|func)\\s+${escape(name)}\\b`
  );
}

// A declaration whose first line ends by opening a block, a parameter list or
// a literal runs to the first later line that closes it at column 0.
const opensBlock = /[{([]\s*$/;
const closesAtColumnZero = /^[})\]]/;

function declaration(lines: string[], name: string, file: string): string[] {
  const pattern = headerPattern(name);
  const header = lines.findIndex(line => pattern.test(line));
  if (header < 0) {
    throw new Error(`Snippet: no top-level declaration named ${name} in examples/acme-shop/${file}`);
  }
  let start = header;
  while (start > 0 && /^\s*(\/\/|\/\*|\*|@)/.test(lines[start - 1])) {
    start--;
  }
  let end = header;
  if (opensBlock.test(lines[header])) {
    end = lines.findIndex((line, i) => i > header && closesAtColumnZero.test(line));
    if (end < 0) {
      throw new Error(`Snippet: ${name} in examples/acme-shop/${file} never closes at column 0`);
    }
  }
  return lines.slice(start, end + 1);
}

function between(lines: string[], from: string, to: string | undefined, file: string): string[] {
  const start = lines.findIndex(line => line.includes(from));
  if (start < 0) {
    throw new Error(`Snippet: no line contains ${JSON.stringify(from)} in examples/acme-shop/${file}`);
  }
  if (to === undefined) {
    return lines.slice(start, start + 1);
  }
  const offset = lines.slice(start).findIndex(line => line.includes(to));
  if (offset < 0) {
    throw new Error(`Snippet: no line after ${JSON.stringify(from)} contains ${JSON.stringify(to)} in examples/acme-shop/${file}`);
  }
  return lines.slice(start, start + offset + 1);
}

// Removes the indentation every non-blank line shares, so an excerpt from
// inside a block starts at column 0.
function dedent(lines: string[]): string[] {
  const indents = lines.filter(line => line.trim() !== '').map(line => line.match(/^[ \t]*/)![0].length);
  const common = indents.length > 0 ? Math.min(...indents) : 0;
  return lines.map(line => line.slice(common));
}

export function extract(source: string, selection: Selection): string {
  const lines = source.replace(/\r\n/g, '\n').split('\n');
  if (selection.symbol !== undefined) {
    const names = Array.isArray(selection.symbol) ? selection.symbol : [selection.symbol];
    return names.map(name => declaration(lines, name, selection.file).join('\n')).join('\n\n');
  }
  if (selection.from !== undefined) {
    return dedent(between(lines, selection.from, selection.to, selection.file)).join('\n');
  }
  return source.replace(/\s+$/, '');
}
