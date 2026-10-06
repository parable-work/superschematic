import { Default, Secret } from "@superschematic/schema";
import { envVars } from "@superschematic/schema-config";

export abstract class PaymentsSecrets {
  STRIPE_KEY: Secret<string>;
}

@envVars
export abstract class OrdersConfig extends PaymentsSecrets {
  FULFILLMENT_REGION: string;
  MAX_LINE_ITEMS: Default<number, 50>;
}
