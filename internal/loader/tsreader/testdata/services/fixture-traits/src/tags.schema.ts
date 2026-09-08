import { Trait, trait } from "@superschematic/schema";

// Configuration accepted by the Tagged trait.
export abstract class TagConfig {
  channel: string;
  priority?: number;
}

// Tags implementing types with routing configuration.
@trait({ prefix: "tag" })
export abstract class Tagged<Config extends TagConfig> {}

// Marker trait carrying no configuration.
@trait()
export abstract class Reviewed {}

// Field-bearing trait: contributes fields to every implementing type via
// the Trait<T> heritage carrier.
@trait()
export abstract class Expirable {
  expiresAt: string;
}

// A notification routed through a tagging channel.
export abstract class Notification implements Tagged<{ channel: "email"; priority: 2 }>, Reviewed, Trait<Expirable> {
  subject: string;
}
