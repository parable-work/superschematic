// The notes app's wiring of its runtime providers (D159): the slots its
// @envVars class declares, the provider packages its server links, and two
// things core will do through them, which sections 3 and 8 build: send a
// notice through the channel adapter the environment configures, and read
// the replies its relay posts to a @webhook route. Nothing here names a
// provider: swapping one for another is the environment's config alone.
import {
  createLogger,
  type Logger,
} from '@superschematic/http-runtime';
import {
  providersFrom,
  type InboundMessage,
  type Providers,
  type RuntimeInterface,
  type SendResult,
  type WebhookRequest,
} from '@superschematic/http-runtime/providers';

import { localSandbox } from '../providers/local-sandbox/src/index.ts';
import { smsRelayA } from '../providers/sms-relay-a/src/index.ts';
import { smsRelayB } from '../providers/sms-relay-b/src/index.ts';
import { stubModel } from '../providers/stub-model/src/index.ts';
import { textPreviewProcessor } from '../providers/text-preview/src/index.ts';
import { vaultBroker } from '../providers/vault-broker/src/index.ts';

/** The provider slots of NotesApiConfig, as its @providers declares them (schemas/services/notes-api). */
export const NOTES_API_SLOTS: Readonly<Record<string, RuntimeInterface>> = Object.freeze({
  SMS: 'channel',
  VAULT: 'credentials',
  RUNNER: 'sandbox',
  MODEL: 'model',
  PREVIEWS: 'files',
});

/** What the server's providers reach their services with: the platform's fetch, or a test's. */
export interface NotesProvidersOptions {
  readonly fetch?: typeof fetch;
  readonly log?: Logger;
}

/** The providers config, the API's loaded config, configures, over the packages notes-api lists. */
export function notesProviders(config: Readonly<Record<string, unknown>>, options: NotesProvidersOptions = {}): Providers {
  const fetch = options.fetch;
  return providersFrom({
    slots: NOTES_API_SLOTS,
    config,
    definitions: [smsRelayA({ fetch }), smsRelayB({ fetch }), vaultBroker({ fetch }), localSandbox, stubModel, textPreviewProcessor],
    log: options.log ?? createLogger({ service: 'notes-api' }),
  });
}

/** A notice to one recipient. */
export interface Notice {
  readonly to: string;
  readonly body: string;
  readonly reference: string;
}

/** Sends notice through the SMS slot's adapter, after its health check; an adapter that is down is refused. */
export async function sendNotice(providers: Providers, notice: Notice): Promise<SendResult> {
  const sms = await providers.channel('SMS');
  const health = await sms.health();
  if (!health.ok) throw new Error(`the SMS channel is down: ${health.detail ?? 'no detail'}`);
  return sms.send({ to: notice.to, body: `${notice.body} (ref ${notice.reference})`, reference: notice.reference, idempotencyKey: notice.reference });
}

/** The replies a @webhook request from the SMS slot's relay carries; none from an adapter that takes no replies. */
export async function receiveReplies(providers: Providers, request: WebhookRequest): Promise<readonly InboundMessage[]> {
  const sms = await providers.channel('SMS');
  return sms.inbound ? sms.inbound(request) : [];
}
