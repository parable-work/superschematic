import type { Revealable } from '../secret.js';

/*
The sandbox provider (D159): where an agent run's instance runs (D158).
The dispatcher starts a fresh instance per run with the run's token, the
MCP URL and the credential references, and cancels it when the run stops;
only what the instance wrote through the API outlives it. A provider for a
container platform, a VM service or a local process implements it.
*/

/** What one run's instance gets. */
export interface SandboxSpec {
  /** The run the instance serves, which its records name. */
  readonly runId: string;
  /** What the instance runs: an image for a container platform, a program for a local process. */
  readonly image?: string;
  readonly command?: readonly string[];
  /** Plain settings: the MCP URL, the definition's version. */
  readonly env?: Readonly<Record<string, string>>;
  /** Secrets the instance reads, the run token among them, which the provider passes without logging. */
  readonly secrets?: Readonly<Record<string, Revealable<string>>>;
  /** Limits the provider enforces. */
  readonly limits?: {
    readonly wallSeconds?: number;
    readonly cpuSeconds?: number;
    readonly memoryMb?: number;
  };
}

/** A started instance. */
export interface SandboxInstance {
  readonly id: string;
  readonly startedAt: Date;
}

export type SandboxState = 'starting' | 'running' | 'succeeded' | 'failed' | 'cancelled';

/** Where an instance stands. */
export interface SandboxStatus {
  readonly id: string;
  readonly state: SandboxState;
  readonly exitCode?: number;
  readonly detail?: string;
}

/** What an instance cost so far, for the Budget behaviour (D104). */
export interface SandboxCost {
  readonly id: string;
  readonly wallSeconds: number;
  readonly cpuSeconds: number;
  /** The amount the platform charges, when it says, as a decimal string. */
  readonly amount?: { readonly value: string; readonly currency: string };
}

/** A sandbox provider. */
export interface SandboxProvider {
  start(spec: SandboxSpec): Promise<SandboxInstance>;
  /** Stops the instance; cancelling one that has stopped does nothing. */
  cancel(id: string): Promise<void>;
  status(id: string): Promise<SandboxStatus>;
  cost(id: string): Promise<SandboxCost>;
}
