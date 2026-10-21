# ts-cron-stack's job CompileReports under workerd (D64,
# docs/stack-model.md, section 18.9): four Workers run the bundle its
# generated bundle.ts writes from scheduled.ts, main.js and superscalar's
# WebAssembly module, with the bindings the cloudflare.cron platform gives
# its version: REPORT_FAILURES as text, how many tries fail before one
# succeeds, and REPORT_TOKEN as a Secrets Store secret, here a wrapped
# stand-in (standins.js), and SUPERSCHEMATIC_BUNDLE, the digest of the
# bundle the deploy pins. The tester (tester.js) fires each Worker's
# scheduled handler through a service binding, as its Cron Trigger fires
# it, and runs the job's Workflow, JobRun, through a binding to that
# entrypoint, as Cloudflare's Workflows engine calls its run with an event
# and a step (D64, amended). TestTypeScriptCronEntrypointRunsUnderWorkerd copies the bundle,
# tester.js and standins.js beside this file and runs `workerd test`.
using Workerd = import "/workerd/workerd.capnp";

const config :Workerd.Config = (
  services = [
    (name = "ok", worker = .ok),
    (name = "flaky", worker = .flaky),
    (name = "broken", worker = .broken),
    (name = "hung", worker = .hung),
    (name = "tester", worker = .tester),
  ],
  extensions = [(
    modules = [(name = "superschematic-fixture:standins", esModule = embed "standins.js", internal = true)],
  )],
);

const ok :Workerd.Worker = (
  modules = [
    (name = "main.js", esModule = embed "main.js"),
    (name = "superscalar_wasm_bg.wasm", wasm = embed "superscalar_wasm_bg.wasm"),
  ],
  bindings = [
    (name = "REPORT_FAILURES", text = "0"),
    (name = "SUPERSCHEMATIC_BUNDLE", text = "b7b7b7"),
    (name = "REPORT_TOKEN", wrapped = (moduleName = "superschematic-fixture:standins", entrypoint = "secret", innerBindings = [(name = "value", text = "rt_test")])),
  ],
  compatibilityDate = "2026-09-15",
  compatibilityFlags = ["nodejs_als"],
);

const flaky :Workerd.Worker = (
  modules = [
    (name = "main.js", esModule = embed "main.js"),
    (name = "superscalar_wasm_bg.wasm", wasm = embed "superscalar_wasm_bg.wasm"),
  ],
  bindings = [
    (name = "REPORT_FAILURES", text = "1"),
    (name = "SUPERSCHEMATIC_BUNDLE", text = "b7b7b7"),
    (name = "REPORT_TOKEN", wrapped = (moduleName = "superschematic-fixture:standins", entrypoint = "secret", innerBindings = [(name = "value", text = "rt_test")])),
  ],
  compatibilityDate = "2026-09-15",
  compatibilityFlags = ["nodejs_als"],
);

const broken :Workerd.Worker = (
  modules = [
    (name = "main.js", esModule = embed "main.js"),
    (name = "superscalar_wasm_bg.wasm", wasm = embed "superscalar_wasm_bg.wasm"),
  ],
  bindings = [
    (name = "REPORT_FAILURES", text = "2"),
    (name = "SUPERSCHEMATIC_BUNDLE", text = "b7b7b7"),
    (name = "REPORT_TOKEN", wrapped = (moduleName = "superschematic-fixture:standins", entrypoint = "secret", innerBindings = [(name = "value", text = "rt_test")])),
  ],
  compatibilityDate = "2026-09-15",
  compatibilityFlags = ["nodejs_als"],
);

const hung :Workerd.Worker = (
  modules = [
    (name = "main.js", esModule = embed "main.js"),
    (name = "superscalar_wasm_bg.wasm", wasm = embed "superscalar_wasm_bg.wasm"),
  ],
  bindings = [
    (name = "REPORT_FAILURES", text = "hang"),
    (name = "SUPERSCHEMATIC_BUNDLE", text = "b7b7b7"),
    (name = "REPORT_TOKEN", wrapped = (moduleName = "superschematic-fixture:standins", entrypoint = "secret", innerBindings = [(name = "value", text = "rt_test")])),
  ],
  compatibilityDate = "2026-09-15",
  compatibilityFlags = ["nodejs_als"],
);

const tester :Workerd.Worker = (
  modules = [(name = "tester.js", esModule = embed "tester.js")],
  bindings = [
    (name = "OK", service = "ok"),
    (name = "FLAKY", service = "flaky"),
    (name = "BROKEN", service = "broken"),
    (name = "HUNG", service = "hung"),
    (name = "OK_RUN", service = (name = "ok", entrypoint = "JobRun")),
    (name = "HUNG_RUN", service = (name = "hung", entrypoint = "JobRun")),
  ],
  compatibilityDate = "2026-09-15",
);
