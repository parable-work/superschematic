import { Network } from "superscalar";
import { Default, Nullable, Secret, Validate, docs, icon, jsonField, purpose } from "@superschematic/schema";
import { envVars } from "@superschematic/schema-config";

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
  @docs({ title: "Database URL" })
  @purpose("Connection target for the **primary database**.")
  @icon("database")
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
