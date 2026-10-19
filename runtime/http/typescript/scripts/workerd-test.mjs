// The workerd harness (D64, section 18.13 of docs/stack-model.md): runs the
// fixtures under test/workerd/fixtures with the workerd binary, which
// test/workerd's lockfile pins, apart from the runtime's own dependencies.
// For each fixture it:
//   1. bundles each of its .ts files as a Worker's bundle.ts bundles one,
//      `bun build --target=browser --conditions=workerd`, with superscalar's
//      WebAssembly module and cloudflare:workers kept external, into
//      test/workerd/out/<fixture>/, beside superscalar's module and the
//      fixture's .js files;
//   2. writes its config.capnp there, with a fresh Ed25519 key pair for the
//      edge in place of $CALLER_KEY_PEM (its PKCS#8 PEM) and
//      $CALLER_PUBLIC_PEM (its SubjectPublicKeyInfo PEM), as the generic
//      connector's key node gives them (D63);
//   3. runs `workerd test` on it, which runs each Worker's test handler.
// A fixture with an expected-failure.txt passes when workerd fails and
// prints that text. The fixtures import the runtime's dist/, so run it
// after `bun run build`.
import { spawnSync } from 'node:child_process';
import { generateKeyPairSync } from 'node:crypto';
import { copyFileSync, existsSync, mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const harness = join(root, 'test', 'workerd');
const fixtures = join(harness, 'fixtures');
const workerd = join(harness, 'node_modules', '.bin', 'workerd');
const SUPERSCALAR_WASM = 'superscalar_wasm_bg.wasm';

function run(command, args, options = {}) {
  const done = spawnSync(command, args, { encoding: 'utf8', ...options });
  if (done.error) throw done.error;
  return done;
}

function must(command, args, options) {
  const done = run(command, args, options);
  if (done.status !== 0) {
    process.stderr.write(done.stdout + done.stderr);
    throw new Error(`${command} ${args.join(' ')} exited ${done.status}`);
  }
  return done;
}

for (const name of ['index', 'workers', 'd1', 'r2']) {
  if (!existsSync(join(root, 'dist', `${name}.js`))) throw new Error(`workerd-test: dist/${name}.js is missing; run bun run build first`);
}
must('bun', ['install', '--frozen-lockfile'], { cwd: harness });

/** A string as a Cap'n Proto text literal's contents. */
function capnpText(value) {
  return value.replace(/\\/gu, '\\\\').replace(/"/gu, '\\"').replace(/\n/gu, '\\n');
}

const { privateKey, publicKey } = generateKeyPairSync('ed25519');
const placeholders = {
  $CALLER_KEY_PEM: capnpText(privateKey.export({ type: 'pkcs8', format: 'pem' })),
  $CALLER_PUBLIC_PEM: capnpText(publicKey.export({ type: 'spki', format: 'pem' })),
};

const superscalarWasm = join(root, 'node_modules', 'superscalar', 'dist', 'workerd', SUPERSCALAR_WASM);
let failed = false;
for (const fixture of readdirSync(fixtures).sort()) {
  const source = join(fixtures, fixture);
  const out = join(harness, 'out', fixture);
  rmSync(out, { recursive: true, force: true });
  mkdirSync(out, { recursive: true });
  for (const file of readdirSync(source)) {
    if (file.endsWith('.ts')) {
      must(
        'bun',
        ['build', '--target=browser', '--conditions=workerd', '--conditions=worker', '--external=*.wasm', '--external=cloudflare:*', `--outdir=${out}`, join(source, file)],
        { cwd: root }
      );
    } else if (file.endsWith('.js')) {
      copyFileSync(join(source, file), join(out, file));
    }
  }
  if (!existsSync(superscalarWasm)) throw new Error(`workerd-test: ${superscalarWasm} is missing; scripts/superscalar-dep.sh builds superscalar's workerd build`);
  copyFileSync(superscalarWasm, join(out, SUPERSCALAR_WASM));
  let config = readFileSync(join(source, 'config.capnp'), 'utf8');
  for (const [placeholder, value] of Object.entries(placeholders)) config = config.split(placeholder).join(value);
  writeFileSync(join(out, 'config.capnp'), config);

  const done = run(workerd, ['test', join(out, 'config.capnp')], { cwd: out });
  const output = done.stdout + done.stderr;
  const expectedFile = join(source, 'expected-failure.txt');
  if (existsSync(expectedFile)) {
    const expected = readFileSync(expectedFile, 'utf8').trim();
    if (done.status === 0 || !output.includes(expected)) {
      failed = true;
      console.error(`workerd-test: ${fixture}: want workerd to fail with ${JSON.stringify(expected)}; it exited ${done.status}:\n${output}`);
    } else {
      console.log(`workerd-test: ${fixture}: workerd refused it, as expected`);
    }
    continue;
  }
  if (done.status !== 0) {
    failed = true;
    console.error(`workerd-test: ${fixture}: workerd test exited ${done.status}:\n${output}`);
    continue;
  }
  console.log(`workerd-test: ${fixture}: ok`);
  if (process.env.WORKERD_TEST_VERBOSE) console.log(output);
}
if (failed) process.exit(1);
