import { Nullable, Trait } from "@superschematic/schema";
import { envVars } from "@superschematic/schema-config";

// The settings the invoice worker writes for itself. The config composes
// this class as it is, so its required field stays required.
export abstract class InvoiceWorkerEnv {
  // Required string.
  INVOICE_QUEUE: string;

  // Optional string.
  INVOICE_REGION: Nullable<string>;
}

// The settings a deploy writes. A local run has none of them, so the config
// composes this class through Partial: each field is optional to the loader.
export abstract class InvoiceDeployEnv {
  // Required for the deploy, optional to the loader.
  INVOICE_RELEASE: string;

  // Required for the deploy, optional to the loader.
  INVOICE_SIGNING_KEY_ID: string;
}

@envVars
export abstract class InvoiceConfig implements Trait<InvoiceWorkerEnv>, Trait<Partial<InvoiceDeployEnv>> {
  // Required string of the config itself.
  SERVICE_NAME: string;
}
