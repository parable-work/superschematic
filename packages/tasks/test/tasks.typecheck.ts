// Type-level checks of @superschematic/tasks (D203). Nothing imports this
// file: `bun run typecheck`, which `make ts` runs, checks it, and fails on
// an @ts-expect-error that no longer errors.
import { service } from "@superschematic/schema-config";

import { task, tasks } from "../src/index";

const ShopApi = service({ name: "shop-api", kind: "API" });

declare module "../src/index" {
  interface InputKinds {
    "bazel-target": { readonly label: string };
  }
  interface TaskSections {
    forge?: { readonly tier: "pr" | "nightly"; readonly timeoutSeconds?: number };
  }
  interface FileSections {
    forge?: { readonly tiers: readonly string[] };
  }
}

@tasks({
  image: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
  platform: "linux/amd64",
  sections: {
    forge: { tiers: ["pr", "nightly"] },
    other: { anything: true },
    github: { on: { pull_request: { branches: ["main"] } }, imageRepository: "ghcr.io/acme/ci", permissions: { contents: "read" } }
  }
})
export abstract class Repo {}

@task({
  run: ["superschematic", "build-all", "schemas/services"],
  inputs: [
    { kind: "schema", spec: { service: ShopApi } },
    { kind: "schema", spec: { service: "shop-db", root: "schemas" } },
    { kind: "rust-crate", spec: { dir: "crates/shop" } },
    { kind: "js-package", spec: { dir: "web/shop" } },
    { kind: "image", spec: { dockerfile: "docker/api.Dockerfile", context: "." } },
    { kind: "bazel-target", spec: { label: "//shop:api" } }
  ],
  outputs: [{ name: "generated", paths: ["schemas/dist"] }]
})
export abstract class SchemaBuild {}

@task({ run: ["golangci-lint", "run", "./..."], inputs: ["go", { kind: "path", spec: { paths: ["go.mod"], exclude: ["**/*.md"] } }] })
export abstract class Lint {}

@task({
  name: "go-test",
  run: ["go", "test", "./..."],
  inputs: ["go", { kind: "go-module", spec: { dir: "go/shop-api" } }],
  uses: [{ task: SchemaBuild, output: "generated" }],
  after: [Lint],
  outputs: [{ name: "image", kind: "image" }],
  services: [{ name: "postgres", image: "postgres@sha256:1111111111111111111111111111111111111111111111111111111111111111" }],
  env: { CGO_ENABLED: "1" },
  hermetic: false,
  sections: { forge: { tier: "pr", timeoutSeconds: 600 }, github: { runsOn: "ubuntu-24.04", timeoutMinutes: 20, if: "github.event_name == 'push'" } }
})
export abstract class GoTest {}

// @ts-expect-error the github section's part of the file names the workflow's triggers
@tasks({ sections: { github: { imageRepository: "ghcr.io/acme/ci" } } })
export abstract class NoTriggers {}

// @ts-expect-error a job's timeout is a number of minutes
@task({ run: ["true"], sections: { github: { timeoutMinutes: "20" } } })
export abstract class BadTimeout {}

// @ts-expect-error a command is an argument list with at least one argument
@task({ run: [] })
export abstract class NoCommand {}

// @ts-expect-error a command is an argument list, not a shell line
@task({ run: "go test ./..." })
export abstract class ShellLine {}

// @ts-expect-error an input's spec is its kind's
@task({ run: ["true"], inputs: [{ kind: "schema", spec: { paths: ["go"] } }] })
export abstract class WrongSpec {}

// @ts-expect-error an input names a registered kind
@task({ run: ["true"], inputs: [{ kind: "cargo", spec: {} }] })
export abstract class UnknownKind {}

// @ts-expect-error a unit is named by its directory
@task({ run: ["true"], inputs: [{ kind: "rust-crate", spec: { manifest: "Cargo.toml" } }] })
export abstract class CrateByManifest {}

// @ts-expect-error an edge names a task's class, not its name
@task({ run: ["true"], after: ["lint"] })
export abstract class NamedByString {}

// @ts-expect-error an image is a digest, not a tag
@task({ run: ["true"], image: "ubuntu:24.04" })
export abstract class Tagged {}

// @ts-expect-error a service container's image holds its digest
@task({ run: ["true"], services: [{ name: "postgres", image: "postgres:16" }] })
export abstract class TaggedService {}

// @ts-expect-error an output of kind image names no paths
@task({ run: ["true"], outputs: [{ name: "image", kind: "image", paths: ["out"] }] })
export abstract class ImageWithPaths {}

// @ts-expect-error a registered section's part is its type
@task({ run: ["true"], sections: { forge: { tier: "weekly" } } })
export abstract class WrongTier {}
