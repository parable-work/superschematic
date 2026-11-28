/*
What jobs-api's implementation adds to its generated TypeScript server
(D157). The generated server wires the rest itself: it reads the channel
adapters the environment's config names from the slots SMS and MAIL over
the provider packages the API's config lists (Deps.providers, D159), builds
the notifier (Deps.notifier, D153) and the inbound path (Deps.inbound) into
Deps, mounts the route each relay posts what it received to (POST
/webhooks/sms/inbound and /webhooks/mail/inbound), which hands every
message down the one inbound path, and hands each message of the
notifier's dispatch queue to notifier.handle, so the Notify worker has no
method here.

Left to the app: what the inbound path is built with beyond that
(createRuntimeOptions: the inbound address and the approval policy of a
send), the staff's sends, timeline and triage, and Runs, the worker that
acts on a run request when no agent does. The roles and each channel
kind's trust are seed rows of jobs-db's schema (@seedRows, #625), which
its migrations write.

The imports name the packages `superschematic build-all` writes for the
example's two services.
*/
import type { Principal } from '@superschematic/http-runtime';
import type { RunRequest } from '@superschematic/http-runtime/postgres';
import type { Deps, RuntimeOptionsConstructor } from '@schemas/jobs-api-api';

/**
 * The inbound path's options beside what the generated server fills: the
 * deployment's inbound address. An API that lists a bucket names it here
 * too (bucket: deps.<bucket>), which a message's files go to; without one
 * they are dropped, and the message says so. No agent sends here, so every
 * send the permission allows goes; examples/agent-orchestration's
 * generated server asks the agent's policy (D158).
 */
export const createRuntimeOptions: RuntimeOptionsConstructor = deps => ({
  inbound: {
    ...(deps.config.INBOUND_ADDRESS ? { inboundAddress: deps.config.INBOUND_ADDRESS } : {}),
    approve: () => 'allow',
  },
});

/**
 * The staff's side of the conversations, each as its caller: writing to a
 * party, held to parties.message, the party's identity, the rate limit and
 * the approval policy; a party's timeline; and triage, what came from no
 * one the deployment trusts, held to parties.triage. The handlers of the
 * API's own operations call these.
 */
export function staff(deps: Deps) {
  return {
    send: (as: Principal, party: { table: string; key: string }, input: { channel: string; body: string; subject?: string }) => deps.inbound.send({ as, party, ...input }),
    timeline: (as: Principal, party: { table: string; key: string }) => deps.inbound.timeline(as, party),
    triage: (as: Principal) => deps.inbound.triage(as),
    settle: (as: Principal, message: string) => deps.inbound.settle(as, message),
  };
}

/**
 * The handler of jobs-api's worker Runs, which takes each run request with
 * no agent: as the party, through the grant the request names, a report on
 * the granted job with the message's text, the message and its photos
 * filed onto both. examples/agent-orchestration's generated server hands
 * the same queue to the agent dispatcher (D158) instead.
 */
export function jobsWorkers(deps: Deps) {
  const inbound = deps.inbound;
  return {
    runs: async (request: RunRequest): Promise<void> => {
      const party = await inbound.principalOf(request.party, { grant: request.grant });
      const grant = party.grants?.[0];
      // The grant ended since the message came: the run reaches nothing.
      if (!grant) return;
      const message = await inbound.message(request.message);
      if (!message) return;
      const report = await deps.orm.as(party).report.createOne({ jobId: grant.targetId, body: String(message.body) });
      await inbound.file({ message: request.message, to: { table: 'Report', key: String(report.id) }, as: party });
      await inbound.file({ message: request.message, to: { table: grant.targetType, key: grant.targetId }, as: party });
    },
  };
}
