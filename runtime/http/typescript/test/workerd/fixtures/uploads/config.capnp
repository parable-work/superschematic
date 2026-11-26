# The uploads fixture (D154): one Worker, records-api, with a bucket on an
# r2_bucket binding, here a stand-in (standins.js, the calls fixture's), and
# the edge's R2 credential, as the cloudflare target gives a Worker that
# lists a Bucket service. Its test handler signs an attachment's upload URL
# as orm.attachments.begin does, sends the PUT a browser sends to R2's S3
# API, which a stand-in in the Worker checks against the URL's V4
# signature, as R2 does, before it stores the bytes through the binding,
# then reads them back through the binding and hashes them with workerd's
# native DigestStream, as orm.attachments.complete does.
using Workerd = import "/workerd/workerd.capnp";

const config :Workerd.Config = (
  services = [
    (name = "uploads-stack-staging-records-api", worker = .api),
  ],
  extensions = [(
    modules = [(name = "superschematic-fixture:uploads-standins", esModule = embed "standins.js", internal = true)],
  )],
);

const api :Workerd.Worker = (
  modules = [
    (name = "worker.js", esModule = embed "worker.js"),
    (name = "superscalar_wasm_bg.wasm", wasm = embed "superscalar_wasm_bg.wasm"),
  ],
  bindings = [
    (name = "LOG_LEVEL", text = "info"),
    (name = "RECORDS_FILES_BUCKET", wrapped = (moduleName = "superschematic-fixture:uploads-standins", entrypoint = "r2")),
    (name = "RECORDS_FILES_BUCKET_BINDING", text = "RECORDS_FILES_BUCKET"),
    (name = "RECORDS_FILES_BUCKET_NAME", text = "uploads-stack-staging-records-files"),
    (name = "RECORDS_FILES_BUCKET_R2_ACCESS_KEY_ID", text = "8f2b4d6e0a1c3e5f7b9d1f3a5c7e9b0d"),
    (name = "RECORDS_FILES_BUCKET_R2_ACCOUNT", text = "0123456789abcdef0123456789abcdef"),
    (name = "RECORDS_FILES_BUCKET_R2_TOKEN", text = "r2-token-value"),
  ],
  compatibilityDate = "2026-09-15",
);
