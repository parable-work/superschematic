// A sandbox provider that runs each run's instance as a local process
// (D159): a stub for tests and stack dev. It isolates nothing; a provider
// for a container platform or a VM service implements the same four
// operations. The command runs with the spec's env and secrets as its
// environment, and no secret is logged. cancel kills the process; cost is
// its wall time and the CPU time the platform reports once it exits.
import { spawn, type ChildProcess } from 'node:child_process';
import {
  defineProvider,
  type SandboxProvider,
  type SandboxState,
} from '@superschematic/http-runtime/providers';

interface Instance {
  readonly child: ChildProcess;
  readonly startedAt: number;
  state: SandboxState;
  endedAt?: number;
  exitCode?: number;
  cpuSeconds: number;
  readonly done: Promise<void>;
}

export const localSandbox = defineProvider({
  name: 'local-sandbox',
  implements: 'sandbox',
  config: {
    WORKDIR: { optional: true, description: "The directory each process starts in; the server's own when unset." },
  },
  create(settings, context): SandboxProvider {
    const cwd = settings.optionalText('WORKDIR');
    const instances = new Map<string, Instance>();
    let next = 0;
    const instanceOf = (id: string): Instance => {
      const instance = instances.get(id);
      if (!instance) throw new Error(`local-sandbox: no instance ${id}`);
      return instance;
    };
    return {
      async start(spec) {
        const [program, ...args] = spec.command ?? [];
        if (!program) throw new Error('local-sandbox: a spec names the command to run');
        const env: Record<string, string> = { PATH: process.env.PATH ?? '', ...(spec.env ?? {}) };
        for (const [name, secret] of Object.entries(spec.secrets ?? {})) env[name] = secret.reveal();
        const child = spawn(program, args, { cwd, env, stdio: 'ignore' });
        const id = `local-${++next}-${spec.runId}`;
        const startedAt = Date.now();
        const done = new Promise<void>(resolve => {
          child.on('exit', (code, signal) => {
            const instance = instances.get(id)!;
            instance.endedAt = Date.now();
            if (instance.state !== 'cancelled') instance.state = code === 0 ? 'succeeded' : 'failed';
            if (code !== null) instance.exitCode = code;
            instance.cpuSeconds = (instance.endedAt - instance.startedAt) / 1000;
            context.log.info('sandbox instance ended', { id, state: instance.state, ...(signal ? { signal } : {}) });
            resolve();
          });
          child.on('error', () => {
            const instance = instances.get(id)!;
            instance.state = 'failed';
            instance.endedAt = Date.now();
            resolve();
          });
        });
        instances.set(id, { child, startedAt, state: 'running', cpuSeconds: 0, done });
        context.log.info('sandbox instance started', { id, runId: spec.runId, secrets: Object.keys(spec.secrets ?? {}) });
        return { id, startedAt: new Date(startedAt) };
      },
      async cancel(id) {
        const instance = instanceOf(id);
        if (instance.endedAt !== undefined) return;
        instance.state = 'cancelled';
        instance.child.kill('SIGTERM');
        await instance.done;
      },
      async status(id) {
        const instance = instanceOf(id);
        return { id, state: instance.state, ...(instance.exitCode !== undefined ? { exitCode: instance.exitCode } : {}) };
      },
      async cost(id) {
        const instance = instanceOf(id);
        const wallSeconds = ((instance.endedAt ?? Date.now()) - instance.startedAt) / 1000;
        return { id, wallSeconds, cpuSeconds: instance.cpuSeconds };
      },
    };
  },
});

/** Waits for the instance id of a provider this module made to end, for tests. */
export async function settled(provider: SandboxProvider, id: string, timeoutMs = 5000): Promise<void> {
  const until = Date.now() + timeoutMs;
  for (;;) {
    const { state } = await provider.status(id);
    if (state !== 'running' && state !== 'starting') return;
    if (Date.now() > until) throw new Error(`local-sandbox: ${id} still ${state}`);
    await new Promise(resolve => setTimeout(resolve, 20));
  }
}

export default localSandbox;
