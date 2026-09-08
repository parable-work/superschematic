import { TenantStatus } from "@parable-platform/fixture-enum-guardrails";

export abstract class ValidExternalEnumExample {
  primaryStatus: TenantStatus;
  secondaryStatus: TenantStatus;
}
