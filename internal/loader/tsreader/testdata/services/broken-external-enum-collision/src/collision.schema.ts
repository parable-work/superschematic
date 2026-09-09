import { TenantStatus as DatabaseTenantStatus } from "@schemas/fixture-db";
import { TenantStatus as GeneralTenantStatus } from "@schemas/fixture-enum-guardrails";

export abstract class CollisionExample {
  databaseStatus: DatabaseTenantStatus;
  generalStatus: GeneralTenantStatus;
}
