#!/usr/bin/env node
// The engine's TypeScript suite against the Rust engine (D58): every test
// file runs on its own with ENGINE_IMPL=rust, under Node.js, Bun or both,
// and the result is held to test/rust-pending.txt, the files not passing
// yet. A file that fails and is not listed fails the run; so does a listed
// file that passes, so the list only shrinks. CI and a person run the same
// command, after `bun run build` and scripts/engine-napi.sh --js-behaviors
// (the test addon, whose bridge runs the behaviors the suite writes in
// TypeScript):
//
//   node scripts/rust-ratchet.mjs                  # Node.js, then Bun
//   node scripts/rust-ratchet.mjs --runtime bun    # one runtime
//   node scripts/rust-ratchet.mjs test/events.test.ts --verbose
//   node scripts/rust-ratchet.mjs --update         # drop what passes, add what fails
//
// --package names another package whose suite opens its engines through
// this one with ENGINE_IMPL=rust, held to its own test/rust-pending.txt:
// the work queue's (runtime/engine-workqueue/typescript, built after this
// package; its `bun run test:rust`):
//
//   node ../../engine/typescript/scripts/rust-ratchet.mjs --package .
//
// A line of rust-pending.txt is a file under test/, then the runtimes it
// is pending on when not both (`node`, `bun`), then a comment:
//
//   events.test.ts            # watch: events.watch over the notifier
//   stream.test.ts bun        # Bun only: ...
//
// Each file's output goes to a log under the system's temporary directory;
// an unexpected failure prints its tail, and --verbose prints every
// failing file's. Files run in parallel (--jobs, half the CPUs by default),
// each with a time limit (--timeout seconds, 600 by default).

import { spawn } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, writeFileSync } from 'node:fs';
import { availableParallelism, tmpdir } from 'node:os';
import { basename, dirname, join, relative, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

// The engine's package, whose dist/native and addon the suite runs on.
const engineRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const RUNTIMES = ['node', 'bun'];

function usage(message) {
  if (message) {
    console.error(message);
  }
  console.error('usage: node scripts/rust-ratchet.mjs [--package dir] [--runtime node|bun] [--jobs N] [--timeout seconds] [--update] [--verbose] [test/<file>.test.ts ...]');
  process.exit(2);
}

const options = { root: engineRoot, runtimes: [...RUNTIMES], jobs: Math.max(1, Math.floor(availableParallelism() / 2)), timeout: 600, update: false, verbose: false, files: [] };
const argv = process.argv.slice(2);
for (let index = 0; index < argv.length; index += 1) {
  const arg = argv[index];
  const value = () => {
    index += 1;
    if (index >= argv.length) {
      usage(`${arg} takes a value`);
    }
    return argv[index];
  };
  if (arg === '--package') {
    options.root = resolve(value());
  } else if (arg === '--runtime') {
    const runtime = value();
    if (!RUNTIMES.includes(runtime)) {
      usage(`--runtime is node or bun, got ${runtime}`);
    }
    options.runtimes = [runtime];
  } else if (arg === '--jobs') {
    options.jobs = Number.parseInt(value(), 10);
  } else if (arg === '--timeout') {
    options.timeout = Number.parseInt(value(), 10);
  } else if (arg === '--update') {
    options.update = true;
  } else if (arg === '--verbose') {
    options.verbose = true;
  } else if (arg.startsWith('--')) {
    usage(`unknown option ${arg}`);
  } else {
    options.files.push(basename(arg));
  }
}
if (!(options.jobs >= 1) || !(options.timeout >= 1)) {
  usage('--jobs and --timeout are positive integers');
}
if (process.env.ENGINE_IMPL !== undefined && process.env.ENGINE_IMPL !== '' && process.env.ENGINE_IMPL !== 'rust') {
  usage(`the ratchet runs the suite with ENGINE_IMPL=rust; ENGINE_IMPL is ${process.env.ENGINE_IMPL}`);
}

// The package whose suite runs: the engine's, or the one --package names.
const root = options.root;
const testDir = join(root, 'test');
const pendingPath = join(testDir, 'rust-pending.txt');
if (!existsSync(pendingPath)) {
  usage(`no ${pendingPath}: the package lists the files not passing yet there`);
}

// The suite's files, and the list's entries.
const allFiles = readdirSync(testDir)
  .filter((name) => name.endsWith('.test.ts'))
  .sort();
const files = options.files.length > 0 ? options.files : allFiles;
for (const file of files) {
  if (!allFiles.includes(file)) {
    usage(`no test file test/${file}`);
  }
}

function readPending() {
  const entries = new Map();
  const problems = [];
  const lines = readFileSync(pendingPath, 'utf8').split('\n');
  lines.forEach((line, at) => {
    const text = line.replace(/#.*$/, '').trim();
    if (text === '') {
      return;
    }
    const [file, ...runtimes] = text.split(/\s+/);
    if (!allFiles.includes(file)) {
      problems.push(`rust-pending.txt:${at + 1}: no test file test/${file}`);
    }
    for (const runtime of runtimes) {
      if (!RUNTIMES.includes(runtime)) {
        problems.push(`rust-pending.txt:${at + 1}: ${runtime} is not a runtime (node, bun)`);
      }
    }
    if (entries.has(file)) {
      problems.push(`rust-pending.txt:${at + 1}: ${file} is listed twice`);
    }
    entries.set(file, { runtimes: runtimes.length > 0 ? runtimes : [...RUNTIMES], line: at });
  });
  return { entries, problems, lines };
}

const pending = readPending();
if (pending.problems.length > 0) {
  console.error(pending.problems.join('\n'));
  process.exit(1);
}

// The build this runs against: the engine's dist and the addon.
if (!existsSync(join(engineRoot, 'dist', 'native', 'index.js'))) {
  usage(`${join(engineRoot, 'dist', 'native')} is missing: run \`bun run build\` in the engine's package first`);
}
try {
  const native = await import(join(engineRoot, 'dist', 'native', 'index.js'));
  // The suite defines behaviors in TypeScript, which the test addon's
  // bridge alone runs (the js-behaviors cargo feature).
  if (typeof native.loadAddon().bridgeCall !== 'function') {
    console.error('the addon in native/ is the shipped one, without the bridge to behaviors written in TypeScript: build the test addon with scripts/engine-napi.sh --js-behaviors');
    process.exit(1);
  }
} catch (error) {
  console.error(`the Rust engine's addon does not load (scripts/engine-napi.sh --js-behaviors builds it): ${error instanceof Error ? error.message : String(error)}`);
  process.exit(1);
}

const logs = mkdtempSync(join(tmpdir(), 'engine-rust-ratchet-'));

function command(runtime, file) {
  const path = join('test', file);
  return runtime === 'node' ? ['node', ['--test', path]] : ['bun', ['test', `./${path}`]];
}

function runFile(runtime, file) {
  return new Promise((done) => {
    const [program, args] = command(runtime, file);
    const log = join(logs, runtime, `${file}.log`);
    mkdirSync(dirname(log), { recursive: true });
    const chunks = [];
    // Bun shortens its report under an agent (CLAUDECODE, AI_AGENT); the
    // ratchet reads the whole of it.
    const env = { ...process.env, ENGINE_IMPL: 'rust', FORCE_COLOR: '0', NO_COLOR: '1' };
    delete env.CLAUDECODE;
    delete env.AI_AGENT;
    const child = spawn(program, args, { cwd: root, env, stdio: ['ignore', 'pipe', 'pipe'] });
    child.stdout.on('data', (chunk) => chunks.push(chunk));
    child.stderr.on('data', (chunk) => chunks.push(chunk));
    let timedOut = false;
    const timer = setTimeout(() => {
      timedOut = true;
      child.kill('SIGKILL');
    }, options.timeout * 1000);
    child.on('close', (code) => {
      clearTimeout(timer);
      const output = Buffer.concat(chunks).toString('utf8') + (timedOut ? `\n[rust-ratchet] killed after ${options.timeout}s\n` : '');
      writeFileSync(log, output);
      done({ runtime, file, passed: code === 0 && !timedOut, timedOut, output, log });
    });
  });
}

async function runAll(runtime) {
  const queue = [...files];
  const results = [];
  const worker = async () => {
    while (queue.length > 0) {
      const file = queue.shift();
      results.push(await runFile(runtime, file));
    }
  };
  await Promise.all(Array.from({ length: Math.min(options.jobs, files.length) }, worker));
  return results.sort((a, b) => a.file.localeCompare(b.file));
}

// gaps counts the facade's NativeUnsupportedError gaps in a file's output,
// a hint at why it fails (src/native/errors.ts).
function gaps(output) {
  const counts = {};
  for (const match of output.matchAll(/is not on the Rust engine yet \(([a-z]+)\)/g)) {
    counts[match[1]] = (counts[match[1]] ?? 0) + 1;
  }
  return Object.entries(counts)
    .map(([gap, count]) => `${gap} ${count}`)
    .join(', ');
}

// counts reads how many of a file's tests passed and failed from its
// runner's report: node's `ℹ pass N`, Bun's ` N pass`.
function counts(runtime, output) {
  const read = (pattern) => Number(output.match(pattern)?.[1] ?? 0);
  return runtime === 'node' ? { pass: read(/^ℹ pass (\d+)$/m), fail: read(/^ℹ fail (\d+)$/m) } : { pass: read(/^ *(\d+) pass$/m), fail: read(/^ *(\d+) fail$/m) };
}

function tail(output, lines = 60) {
  return output.split('\n').slice(-lines).join('\n');
}

const failures = [];
const stillPending = new Map();
const nowPassing = new Map();
for (const runtime of options.runtimes) {
  const started = Date.now();
  const results = await runAll(runtime);
  let passed = 0;
  const tests = { pass: 0, fail: 0 };
  for (const result of results) {
    const { pass, fail } = counts(runtime, result.output);
    tests.pass += pass;
    tests.fail += fail;
    const entry = pending.entries.get(result.file);
    const listed = entry !== undefined && entry.runtimes.includes(runtime);
    if (result.passed) {
      passed += 1;
    }
    if (result.passed && listed) {
      failures.push(`${runtime}: ${result.file} passes: take it off test/rust-pending.txt`);
      nowPassing.set(result.file, [...(nowPassing.get(result.file) ?? []), runtime]);
    } else if (!result.passed) {
      stillPending.set(result.file, [...(stillPending.get(result.file) ?? []), runtime]);
      const hint = gaps(result.output);
      const why = `${result.timedOut ? 'timed out' : 'fails'}${hint ? ` (${hint})` : ''}`;
      if (!listed) {
        failures.push(`${runtime}: ${result.file} ${why} and is not on test/rust-pending.txt; log ${result.log}`);
        console.log(`\n----- ${runtime} ${result.file} (${result.log})\n${tail(result.output)}`);
      } else if (options.verbose) {
        console.log(`\n----- ${runtime} ${result.file}: pending, ${why} (${result.log})\n${tail(result.output)}`);
      }
    }
  }
  console.log(
    `${runtime}: ${passed} of ${results.length} test files pass on the Rust engine, and ${tests.pass} of ${tests.pass + tests.fail} tests (${Math.round((Date.now() - started) / 1000)}s; logs ${join(logs, runtime)})`
  );
}

if (options.update) {
  // Drop the lines of files that pass on every runtime this run covered,
  // narrow a line to the runtimes it still fails on, and add the new ones.
  const lines = [...pending.lines];
  for (const [file, entry] of pending.entries) {
    const failing = stillPending.get(file) ?? [];
    const covered = options.runtimes.filter((runtime) => entry.runtimes.includes(runtime));
    if (covered.length === 0 || (options.files.length > 0 && !options.files.includes(file))) {
      continue;
    }
    const keep = entry.runtimes.filter((runtime) => !options.runtimes.includes(runtime) || failing.includes(runtime));
    const comment = lines[entry.line].includes('#') ? lines[entry.line].slice(lines[entry.line].indexOf('#')) : '';
    lines[entry.line] = keep.length === 0 ? null : `${[file, ...(keep.length === RUNTIMES.length ? [] : keep)].join(' ').padEnd(40)} ${comment}`.trimEnd();
  }
  const added = [...stillPending.entries()].filter(([file]) => !pending.entries.has(file));
  const out = lines.filter((line) => line !== null);
  while (out.length > 0 && out[out.length - 1] === '') {
    out.pop();
  }
  if (added.length > 0) {
    out.push('', '# Added by scripts/rust-ratchet.mjs --update: say why each fails.');
    for (const [file, runtimes] of added) {
      out.push([file, ...(runtimes.length === RUNTIMES.length || options.runtimes.length < RUNTIMES.length ? [] : runtimes)].join(' '));
    }
  }
  writeFileSync(pendingPath, `${out.join('\n')}\n`);
  console.log(`updated ${relative(process.cwd(), pendingPath)}`);
  process.exit(0);
}

if (failures.length > 0) {
  console.error(`\nthe Rust engine's ratchet fails:\n${failures.map((failure) => `  ${failure}`).join('\n')}`);
  process.exit(1);
}
console.log('the ratchet holds: every file that fails is on test/rust-pending.txt, and none on it passes');
