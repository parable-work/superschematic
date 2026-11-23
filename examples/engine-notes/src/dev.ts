// The notes schema on development mode (D117): the same schema file served
// by superschematic-dev, the development server of the behaviors' core,
// in place of the engine. A row is kept as JSON in the server's own tables,
// a publish takes effect at once, and the behaviors run on the core
// (runtime/engine/rust/rules-behaviors).
//
// What the engine's server has that this one differs in:
//
// - The type is Note, as the schema file's type names it; the engine
//   names the schema notes. Routes are under /namespaces/default/schemas/Note.
// - A row is its own fields beside its behaviors' fields, each named after
//   its behavior (workflowStatus, commentsMessages), and a read's fields
//   are its version's (_version); the engine answers data and behaviors
//   apart, with seq and an ETag on an operation too.
// - The caller is whoever the x-superschematic-subject and
//   x-superschematic-permissions headers say, and the policy is data
//   (devPolicy); the engine authenticates a bearer token and asks a
//   function.
// - The event log holds what the behaviors emit about their rows, which
//   Workflow, Comments and Revisions do not; the engine logs every write.
//   The stream sends signals beside them with signals=1 as the engine's
//   does, a streamed reply's draft among them.
// - A publish has no compatibility rule: a version may add a required
//   field, and its publish is refused while a stored note lacks it (409
//   conflict, StoredRowsError), unless it discards those notes. The engine
//   refuses such a draft at define (incompatible_change).
import { spawn, type ChildProcess } from 'node:child_process';
import { existsSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

/** The repository's root, two directories above this example. */
const repoRoot = join(dirname(fileURLToPath(import.meta.url)), '..', '..', '..');

/**
 * The superschematic-dev binary: SUPERSCHEMATIC_DEV when set, else the debug
 * build in the engine's Cargo workspace (scripts/check.sh builds it).
 */
export function devBinary(): string {
  const given = process.env.SUPERSCHEMATIC_DEV;
  if (given !== undefined && given !== '') {
    return given;
  }
  const target = process.env.CARGO_TARGET_DIR ?? join(repoRoot, 'runtime', 'engine', 'rust', 'target');
  return join(target, 'debug', 'superschematic-dev');
}

/**
 * The access policy of src/auth.ts as development mode's data: defining and
 * publishing to a holder of schemas.<action>; posting, reacting, a streamed
 * reply and a proposal to a reader; and the rest to a holder of
 * notes.<action>. Signals and read state are asked as reads.
 */
export const devPolicy = {
  rules: [
    { action: ['define', 'publish'], require: ['schemas.{action}'] },
    {
      action: 'write',
      operation: ['post', 'react', 'unreact', 'propose', 'begin', 'appendDraft', 'finish', 'interrupt'],
      require: ['notes.read'],
    },
    { action: ['read', 'write'], require: ['notes.{action}'] },
  ],
};

/** The callers of src/auth.ts, as the headers that name them to development mode. */
export const devCallers: Readonly<Record<string, Readonly<Record<string, string>>>> = {
  alice: { 'x-superschematic-subject': 'alice', 'x-superschematic-permissions': 'notes.read,notes.write' },
  bob: { 'x-superschematic-subject': 'bob', 'x-superschematic-permissions': 'notes' },
  carol: { 'x-superschematic-subject': 'carol', 'x-superschematic-permissions': 'notes.read' },
  dana: { 'x-superschematic-subject': 'dana', 'x-superschematic-permissions': 'schemas' },
};

export interface DevServer {
  /** The server's origin, such as http://127.0.0.1:4500. */
  url: string;
  close(): Promise<void>;
}

/**
 * startDev runs superschematic-dev over a SQLite file, with devPolicy and no
 * default caller, on a free port, and resolves once it serves.
 */
export function startDev(db: string, policyFile: string): Promise<DevServer> {
  const binary = devBinary();
  if (!existsSync(binary)) {
    return Promise.reject(
      new Error(`${binary} is missing: build it with cargo build -p superschematic-rules-dev --features server --bin superschematic-dev, or set SUPERSCHEMATIC_DEV`)
    );
  }
  writeFileSync(policyFile, JSON.stringify(devPolicy));
  const child: ChildProcess = spawn(binary, ['--db', db, '--listen', '127.0.0.1:0', '--policy', `@${policyFile}`, '--as', ''], {
    stdio: ['ignore', 'ignore', 'pipe'],
  });
  return new Promise((resolve, reject) => {
    let output = '';
    const failed = (error: Error) => reject(error);
    child.once('error', failed);
    child.once('exit', (code) => reject(new Error(`superschematic-dev exited with ${code}: ${output}`)));
    child.stderr?.setEncoding('utf8');
    child.stderr?.on('data', (chunk: string) => {
      output += chunk;
      const served = /serving (http:\/\/[^/\s]+)/.exec(output);
      if (served === null) {
        return;
      }
      child.removeAllListeners('exit');
      child.removeListener('error', failed);
      resolve({
        url: served[1],
        close: () =>
          new Promise<void>((done) => {
            if (child.exitCode !== null) {
              done();
              return;
            }
            child.once('exit', () => done());
            child.kill('SIGTERM');
          }),
      });
    });
  });
}
