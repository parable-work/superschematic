// The Tasks kind's decorators (D203): a Tasks service declares the work a
// commit of its repository needs, a class per task. Each decorator does
// nothing at run time; superschematic reads the arguments statically and
// writes the task file from them, and the types here check in the editor
// what the loader checks again.
import type { ServiceHandle } from "@superschematic/schema-config";

/**
 * The input kinds a task may name, each with the spec its resolver reads:
 * the core's `path` and `schema`; the four of the official `resolvers`
 * extension, which the installed binary links, `go-module`, `rust-crate`,
 * `js-package` and `image`; and each kind whose authoring package
 * augments this interface when an extension registers its resolver.
 *
 * ```ts
 * declare module "@superschematic/tasks" {
 *   interface InputKinds {
 *     "bazel-target": { readonly label: string };
 *   }
 * }
 * ```
 */
export interface InputKinds {
  /**
   * Tracked files by path or glob, from the repository root. A path that
   * names a directory covers every file under it, and `**` stands for any
   * number of directories. `exclude` leaves files out.
   */
  path: { readonly paths: readonly string[]; readonly exclude?: readonly string[] };
  /**
   * A schema service, by its handle or its name: its directory, the
   * directory of each service it names, its schemas root's shared files,
   * what the naming file says, and the naming file's `[cache] inputs`. A
   * comment in the naming file, its layout and its `[deps]` are no part
   * of the input. A handle is the service's
   * sentinel, imported from its package, so a wrong name fails in the
   * editor. `root` names the schemas root when the repository has several
   * with a service of that name.
   */
  schema: { readonly service: ServiceHandle | string; readonly root?: string };
  /**
   * A Go module, by the directory of its go.mod: its files without the
   * modules nested in it, the modules its `replace` lines point at, and
   * the modules of its go.work that it requires.
   */
  "go-module": { readonly dir: string };
  /**
   * A Rust crate, by the directory of its Cargo.toml: its files without
   * the crates nested in it, the crates its dependency tables name by a
   * path, and its workspace's manifest and lockfile. A workspace's
   * directory is every member.
   */
  "rust-crate": { readonly dir: string };
  /**
   * A JS package, by the directory of its package.json: its files, the
   * packages of its workspace it depends on, the packages it names by a
   * path, and the workspace root's manifests and lockfiles. `workspace`
   * names the directory of the workspace's root when it is not above the
   * package: a root that sits elsewhere, or one a build writes, whose
   * committed lockfile then says where its members are.
   */
  "js-package": { readonly dir: string; readonly workspace?: string };
  /**
   * An image, by its Dockerfile: the Dockerfile and each file of the
   * build context a `COPY`, an `ADD` or a bind mount names. The context
   * is the Dockerfile's directory unless `context` names one, `.` for
   * the repository root.
   */
  image: { readonly dockerfile: string; readonly context?: string };
}

/**
 * The part of each section that belongs to one task, by the section's
 * name. A section is a part of the task file that one forge owns: the core
 * checks it against the section's schema and copies it. An extension that
 * registers a section augments this interface with its type; a section the
 * naming file registers as data takes any object.
 */
export interface TaskSections {
  /**
   * What the `github` renderer reads of one task when it writes the
   * workflow (`outputs.tasks.ci.github`): the runner its job runs on in
   * place of the file's, its timeout, and the condition it runs on,
   * written as GitHub writes a job's `if`.
   */
  github?: {
    readonly runsOn?: string;
    readonly timeoutMinutes?: number;
    readonly if?: string;
  };
}

/**
 * The part of each section that belongs to the file, by the section's
 * name, as `TaskSections` is for a task.
 */
export interface FileSections {
  /**
   * What the `github` renderer reads of the file when it writes the
   * workflow: its triggers, written as GitHub writes a workflow's `on`;
   * its name, the service's unless given; its permissions, `contents:
   * read` unless given; the runner every job runs on, the hosted runner
   * of the task's platform unless given; and the repository the tasks'
   * image is pulled from by its digest, as `ghcr.io/acme/ci`.
   *
   * With `reuse` the workflow plans the commit in a job of its own and
   * keeps each task's pass, with its outputs, in GitHub's cache under the
   * task's input key: a task whose key has a pass runs nothing. It needs
   * the task file committed (`outputs.tasks.install`) and the image
   * `@tasks` names, which holds the plan command. `command` is that
   * command's first words, `["superschematic"]` unless given: the binary
   * that links the resolvers the inputs name, as the image calls it.
   */
  github?: {
    readonly on: { readonly [event: string]: unknown };
    readonly name?: string;
    readonly permissions?: { readonly [scope: string]: "read" | "write" | "none" };
    readonly runsOn?: string;
    readonly imageRepository?: string;
    readonly reuse?: { readonly command?: readonly [string, ...string[]] };
  };
}

/** One input: a path or a glob, or a kind with the spec its resolver reads. */
export type Input = string | { readonly [K in keyof InputKinds]: { readonly kind: K; readonly spec: InputKinds[K] } }[keyof InputKinds];

/** A task, named as a value: its `@task` class. */
export type TaskClass = abstract new (...args: never[]) => unknown;

/** One output of another task that this one reads. */
export type Use = { readonly task: TaskClass; readonly output: string };

/**
 * One named result of a task. An output of kind `paths`, which is what an
 * output with `paths` is, holds the files the task writes there; `image`
 * holds an image's digest and `value` a string.
 */
export type Output =
  | { readonly name: string; readonly kind?: "paths"; readonly paths: readonly string[] }
  | { readonly name: string; readonly kind: "image" | "value" };

/** One service container: its name and its image, `<repository>@sha256:<digest>`. */
export type Service = { readonly name: string; readonly image: `${string}@sha256:${string}` };

/** Each registered section's part, with any other section as an object. */
type Sections<S> = S & { readonly [section: string]: object | undefined };

/** The defaults of a schema's tasks, and each section's part of the file. */
export type TasksOptions = {
  /** The digest of the image a task runs from unless it names its own. The plan command ships in it. */
  readonly image?: `sha256:${string}`;
  /** The platform a task runs on unless it names its own, as `linux/amd64`. */
  readonly platform?: `${string}/${string}`;
  /** Each section's part that belongs to the file. */
  readonly sections?: Sections<FileSections>;
};

/** One unit of work. */
export type TaskOptions = {
  /** The task's name when it is not the class's in kebab case. */
  readonly name?: string;
  /** The command, an argument list. Nothing starts a shell: `["bash", "-c", "..."]` asks for one. */
  readonly run: readonly [string, ...string[]];
  /** What the task reads of the commit. A string is a path or a glob. */
  readonly inputs?: readonly Input[];
  /** The outputs of other tasks this one reads. Each enters this task's key by the other task's key. */
  readonly uses?: readonly Use[];
  /** The tasks that must pass before this one starts, and from which it reads nothing. It enters no key. */
  readonly after?: readonly TaskClass[];
  /** The named results the task declares. */
  readonly outputs?: readonly Output[];
  /** The service containers the task runs beside. */
  readonly services?: readonly Service[];
  /** The environment the command starts with. */
  readonly env?: { readonly [name: string]: string };
  /** The digest of the image this task runs from, in place of the file's. */
  readonly image?: `sha256:${string}`;
  /** The platform this task runs on, in place of the file's. */
  readonly platform?: `${string}/${string}`;
  /** False for a task whose result its inputs do not decide: it has no key and is never reused. */
  readonly hermetic?: boolean;
  /** Each section's part that belongs to this task. */
  readonly sections?: Sections<TaskSections>;
};

const noop: ClassDecorator = () => {};

/**
 * Declares the defaults of the schema's tasks and each section's part of
 * the file. A schema has at most one `@tasks` class.
 */
export function tasks(_options: TasksOptions): ClassDecorator {
  return noop;
}

/** Declares a task: one unit of work, a class of its own. */
export function task(_options: TaskOptions): ClassDecorator {
  return noop;
}
