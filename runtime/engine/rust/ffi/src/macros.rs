//! `export_engine_c_abi!`: the exported C symbols, emitted over a project.

/// Emits the engine's C ABI (the ten `extern "C"` functions
/// `runtime/engine/go/include/superschematic_engine.h` declares) over a
/// project: the behaviors its engine runs, or a configure of its own.
///
/// ```ignore
/// // The behaviors alone: the core's and an extension's.
/// fn behaviors() -> superschematic_engine_ffi::BehaviorSet {
///     superschematic_engine_behaviors::core().with(acme_behaviors::Rating)
/// }
/// superschematic_engine_ffi::export_engine_c_abi!(behaviors = behaviors);
///
/// // A configure of its own, which also reads the config's `project`
/// // member (a Rust access policy, say).
/// fn configure(
///     options: &mut superschematic_engine_ffi::EngineOptions,
///     project: Option<&superschematic_engine_ffi::JsonValue>,
/// ) -> Result<(), String> {
///     options.behaviors = superschematic_engine_behaviors::core();
///     Ok(())
/// }
/// superschematic_engine_ffi::export_engine_c_abi!(configure = configure);
/// ```
///
/// The project's name, which `eng_open` and the `abi` call report, is the
/// crate's (`CARGO_PKG_NAME`). `#[no_mangle]` exports each name verbatim,
/// so a static library invokes the macro once, at the crate root, and
/// depends on `superschematic-engine-ffi` with `default-features = false`:
/// the macro refuses to expand beside the crate's `core` feature
/// ([`crate::CORE_ASSEMBLY`]), whose archive already exports the ABI over
/// the core's behaviors. The `@emit` arm is that assembly's own and skips
/// the check.
#[macro_export]
macro_rules! export_engine_c_abi {
    (behaviors = $behaviors:path) => {
        const _: () = ::core::assert!(
            !$crate::CORE_ASSEMBLY,
            "superschematic-engine-ffi was built with its `core` feature and already exports the C ABI over the core's behaviors; depend on it with default-features = false"
        );
        $crate::export_engine_c_abi! { @emit behaviors = $behaviors }
    };
    (configure = $configure:path) => {
        const _: () = ::core::assert!(
            !$crate::CORE_ASSEMBLY,
            "superschematic-engine-ffi was built with its `core` feature and already exports the C ABI over the core's behaviors; depend on it with default-features = false"
        );
        $crate::export_engine_c_abi! { @emit configure = $configure }
    };
    (@emit behaviors = $behaviors:path) => {
        fn __superschematic_engine_configure(
            options: &mut $crate::EngineOptions,
            project: ::core::option::Option<&$crate::JsonValue>,
        ) -> ::core::result::Result<(), ::std::string::String> {
            $crate::with_behaviors(options, project, $behaviors)
        }
        $crate::export_engine_c_abi! { @emit configure = __superschematic_engine_configure }
    };
    (@emit configure = $configure:path) => {
        static __SUPERSCHEMATIC_ENGINE_PROJECT: $crate::Project = $crate::Project {
            name: ::core::env!("CARGO_PKG_NAME"),
            configure: $configure,
        };

        /// The version of the contract (`ENG_ABI_VERSION`).
        #[no_mangle]
        pub extern "C" fn eng_abi_version() -> u32 {
            $crate::ABI_VERSION
        }

        /// Opens an engine on its config.
        ///
        /// # Safety
        /// `config` must point to `config_len` readable bytes (or be null
        /// with `config_len` 0); `engine`, `out_ptr` and `out_len` must be
        /// valid for writes.
        #[no_mangle]
        pub unsafe extern "C" fn eng_open(
            config: *const u8,
            config_len: usize,
            engine: *mut u64,
            out_ptr: *mut *mut u8,
            out_len: *mut usize,
        ) -> i32 {
            $crate::abi::open(
                &__SUPERSCHEMATIC_ENGINE_PROJECT,
                config,
                config_len,
                engine,
                out_ptr,
                out_len,
            )
        }

        /// Shuts an engine down and lets its handle go.
        #[no_mangle]
        pub extern "C" fn eng_close(engine: u64) -> i32 {
            $crate::abi::close(engine)
        }

        /// Makes a call.
        ///
        /// # Safety
        /// `request` must point to `request_len` readable bytes; `out_ptr`
        /// and `out_len` must be valid for writes.
        #[no_mangle]
        pub unsafe extern "C" fn eng_call(
            engine: u64,
            request: *const u8,
            request_len: usize,
            out_ptr: *mut *mut u8,
            out_len: *mut usize,
        ) -> i32 {
            $crate::abi::call(engine, request, request_len, out_ptr, out_len)
        }

        /// Serves a request.
        ///
        /// # Safety
        /// `head` and `body` must point to `head_len` and `body_len`
        /// readable bytes; the four out pointers must be valid for writes.
        #[no_mangle]
        #[allow(clippy::too_many_arguments)]
        pub unsafe extern "C" fn eng_http(
            engine: u64,
            head: *const u8,
            head_len: usize,
            body: *const u8,
            body_len: usize,
            head_out_ptr: *mut *mut u8,
            head_out_len: *mut usize,
            body_out_ptr: *mut *mut u8,
            body_out_len: *mut usize,
        ) -> i32 {
            $crate::abi::http(
                engine,
                head,
                head_len,
                body,
                body_len,
                head_out_ptr,
                head_out_len,
                body_out_ptr,
                body_out_len,
            )
        }

        /// Pulls a stream without waiting.
        ///
        /// # Safety
        /// `out_ptr` and `out_len` must be valid for writes.
        #[no_mangle]
        pub unsafe extern "C" fn eng_stream_poll(
            engine: u64,
            stream: u64,
            out_ptr: *mut *mut u8,
            out_len: *mut usize,
        ) -> i32 {
            $crate::abi::stream_poll(engine, stream, out_ptr, out_len)
        }

        /// Ends a stream.
        #[no_mangle]
        pub extern "C" fn eng_stream_close(engine: u64, stream: u64) -> i32 {
            $crate::abi::stream_close(engine, stream)
        }

        /// Waits for a commit or the timeout.
        ///
        /// # Safety
        /// `epoch` must be valid for writes.
        #[no_mangle]
        pub unsafe extern "C" fn eng_wait(
            engine: u64,
            after_epoch: u64,
            timeout_ms: u32,
            epoch: *mut u64,
        ) -> i32 {
            $crate::abi::wait(engine, after_epoch, timeout_ms, epoch)
        }

        /// Drives the runner.
        ///
        /// # Safety
        /// `request` must point to `request_len` readable bytes; `out_ptr`
        /// and `out_len` must be valid for writes.
        #[no_mangle]
        pub unsafe extern "C" fn eng_runner(
            engine: u64,
            request: *const u8,
            request_len: usize,
            out_ptr: *mut *mut u8,
            out_len: *mut usize,
        ) -> i32 {
            $crate::abi::runner(engine, request, request_len, out_ptr, out_len)
        }

        /// Releases an output.
        ///
        /// # Safety
        /// `ptr` and `len` must be one output of an export, not yet
        /// released, or null.
        #[no_mangle]
        pub unsafe extern "C" fn eng_free(ptr: *mut u8, len: usize) {
            $crate::abi::free(ptr, len)
        }
    };
}
