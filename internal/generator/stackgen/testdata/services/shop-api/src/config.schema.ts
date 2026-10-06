import { Default, Nullable, Secret } from "@superschematic/schema";
import { envVars } from "@superschematic/schema-config";

// A secret is identified by the type that declares it: shop-orders declares
// the same PaymentsSecrets, so the two servers share one STRIPE_KEY.
export abstract class PaymentsSecrets {
  STRIPE_KEY: Secret<string>;
}

@envVars
export abstract class ShopApiConfig extends PaymentsSecrets {
  LOG_LEVEL: Default<string, "info">;
  PREVIEW_ID: Nullable<string>;
}
