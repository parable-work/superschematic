# `@superschematic/engine-linux-arm64-gnu`

The **aarch64-unknown-linux-gnu** build of the Rust engine's Node-API addon
(D58) for `@superschematic/engine`, which names this package as an optional
dependency and loads it on linux/arm64 with glibc. Install
`@superschematic/engine`, not this package.

The release builds it from `runtime/engine/rust/addon` (the engine's addon
over the core's behaviors and the work queue's) with the `dist` profile:
LTO, one codegen unit, symbols stripped. It has no bridge to behaviors
written in TypeScript. A project whose schemas compose an extension's
behaviors builds an addon of its own (`scripts/engine-napi.sh --crate`) and
names it with the engine option `addon` or `SUPERSCHEMATIC_ENGINE_ADDON`.
