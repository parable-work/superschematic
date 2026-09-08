import { TenantStatus } from "@schemas/fixture-enum-guardrails";

export abstract class ValidExternalEnumExample {
  primaryStatus: TenantStatus;
  secondaryStatus: TenantStatus;
}
