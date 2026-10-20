//! `export_engine_napi!`: the addon's Node-API exports, emitted over a
//! project.

/// Emits the engine's Node-API addon over a project: the `#[napi]`
/// functions and the `NativeEngine` class `@superschematic/engine`'s facade
/// loads (`src/native/addon.ts`), each a shell over the library's
/// [`Native`](crate::__private::Native), and the project's behaviors.
///
/// ```ignore
/// // Every behavior the addon is built with: the core's and an extension's.
/// fn behaviors() -> superschematic_engine_napi::BehaviorSet {
///     superschematic_engine_behaviors::core().with(acme_behaviors::Rating)
/// }
/// superschematic_engine_napi::export_engine_napi!(behaviors = behaviors);
/// ```
///
/// `behaviors` returns every behavior the addon is built with, the core's
/// among them. An engine registers the core's of them when it opens, then
/// each other one its options' `behaviors` name, in their order, as the
/// TypeScript engine registers a deployment's after the core's; a name it
/// is not built with is refused. (The C ABI's `export_engine_c_abi!`
/// registers all of them, since a Go server's config names none.)
///
/// The project's name, which the addon's `project()` reports, is the
/// crate's (`CARGO_PKG_NAME`). Node-API registers what the expansion
/// exports when the addon loads, so a cdylib expands the macro once and
/// depends on no other addon crate (`superschematic-engine-addon`, the
/// core's, among them), whose exports its own would register again. The
/// cdylib's build script runs `napi_build::setup()`. With the library's
/// `js-behaviors` feature the expansion also exports the test addon's
/// `bridgeCall`; without it, as in every shipped addon, there is none.
#[macro_export]
macro_rules! export_engine_napi {
    (behaviors = $behaviors:path) => {
        #[doc(hidden)]
        static __SUPERSCHEMATIC_ENGINE_NAPI_PROJECT: $crate::Project = $crate::Project {
            name: ::core::env!("CARGO_PKG_NAME"),
            behaviors: $behaviors,
        };

        // The exports, in a module of their own so the names they are
        // written with stay out of the crate's. `#[napi]` writes paths from
        // `napi::`, which the first import resolves. Each item's `#[napi]`
        // is named by its path from `$crate`, not through an import of this
        // expansion: rustc defers an attribute whose import it has not
        // resolved yet, and expands deferred ones in the reverse of their
        // order, while napi-derive expands a class's `impl` only after its
        // struct. A method's `#[napi]` is napi-derive's own to read, inside
        // the impl's.
        #[doc(hidden)]
        mod __superschematic_engine_napi {
            use $crate::__private::napi;
            use $crate::__private::napi::bindgen_prelude::*;
            use $crate::__private::{
                engine_routes as routes, CallTask, HostFunction, HttpAnswer, Native, StreamPull,
            };
            use $crate::ABI_VERSION;

            use super::__SUPERSCHEMATIC_ENGINE_NAPI_PROJECT as PROJECT;

            /// The version of the contract between the addon and the
            /// facade, which refuses an addon of another.
            #[$crate::__private::napi_derive::napi]
            pub fn abi_version() -> u32 {
                ABI_VERSION
            }

            /// The routes the engine serves, as JSON: what the facade's
            /// HTTP adapters mount, for any engine.
            #[$crate::__private::napi_derive::napi]
            pub fn engine_routes() -> String {
                routes()
            }

            /// The name of the crate that built this addon: the core's
            /// (`superschematic-engine-addon`) or a project's.
            #[$crate::__private::napi_derive::napi]
            pub fn project() -> &'static str {
                PROJECT.name
            }

            $crate::__napi_bridge! {
                use $crate::__private::bridge_call as bridge_call_of;

                /// A behavior context's method while its hook runs, on the
                /// test addon alone.
                #[$crate::__private::napi_derive::napi]
                pub fn bridge_call(env: Env, generation: f64, method: String, args: String) -> Result<String> {
                    bridge_call_of(env, generation, method, args)
                }
            }

            /// An engine, opened from JavaScript.
            #[$crate::__private::napi_derive::napi]
            pub struct NativeEngine {
                native: Native,
            }

            #[$crate::__private::napi_derive::napi]
            impl NativeEngine {
                /// Opens an engine on its options' JSON: its storage created
                /// if absent and migrated, the core's behaviors registered
                /// and the ones its `behaviors` name. `host` answers what
                /// the options' `hosted` lists.
                #[napi(factory)]
                pub fn open(env: Env, options: String, host: Option<HostFunction>) -> Result<NativeEngine> {
                    Native::open(env, &PROJECT, options, host).map(|native| NativeEngine { native })
                }

                /// Whether the facade answers something of this engine on
                /// the JavaScript thread alone.
                #[napi(getter)]
                pub fn hosted(&self) -> bool {
                    self.native.hosted()
                }

                /// Whether this addon has the bridge to behaviors written in
                /// TypeScript (the test addon).
                #[napi(getter)]
                pub fn bridged(&self) -> bool {
                    self.native.bridged()
                }

                /// Registers a behavior written in TypeScript, on the test
                /// addon alone.
                #[napi]
                pub fn register_behavior(&self, env: Env, descriptor: String) -> Result<()> {
                    self.native.register_behavior(env, descriptor)
                }

                /// `"sqlite"` or `"postgres"`.
                #[napi(getter)]
                pub fn dialect(&self) -> &'static str {
                    self.native.dialect()
                }

                /// Makes a call on the JavaScript thread; SQLite only.
                #[napi]
                pub fn call_sync(&self, env: Env, request: String) -> Result<String> {
                    self.native.call_sync(env, request)
                }

                /// Makes a call off the JavaScript thread, returning a
                /// promise of its result.
                #[napi(ts_return_type = "Promise<string>")]
                pub fn call<'env>(
                    &self,
                    env: &'env Env,
                    request: String,
                ) -> Result<Either<AsyncTask<CallTask>, PromiseRaw<'env, String>>> {
                    self.native.call(env, request)
                }

                /// Serves one HTTP request off the JavaScript thread.
                #[napi(ts_return_type = "Promise<HttpAnswer>")]
                pub fn handle_http_async<'env>(
                    &self,
                    env: &'env Env,
                    head: String,
                    body: Option<Buffer>,
                ) -> Result<PromiseRaw<'env, HttpAnswer>> {
                    self.native.handle_http_async(env, head, body)
                }

                /// Pulls an event stream off the JavaScript thread, waiting
                /// up to `timeoutMs` for its next bytes.
                #[napi(ts_return_type = "Promise<StreamPull>")]
                pub fn stream_pull_async<'env>(
                    &self,
                    env: &'env Env,
                    id: f64,
                    timeout_ms: u32,
                ) -> Result<PromiseRaw<'env, StreamPull>> {
                    self.native.stream_pull_async(env, id, timeout_ms)
                }

                /// Runs raw SQL on the engine's SQLite file.
                #[napi]
                pub fn storage_query(&self, env: Env, mode: String, sql: String, params: String) -> Result<String> {
                    self.native.storage_query(env, mode, sql, params)
                }

                /// Runs `callback` in a write transaction on the engine's
                /// SQLite file that this thread holds while it runs.
                #[napi(ts_args_type = "callback: () => boolean")]
                pub fn storage_transaction(&self, env: Env, callback: Function<(), bool>) -> Result<bool> {
                    self.native.storage_transaction(env, callback)
                }

                /// Whether this thread holds a transaction on the engine's
                /// storage.
                #[napi(getter)]
                pub fn storage_in_transaction(&self) -> bool {
                    self.native.storage_in_transaction()
                }

                /// Serves one HTTP request on the JavaScript thread
                /// (SQLite).
                #[napi]
                pub fn handle_http(&self, env: Env, head: String, body: Option<Buffer>) -> Result<HttpAnswer> {
                    self.native.handle_http(env, head, body)
                }

                /// Pulls an event stream `handleHttp` holds open, without
                /// waiting.
                #[napi]
                pub fn stream_pull(&self, env: Env, id: f64) -> Result<StreamPull> {
                    self.native.stream_pull(env, id)
                }

                /// Ends an event stream `handleHttp` holds open.
                #[napi]
                pub fn stream_close(&self, id: f64) {
                    self.native.stream_close(id)
                }

                /// How many commits that notified the engine's notifier has
                /// counted.
                #[napi(getter)]
                pub fn epoch(&self) -> f64 {
                    self.native.epoch()
                }

                /// Calls `callback(epoch)` on the JavaScript thread each time
                /// a commit moves the engine's notifier.
                #[napi(ts_args_type = "callback: (epoch: number) => void")]
                pub fn watch(&self, env: Env, callback: Function<f64, ()>) -> Result<()> {
                    self.native.watch(env, callback)
                }

                /// Ends the watch `watch` started.
                #[napi]
                pub fn unwatch(&self) {
                    self.native.unwatch()
                }

                /// Stops the runner and ends every event stream.
                #[napi]
                pub fn shutdown(&self) {
                    self.native.shutdown()
                }

                /// Shuts the engine down and lets it go.
                #[napi]
                pub fn close(&self) {
                    self.native.close()
                }
            }
        }
    };
}

/// The test addon's exports, which [`export_engine_napi!`] emits when the
/// library has the `js-behaviors` feature, whatever the expanding crate's
/// own features: a `cfg` in a macro's body reads the crate it expands in.
#[cfg(feature = "js-behaviors")]
#[doc(hidden)]
#[macro_export]
macro_rules! __napi_bridge {
    ($($items:tt)*) => {
        $($items)*
    };
}

/// Without the `js-behaviors` feature, nothing: a shipped addon has no
/// bridge.
#[cfg(not(feature = "js-behaviors"))]
#[doc(hidden)]
#[macro_export]
macro_rules! __napi_bridge {
    ($($items:tt)*) => {};
}
