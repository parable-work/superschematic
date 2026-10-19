import { Geo, Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { queue } from "@superschematic/db";

// Planned when a courier takes an order: where to leave it, and how long
// the courier waits there. Postgres holds a location as a POINT and a
// duration as an INTERVAL.
@queue({ retries: 2, backoff: "30s" })
export abstract class DeliveryPlanned {
  orderId: Identity.UUID;

  dropAt: Geo.Location;

  wait: Temporal.Duration;

  meetAt: Nullable<Geo.Location>;

  grace: Nullable<Temporal.Duration>;
}
