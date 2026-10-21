# The telemetry-refused fixture (D67, D64 amended): the telemetry fixture's
# app with a sampler the runtime does not support, which it refuses at
# module scope, so workerd, as Cloudflare refuses the version's upload,
# fails to load it with the message in expected-failure.txt.
using Workerd = import "/workerd/workerd.capnp";

const config :Workerd.Config = (
  services = [(name = "app", worker = .app)],
);

const app :Workerd.Worker = (
  modules = [
    (name = "worker.js", esModule = embed "app.js"),
    (name = "superscalar_wasm_bg.wasm", wasm = embed "superscalar_wasm_bg.wasm"),
  ],
  bindings = [
    (name = "OTEL_EXPORTER_OTLP_ENDPOINT", text = "http://collector.example"),
    (name = "OTEL_TRACES_SAMPLER", text = "jaeger_remote"),
  ],
  compatibilityDate = "2026-09-15",
  compatibilityFlags = ["nodejs_als"],
);
