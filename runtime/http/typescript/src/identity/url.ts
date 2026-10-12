import { isIPv6 } from 'node:net';

/*
The part of Go's net/url.Parse the identity runtime's origin rules rest on.
Go's config validation reads a trusted origin with url.Parse, and
net/http's CrossOriginProtection compares an Origin header's parsed Host
with the request's Host, so a URL that Go refuses, or whose host Go reads
otherwise than WHATWG's URL does (Go keeps the host's case and an empty
port, and refuses a space), is treated here as Go treats it. The input is
read as Go reads a string, one byte per code unit for ASCII; a non-ASCII
code unit is a byte Go allows in a host.
*/

/** What parseGoURL reads of a URL. */
export interface GoURL {
  scheme: string;
  /** The rest of a URL with a scheme and no `//` or `/`, as `mailto:a@b`. */
  opaque: string;
  /** The URL carries a user (`user@host`). */
  hasUser: boolean;
  /** The host and port, unescaped, as Go's URL.Host. */
  host: string;
  path: string;
  rawQuery: string;
  fragment: string;
}

// The ASCII bytes a host (and a zone) may hold unescaped: letters, digits
// and the punctuation net/url's encoding table marks for encodeHost.
const HOST_PUNCTUATION = new Set('!"$&\'()*+,-.:;<=>[]_~');

function isHostByte(c: number): boolean {
  return (c >= 0x30 && c <= 0x39) || (c >= 0x41 && c <= 0x5a) || (c >= 0x61 && c <= 0x7a) || HOST_PUNCTUATION.has(String.fromCharCode(c));
}

function isHex(c: string | undefined): boolean {
  return c !== undefined && /^[0-9A-Fa-f]$/u.test(c);
}

type Mode = 'host' | 'zone' | 'path' | 'fragment' | 'user';

/** Go's unescape: undefined where it returns an error. */
function unescape(s: string, mode: Mode): string | undefined {
  let out = '';
  for (let i = 0; i < s.length; ) {
    const c = s.charCodeAt(i);
    if (c === 0x25) {
      if (i + 2 >= s.length || !isHex(s[i + 1]) || !isHex(s[i + 2])) return undefined;
      const escaped = s.slice(i, i + 3);
      const value = Number.parseInt(s.slice(i + 1, i + 3), 16);
      if (mode === 'host' && value < 0x80 && escaped !== '%25') return undefined;
      if (mode === 'zone' && escaped !== '%25' && value !== 0x20 && !isHostByte(value)) return undefined;
      out += String.fromCharCode(value);
      i += 3;
      continue;
    }
    if ((mode === 'host' || mode === 'zone') && c < 0x80 && !isHostByte(c)) return undefined;
    out += s[i];
    i++;
  }
  return out;
}

function hasControlByte(s: string): boolean {
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c < 0x20 || c === 0x7f) return true;
  }
  return false;
}

/** Go's getScheme: the scheme and the rest, or undefined for a URL that starts with a colon. */
function getScheme(raw: string): { scheme: string; rest: string } | undefined {
  for (let i = 0; i < raw.length; i++) {
    const c = raw[i]!;
    if (/[A-Za-z]/u.test(c)) continue;
    if (/[0-9+\-.]/u.test(c)) {
      if (i === 0) return { scheme: '', rest: raw };
      continue;
    }
    if (c === ':') {
      if (i === 0) return undefined;
      return { scheme: raw.slice(0, i), rest: raw.slice(i + 1) };
    }
    return { scheme: '', rest: raw };
  }
  return { scheme: '', rest: raw };
}

function validOptionalPort(port: string): boolean {
  if (port === '') return true;
  if (port[0] !== ':') return false;
  return /^[0-9]*$/u.test(port.slice(1));
}

function validUserinfo(s: string): boolean {
  return /^[A-Za-z0-9\-._:~!$&'()*+,;=%@]*$/u.test(s);
}

/** Go's parseHost: the unescaped host, or undefined. */
function parseHost(scheme: string, host: string): string | undefined {
  const open = host.lastIndexOf('[');
  if (open > 0) return undefined;
  if (open === 0) {
    const close = host.lastIndexOf(']');
    if (close < 0) return undefined;
    const colonPort = host.slice(close + 1);
    if (!validOptionalPort(colonPort)) return undefined;
    const port = unescape(colonPort, 'host');
    if (port === undefined) return undefined;
    const hostname = host.slice(1, close);
    const zone = hostname.indexOf('%25');
    let unescaped: string | undefined;
    if (zone >= 0) {
      const address = unescape(hostname.slice(0, zone), 'host');
      const zonePart = unescape(hostname.slice(zone), 'zone');
      unescaped = address === undefined || zonePart === undefined ? undefined : address + zonePart;
    } else {
      unescaped = unescape(hostname, 'host');
    }
    // Only an IPv6 address may be in brackets.
    if (unescaped === undefined || !isIPv6(unescaped)) return undefined;
    return `[${unescaped}]${port}`;
  }
  const first = host.indexOf(':');
  if (first !== -1) {
    let at = first;
    const last = host.lastIndexOf(':');
    // Go holds an http or https host to one colon and takes the last colon
    // of any other scheme's as the port's.
    if (last !== first && scheme !== 'http' && scheme !== 'https') at = last;
    if (!validOptionalPort(host.slice(at))) return undefined;
  }
  return unescape(host, 'host');
}

/** Go's url.Parse, as far as the origin rules read it: undefined where Go returns an error. */
export function parseGoURL(raw: string): GoURL | undefined {
  const hash = raw.indexOf('#');
  const head = hash < 0 ? raw : raw.slice(0, hash);
  const url: GoURL = { scheme: '', opaque: '', hasUser: false, host: '', path: '', rawQuery: '', fragment: '' };
  if (hasControlByte(head)) return undefined;
  if (head === '*') {
    url.path = '*';
  } else {
    const split = getScheme(head);
    if (!split) return undefined;
    url.scheme = split.scheme.toLowerCase();
    let rest = split.rest;
    if (rest.endsWith('?') && rest.indexOf('?') === rest.length - 1) {
      rest = rest.slice(0, -1);
    } else {
      const query = rest.indexOf('?');
      if (query >= 0) {
        url.rawQuery = rest.slice(query + 1);
        rest = rest.slice(0, query);
      }
    }
    let opaque = false;
    if (!rest.startsWith('/')) {
      if (url.scheme !== '') {
        url.opaque = rest;
        opaque = true;
      } else {
        const slash = rest.indexOf('/');
        if ((slash < 0 ? rest : rest.slice(0, slash)).includes(':')) return undefined;
      }
    }
    if (!opaque) {
      if ((url.scheme !== '' || !rest.startsWith('///')) && rest.startsWith('//')) {
        let authority = rest.slice(2);
        rest = '';
        const slash = authority.indexOf('/');
        if (slash >= 0) {
          rest = authority.slice(slash);
          authority = authority.slice(0, slash);
        }
        const at = authority.lastIndexOf('@');
        const host = parseHost(url.scheme, at < 0 ? authority : authority.slice(at + 1));
        if (host === undefined) return undefined;
        url.host = host;
        if (at >= 0) {
          const userinfo = authority.slice(0, at);
          if (!validUserinfo(userinfo)) return undefined;
          const colon = userinfo.indexOf(':');
          const parts = colon < 0 ? [userinfo] : [userinfo.slice(0, colon), userinfo.slice(colon + 1)];
          if (parts.some(part => unescape(part, 'user') === undefined)) return undefined;
          url.hasUser = true;
        }
      }
      const path = unescape(rest, 'path');
      if (path === undefined) return undefined;
      url.path = path;
    }
  }
  if (hash >= 0 && hash + 1 < raw.length) {
    const fragment = unescape(raw.slice(hash + 1), 'fragment');
    if (fragment === undefined) return undefined;
    url.fragment = fragment;
  }
  return url;
}
