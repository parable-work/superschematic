/*
Small helpers the core behaviors' guidance writes its sentences with, so
every behavior lists names and says durations the same way. The text is
plain ASCII and depends only on the config it is given.
*/

/** list joins names as a sentence does: a; a and b; a, b and c. */
export function list(names: readonly string[], conjunction: 'and' | 'or' = 'and'): string {
  if (names.length <= 1) {
    return names.join('');
  }
  return `${names.slice(0, -1).join(', ')} ${conjunction} ${names[names.length - 1]}`;
}

/** plural is a count and a noun: 1 rollup, 2 rollups. */
export function plural(count: number, noun: string, many = `${noun}s`): string {
  return `${count} ${count === 1 ? noun : many}`;
}

/** duration says a span of milliseconds as a reader would: 90 seconds, 2 minutes, 30 days, 1500 ms. */
export function duration(ms: number): string {
  if (ms % 86_400_000 === 0) {
    return plural(ms / 86_400_000, 'day');
  }
  if (ms % 3_600_000 === 0) {
    return plural(ms / 3_600_000, 'hour');
  }
  if (ms % 60_000 === 0) {
    return plural(ms / 60_000, 'minute');
  }
  if (ms % 1000 === 0) {
    return plural(ms / 1000, 'second');
  }
  return `${ms} ms`;
}

/** capital starts a sentence with a capital letter. */
export function capital(text: string): string {
  return text.charAt(0).toUpperCase() + text.slice(1);
}

/** sentences joins the sentences that are there with a space. */
export function sentences(...parts: ReadonlyArray<string | undefined | false>): string {
  return parts.filter((part): part is string => typeof part === 'string' && part !== '').join(' ');
}
