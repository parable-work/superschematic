import { Identity } from "@psgen/scalar-lib";
import { source } from "@psgen/api";
import { Tenant } from "@parable-platform/fixture-db";

// Projection with a type mismatch and an unmarked extra field: both are
// verification errors.
@source(Tenant)
export abstract class BrokenTenantView {
  id: Identity.UUID;
  name: number;
  extraField: string;
}

// Projection omitting the @sourceMustProject name field: a verification
// warning.
@source(Tenant)
export abstract class SlimTenantView {
  id: Identity.UUID;
}
