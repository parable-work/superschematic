//! The core's Node-API addon (D58): the engine's addon
//! (`superschematic-engine-napi`) over the core's behaviors
//! ([`superschematic_engine_behaviors::core`]), the work queue's
//! ([`superschematic_engine_workqueue::workqueue`]) and the commerce
//! behaviors ([`superschematic_engine_commerce::commerce`]), which
//! `@superschematic/engine` loads from the checkout's `native/`
//! (`scripts/engine-napi.sh`) or from the release's
//! `@superschematic/engine-<target>` package. An engine registers the
//! core's when it opens, and the work queue's and the commerce ones its
//! options' `behaviors` name.
//! A project whose schemas compose an extension's behaviors builds an addon
//! of its own from a crate like this one over its behaviors, and loads it
//! in this one's place; it never depends on this crate, whose exports its
//! own would register again.

use superschematic_engine_napi::BehaviorSet;

/// Every behavior the addon is built with: the core's, then the work
/// queue's, then the commerce ones.
fn behaviors() -> BehaviorSet {
    superschematic_engine_behaviors::core()
        .extend(superschematic_engine_workqueue::workqueue())
        .extend(superschematic_engine_commerce::commerce())
}

superschematic_engine_napi::export_engine_napi!(behaviors = behaviors);
