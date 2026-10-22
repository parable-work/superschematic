# The telemetry fixture (D67, D64 amended): trace export on Workers.
# shop-storefront's bundle (app.ts) runs as two Workers under the
# nodejs_als compatibility flag, which every Worker version of a server or
# a job on the cloudflare target names: "app", with the OTEL_ bindings that
# turn export on, and "off", with none. Each reaches shop-pricing
# (pricing.js) through a service binding, and the receiver (receiver.ts)
# is each one's global outbound, so the exporter's fetch to the endpoint
# reaches it. The tester (tester.js) drives both and reads what the
# receiver kept.
using Workerd = import "/workerd/workerd.capnp";

const config :Workerd.Config = (
  services = [
    (name = "app", worker = .app),
    (name = "off", worker = .off),
    (name = "tel-stack-staging-shop-pricing", worker = .pricing),
    (name = "receiver", worker = .receiver),
    (name = "tester", worker = .tester),
  ],
);

const app :Workerd.Worker = (
  modules = [
    (name = "worker.js", esModule = embed "app.js"),
    (name = "superscalar_wasm_bg.wasm", wasm = embed "superscalar_wasm_bg.wasm"),
  ],
  bindings = [
    (name = "OTEL_EXPORTER_OTLP_ENDPOINT", text = "http://collector.example"),
    (name = "OTEL_EXPORTER_OTLP_HEADERS", text = "x-collector-key=k1"),
    (name = "OTEL_RESOURCE_ATTRIBUTES", text = "deployment.environment.name=Staging"),
    (name = "PRICING", service = "tel-stack-staging-shop-pricing"),
  ],
  globalOutbound = "receiver",
  compatibilityDate = "2026-09-15",
  compatibilityFlags = ["nodejs_als"],
);

const off :Workerd.Worker = (
  modules = [
    (name = "worker.js", esModule = embed "app.js"),
    (name = "superscalar_wasm_bg.wasm", wasm = embed "superscalar_wasm_bg.wasm"),
  ],
  bindings = [(name = "PRICING", service = "tel-stack-staging-shop-pricing")],
  globalOutbound = "receiver",
  compatibilityDate = "2026-09-15",
  compatibilityFlags = ["nodejs_als"],
);

const pricing :Workerd.Worker = (
  modules = [(name = "worker.js", esModule = embed "pricing.js")],
  compatibilityDate = "2026-09-15",
);

const receiver :Workerd.Worker = (
  modules = [(name = "worker.js", esModule = embed "receiver.js")],
  compatibilityDate = "2026-09-15",
);

const tester :Workerd.Worker = (
  modules = [(name = "worker.js", esModule = embed "tester.js")],
  bindings = [
    (name = "APP", service = "app"),
    (name = "OFF", service = "off"),
    (name = "RECEIVER", service = "receiver"),
  ],
  compatibilityDate = "2026-09-15",
);
