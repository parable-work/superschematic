import { Network } from "superscalar";
import { Default, Secret } from "@superschematic/schema";
import { envVars } from "@superschematic/schema-config";

export enum ShopEnvironment {
  Development = "development",
  Production = "production"
}

// The environment variables the shop API reads at start-up.
@envVars
export abstract class ShopConfig {
  DATABASE_URL: Network.Url;
  API_KEY_PEPPER: Secret<string>;
  PORT: Default<number, 8080>;
  ENVIRONMENT: Default<ShopEnvironment, ShopEnvironment.Development>;
}

// A currency amount as the API renders it.
export abstract class Money {
  amountCents: number;
  currency: string;
}
