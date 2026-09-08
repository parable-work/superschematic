import { TenantStatus as DatabaseTenantStatus } from "@parable-platform/fixture-db";
import { TenantStatus as GeneralTenantStatus } from "@parable-platform/fixture-enum-guardrails";

export abstract class CollisionExample {
  databaseStatus: DatabaseTenantStatus;
  generalStatus: GeneralTenantStatus;
}
