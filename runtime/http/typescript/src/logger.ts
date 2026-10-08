/*
The logger a generated TypeScript server writes through (D51, section 8.6
of docs/stack-model.md): one JSON object per line on stdout, with `level`,
`time` (RFC 3339 in UTC) and `msg` first, then the fields bound with
createLogger and child, then the call's own. It has no dependency and
imports nothing from node:, as the Go server logs JSON lines through zap.
A field's Error is written as its message, a bigint as its decimal, and a
value JSON cannot hold leaves the line without its fields rather than
throwing.
*/

/** The levels, least severe first. */
export const LOG_LEVELS = ['debug', 'info', 'warn', 'error'] as const;

export type LogLevel = (typeof LOG_LEVELS)[number];

/** Fields a line carries beside its level, time and message. */
export type LogFields = Readonly<Record<string, unknown>>;

export interface Logger {
  debug(msg: string, fields?: LogFields): void;
  info(msg: string, fields?: LogFields): void;
  warn(msg: string, fields?: LogFields): void;
  error(msg: string, fields?: LogFields): void;
  /** A logger that writes fields on every line beside this one's. */
  child(fields: LogFields): Logger;
}

export interface LoggerOptions {
  /** The least severe level written; info by default, as zap's production logger. */
  readonly level?: LogLevel;
  /** Writes one line, without its newline; console.log by default, which writes it to stdout. */
  readonly write?: (line: string) => void;
  /** Clock in milliseconds, for tests; Date.now by default. */
  readonly now?: () => number;
}

/** The members level, time and msg take, which a field of the same name does not replace. */
const RESERVED = new Set(['level', 'time', 'msg']);

function replacer(_key: string, value: unknown): unknown {
  if (value instanceof Error) return value.message;
  if (typeof value === 'bigint') return value.toString();
  return value;
}

/**
 * A logger whose every line carries fields. Each line is one JSON object:
 * `{"level":"info","time":"…","msg":"…",…}`.
 */
export function createLogger(fields: LogFields = {}, options: LoggerOptions = {}): Logger {
  const least = LOG_LEVELS.indexOf(options.level ?? 'info');
  const write = options.write ?? ((line: string) => console.log(line));
  const now = options.now ?? Date.now;
  const make = (bound: LogFields): Logger => {
    const log = (level: LogLevel, msg: string, extra?: LogFields): void => {
      if (LOG_LEVELS.indexOf(level) < least) return;
      const head = { level, time: new Date(now()).toISOString(), msg };
      // fromEntries makes each field an own member, __proto__ included; a
      // later field replaces an earlier one of the same name.
      const fieldEntries = [...Object.entries(bound), ...Object.entries(extra ?? {})].filter(([key]) => !RESERVED.has(key));
      const line = Object.fromEntries([...Object.entries(head), ...fieldEntries]);
      let text: string;
      try {
        text = JSON.stringify(line, replacer);
      } catch (err) {
        text = JSON.stringify({ ...head, logError: `the line's fields are not JSON: ${err instanceof Error ? err.message : String(err)}` });
      }
      write(text);
    };
    return {
      debug: (msg, extra) => log('debug', msg, extra),
      info: (msg, extra) => log('info', msg, extra),
      warn: (msg, extra) => log('warn', msg, extra),
      error: (msg, extra) => log('error', msg, extra),
      child: more => make({ ...bound, ...more }),
    };
  };
  return make(fields);
}
