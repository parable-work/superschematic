import { Generic, Identity } from "superscalar";
import { Nullable, Secret } from "@superschematic/schema";
import {
  Authenticated,
  HttpMethod,
  behaviorWorker,
  paginated,
  requirePermission,
  rest,
  source,
  tableOperation,
  worker
} from "@superschematic/api";
import { envVars, providers } from "@superschematic/schema-config";
import { Duty, NoticeRoute, NoticeWork, ReferenceCleared } from "@schemas/tickets-db";

// The API's config (D159). SMS and MAIL are channel slots: each
// environment names the adapter that serves each, and the settings after
// it. The notifier sends on every channel slot an environment fills, and
// an environment that fills none is refused, since tickets-db raises
// events (D153).
@envVars
@providers({ SMS: "channel", MAIL: "channel" })
export abstract class TicketsApiConfig {
  SMS: Nullable<string>;
  SMS_URL: Nullable<string>;
  SMS_TOKEN: Nullable<Secret<string>>;
  SMS_SENDER: Nullable<string>;

  MAIL: Nullable<string>;
  MAIL_URL: Nullable<string>;
  MAIL_TOKEN: Nullable<Secret<string>>;
  MAIL_FROM: Nullable<string>;
}

// A route as its editors read and write it: the event kind it matches, a
// filter on the record, its recipients, an urgency and an escalation.
@source(NoticeRoute)
export abstract class RouteView {
  id: Identity.UUID;
  name: string;
  eventKind: string;
  subjectTable: Nullable<string>;
  filter: Nullable<Generic.JSON>;
  recipients: Generic.JSON;
  urgency: Nullable<string>;
  escalation: Nullable<Generic.JSON>;
  enabled: boolean;
}

@source(NoticeRoute)
export abstract class RouteInput {
  name: string;
  eventKind: string;
  subjectTable: Nullable<string>;
  filter: Nullable<Generic.JSON>;
  recipients: Generic.JSON;
  urgency: Nullable<string>;
  escalation: Nullable<Generic.JSON>;
}

// Routes are rows; editing them is a permission (D153).
export class Routes extends Authenticated {
  @rest(HttpMethod.GET, "notifications/routes")
  @requirePermission(["notifications.routes"])
  @paginated()
  @tableOperation()
  listRoutes(): RouteView[] {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.POST, "notifications/routes")
  @requirePermission(["notifications.routes"])
  @tableOperation()
  createRoute(input: RouteInput): RouteView {
    throw new Error("schema declaration only");
  }
}

// Runs the notifier: routes each event, sends each delivery when it is
// due, and escalates what nobody acknowledged (D153).
@worker({ queue: NoticeWork, concurrency: 4 })
export abstract class Notify {}

// Records a deleted ticket on each ticket that referred to it (D154).
@worker({ queue: ReferenceCleared })
export abstract class HearCleared {}

// Fires each duty's deadlines and hands Recurrence's events to the
// notifier (D152, D153).
@behaviorWorker(Duty, { permissions: ["tickets.work"] })
export abstract class DutyDeadlines {}
