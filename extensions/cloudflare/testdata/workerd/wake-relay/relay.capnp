# A drainer waking another drainer through the other's wake relay under
# workerd (#600, D151). "drainer" stands in for the waking drainer's
# Worker: the runtime's queue waker (drainer.js, bundled from waker.ts)
# with the QUEUE_WAKES and the WAKE_ service bindings the cloudflare.drainer
# platform gives its version, each a binding of the relay. "relay" runs the
# script the extension embeds (wake-relay.js) with WORKFLOW, which workerd
# cannot run, as the stand-in's Workflow entrypoint (workflow.js), which
# records each instance created and refuses an id it holds, as a
# Workflow does. TestWakeRelayUnderWorkerd writes the embedded script and
# the bundle beside this file, puts the version's QUEUE_WAKES and its wake
# bindings in place of the drainer's placeholders, then runs `workerd test`.
using Workerd = import "/workerd/workerd.capnp";

const config :Workerd.Config = (
  services = [
    (name = "drainer", worker = .drainer),
    (name = "relay", worker = .relay),
    (name = "workflow", worker = .workflow),
  ],
);

const drainer :Workerd.Worker = (
  modules = [(name = "drainer.js", esModule = embed "drainer.js")],
  bindings = [
    (name = "QUEUE_WAKES", text = "$QUEUE_WAKES"),
    $WAKE_BINDINGS
    (name = "RELAY", service = "relay"),
    (name = "RECORDED", service = (name = "workflow", entrypoint = "Workflow")),
  ],
  compatibilityDate = "2026-09-15",
  compatibilityFlags = ["nodejs_als"],
);

const relay :Workerd.Worker = (
  modules = [(name = "wake-relay.js", esModule = embed "wake-relay.js")],
  bindings = [
    (name = "WORKFLOW", service = (name = "workflow", entrypoint = "Workflow")),
  ],
  compatibilityDate = "2026-09-15",
);

const workflow :Workerd.Worker = (
  modules = [(name = "workflow.js", esModule = embed "workflow.js")],
  compatibilityDate = "2026-09-15",
);
