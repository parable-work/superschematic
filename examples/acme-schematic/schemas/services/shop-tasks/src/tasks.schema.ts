import { task, tasks } from "@superschematic/tasks";

// The defaults of every task: the image a task runs from, by its digest,
// and its platform. The plan command ships in that image. The acme section
// (ext/tasks.go) holds what acme's forge reads and the core does not: the
// tiers, each with its budget.
@tasks({
  image: "sha256:6a1f0c5d2b7e4f8093a1c6d5e4b3a29187f6e5d4c3b2a1908f7e6d5c4b3a2918",
  platform: "linux/amd64",
  sections: {
    acme: {
      tiers: [
        { name: "pr", budgetSeconds: 180 },
        { name: "nightly", budgetSeconds: 3600 }
      ]
    }
  }
})
export abstract class Shop {}


// Builds every schema service. It reads the binary's sources and four
// services, each a schema input: the service's directory, the directory
// of each service it names (shop-api and shop-storefront name shop-db),
// and the schemas root's shared files. Its output is the generated tree,
// which ApiBuild reads.
@task({
  run: ["acme-schematic", "build-all", "examples/acme-schematic/schemas/services"],
  inputs: [
    { kind: "schema", spec: { service: "shop-api", root: "examples/acme-schematic/schemas" } },
    { kind: "schema", spec: { service: "shop-storefront", root: "examples/acme-schematic/schemas" } },
    { kind: "schema", spec: { service: "shop-catalog", root: "examples/acme-schematic/schemas" } },
    { kind: "schema", spec: { service: "shop-config", root: "examples/acme-schematic/schemas" } },
    "examples/acme-schematic/ext",
    "examples/acme-schematic/cmd",
    "examples/acme-schematic/go.mod",
    "examples/acme-schematic/go.sum"
  ],
  outputs: [{ name: "generated", paths: ["examples/acme-schematic/schemas/dist"] }],
  sections: { acme: { tier: "pr" } }
})
export abstract class SchemaBuild {}

// Fails when the committed task file is not what this schema gives.
@task({
  run: ["acme-schematic", "tasks", "examples/acme-schematic/schemas/services/shop-tasks", "--check"],
  inputs: [
    { kind: "schema", spec: { service: "shop-tasks", root: "examples/acme-schematic/schemas" } },
    "examples/acme-schematic/ci/tasks.json"
  ],
  sections: { acme: { tier: "pr" } }
})
export abstract class TaskFile {}

// Vets the extension. It reads nothing a task writes.
@task({
  run: ["go", "vet", "./..."],
  inputs: ["examples/acme-schematic/ext", "examples/acme-schematic/cmd", "examples/acme-schematic/go.mod", "examples/acme-schematic/go.sum"],
  sections: { acme: { tier: "pr" } }
})
export abstract class Vet {}

// Compiles the generated API against the acme auth provider. It reads the
// generated tree, so it uses SchemaBuild's output and its key covers
// SchemaBuild's. It starts only once Vet passed, and reads nothing of it:
// an edit that changes Vet's key alone leaves this task's key as it was.
@task({
  run: ["go", "build", "./..."],
  inputs: ["examples/acme-schematic/go.mod", "examples/acme-schematic/go.sum"],
  uses: [{ task: SchemaBuild, output: "generated" }],
  after: [Vet],
  sections: { acme: { tier: "pr", timeoutSeconds: 120 } }
})
export abstract class ApiBuild {}

// The example's end-to-end script. It reads the network and the clock, so
// its inputs do not decide its result: it has no key and runs every time.
@task({
  run: ["examples/acme-schematic/scripts/smoke.sh"],
  after: [ApiBuild, TaskFile],
  hermetic: false,
  sections: { acme: { tier: "nightly" } }
})
export abstract class Smoke {}
