import { defineConfig, SchemaKind } from "@acme/schema-config";

// A Tasks schema (D203): the work a commit of this example needs. Its build
// writes the task file, which build-all installs at the path below, from
// the repository root, where it is committed. `acme-schematic tasks --check`
// fails when the committed file is not what the schema gives.
//
// `ci.github` writes a GitHub Actions workflow that runs each task as a
// job, from the same file. Its directory is the example's own and not
// `.github/workflows`, so GitHub does not run it: it is here to be read.
export default defineConfig({
  name: "shop-tasks",
  kind: SchemaKind.Tasks,
  outputs: {
    tasks: {
      install: "examples/acme-schematic/ci/tasks.json",
      ci: { github: { install: "examples/acme-schematic/ci/workflows" } }
    }
  }
});
