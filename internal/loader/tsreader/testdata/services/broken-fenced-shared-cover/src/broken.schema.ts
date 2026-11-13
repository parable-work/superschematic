import { Generic, Identity } from "superscalar";
import { Default, fenced } from "@superschematic/schema";
import { AutoGenerate, key } from "@superschematic/db";

// The compiler takes a field in two fences' covers. The build refuses it:
// a write of priority would have two counters to advance, and each
// party's write would fail the other's compare.
@fenced<Job>({ field: "version", covers: ["label", "priority"] })
@fenced<Job>({ field: "runVersion", covers: ["progress", "priority"] })
export abstract class Job {
  @key
  id: AutoGenerate<Identity.UUID>;

  label: string;

  priority: Default<Generic.Int64, 0>;

  version: Default<Generic.Int64, 0>;

  progress: Default<Generic.Int64, 0>;

  runVersion: Default<Generic.Int64, 0>;
}
