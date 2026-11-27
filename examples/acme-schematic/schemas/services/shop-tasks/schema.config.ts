import { defineConfig, SchemaKind } from "@acme/schema-config";

// A Tasks schema (D203): the work a commit of this example needs. Its build
// writes the task file, which build-all installs at the path below, from
// the repository root, where it is committed. `acme-schematic tasks --check`
// fails when the committed file is not what the schema gives.
export default defineConfig({
  name: "shop-tasks",
  kind: SchemaKind.Tasks,
  outputs: {
    tasks: { install: "examples/acme-schematic/ci/tasks.json" }
  }
});
