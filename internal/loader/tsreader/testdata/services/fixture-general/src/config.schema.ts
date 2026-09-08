import { Network } from "@psgen/scalar-lib";
import { Default, Nullable, Secret, Validate, jsonField } from "@psgen/schema";
import { envVars } from "@psgen/schema-config";

// Runtime environment classification.
export enum FixtureEnvironment {
  Development = "development",
  Production = "production"
}

export type RetryPolicy = {
  readonly maxAttempts: number;
  readonly backoffSeconds?: number;
};

@envVars
export abstract class FixtureConfig {
  DATABASE_URL: Network.Url;
  JWT_SECRET: Secret<string>;
  PORT: Default<number, 8080>;
  ENVIRONMENT: Default<FixtureEnvironment, FixtureEnvironment.Development>;
}

// JSON-persisted filter: exercises optional-list constraint gating.
// Absent/null must skip listMin; an explicit [] must fail it (decode must
// not normalize an absent optional list into an empty non-nil slice).
@jsonField
export abstract class FixtureFilter {
  kind: Validate<string, { maxLength: 32 }>;

  values: Nullable<Validate<string[], { maxLength: 64; listMin: 1; listMax: 10 }>>;
}
