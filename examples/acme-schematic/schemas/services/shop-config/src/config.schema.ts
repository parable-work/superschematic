import { Network } from "superscalar";
import { Default, Secret, docs, icon, purpose } from "@superschematic/schema";
import { envVars } from "@acme/schema-config";

export enum ShopEnvironment {
  Development = "development",
  Production = "production"
}

// The environment variables the shop API reads at start-up.
@envVars
export abstract class ShopConfig {
  // Field presentation for a settings UI. The acme extension accepts only
  // icons from its own set.
  @docs({ title: "Database URL" })
  @purpose("Connection string of the **shop database**.")
  @icon("globe")
  DATABASE_URL: Network.Url;

  @docs({ title: "API key pepper" })
  @purpose("Secret mixed into every stored API key hash.")
  @icon("key")
  API_KEY_PEPPER: Secret<string>;
  PORT: Default<number, 8080>;
  ENVIRONMENT: Default<ShopEnvironment, ShopEnvironment.Development>;
}

// A currency amount as the API renders it.
export abstract class Money {
  amountCents: number;
  currency: string;
}
