import { Network } from "superscalar";
import { Default, Nullable, Secret } from "@superschematic/schema";
import { envVars } from "@superschematic/schema-config";

// How the fixture service runs.
export enum FixtureEnvMode {
  Direct = "direct",
  Queued = "queued"
}

// One field of each shape the TypeScript env loader parses: required and
// optional, plain and secret, string, integer, boolean, enum, and a URL scalar,
// with and without defaults.
@envVars
export abstract class FixtureEnvConfig {
  // Required plain string.
  SERVICE_NAME: string;

  // Required secret.
  SIGNING_KEY: Secret<string>;

  // Optional secret.
  PROVIDER_API_KEY: Nullable<Secret<string>>;

  // Required URL scalar.
  UPSTREAM_URL: Network.Uri;

  // URL scalar with a default.
  CALLBACK_URL: Nullable<Default<Network.Uri, "http://localhost:8080">>;

  // Integer with a default.
  PORT: Nullable<Default<number, 8095>>;

  // Optional integer.
  MAX_TABS: Nullable<number>;

  // Boolean with a default.
  ALLOW_VIDEO: Nullable<Default<boolean, false>>;

  // Optional boolean without a default.
  PREVIEW_IMAGES: Nullable<boolean>;

  // Enum with a default.
  MODE: Nullable<Default<FixtureEnvMode, FixtureEnvMode.Direct>>;

  // Optional string.
  VERSION: Nullable<string>;
}
