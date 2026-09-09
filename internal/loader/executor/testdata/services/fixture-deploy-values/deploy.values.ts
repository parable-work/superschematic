/**
 * Executor fixture: a deploy values document built from arbitrary value
 * expressions (spreads, computed entries, helper calls) that only execution
 * can evaluate. The default export is the pinned JSON contract shape.
 */

const services = ["web-api", "web-admin-api"];
const basePort = 8080;

function secretRef(secretName: string, secretKey: string) {
  return { __kind: "secretRef", secretName, secretKey };
}

export default {
  config: { name: "fixture-deploy-values", kind: "General" },
  services: [...services],
  base: {
    common: { LOG_LEVEL: "info", DD_APPSEC_SCA_ENABLED: true },
    perService: Object.fromEntries(
      services.map((name, i) => [name, { env: { PORT: basePort + i } }])
    )
  },
  environments: {
    production: {
      common: { ENVIRONMENT: "production" },
      perService: {
        "web-api": {
          env: { JWT_SECRET: secretRef("acme-apps-jwt", "jwt-secret") },
          scaling: { replicas: 3 }
        }
      }
    }
  }
};
