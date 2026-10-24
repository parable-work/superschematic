# The superschematic agent skill

The skill a coding agent reads to build software **with** superschematic:
it declares types, tables, routes, permissions, jobs, queues and stacks
in schema files, and lets the compiler generate the SQL, migrations,
validation, servers, OpenAPI, SDKs and deploys it would otherwise write
by hand (D76).

```text
.claude-plugin/marketplace.json       the marketplace, at the repository root: one plugin, superschematic
agent/
  .claude-plugin/plugin.json          the plugin
  skills/superschematic/SKILL.md      the skill: what to declare, the loop, the rules
```

`SKILL.md` follows the [Agent Skills](https://agentskills.io/specification)
format, so any agent that reads skills can use it. It lives outside
`.claude/`, so an agent working on superschematic itself does not load it.

## Install

In Claude Code:

```text
/plugin marketplace add parable-work/superschematic
/plugin install superschematic@superschematic
```

For another agent, or without the marketplace, copy
`agent/skills/superschematic/` into the agent's skills directory:
`.claude/skills/` in a project, or `~/.claude/skills/` for every project.

The skill needs a `superschematic` binary on the `PATH` that has the
`catalog` and `check` commands.

## Why the skill holds no reference tables

Everything the compiler can say about itself, it prints:

```sh
superschematic catalog capabilities   # need -> declaration -> what is generated -> what you write
superschematic catalog scalars        # every scalar, its checks, its SQL and language types
superschematic catalog decorators     # every decorator and wrapper, with its TypeScript declaration
superschematic catalog behaviors      # every engine behavior, its config, what it adds
```

Each is written from the binary's own registry, the extensions it links
included, so it always matches the compiler the agent builds with. A copy
kept in the skill would drift, and would not know a project's extensions.
`SKILL.md` keeps only what no command prints: what to declare instead of
write, the order of work, the rules that cost a round-trip, and when to
stop and ask the user.

## Changing the skill

- Don't restate a scalar, decorator, flag or default the binary prints.
  Point at the command instead.
- A change to a command the skill names (`init`, `new service`, `check`,
  `build`, `catalog`, `stack`, `migrate plan`) updates `SKILL.md` in the
  same pull request.
- Keep `SKILL.md` short: it loads whenever the skill triggers.
- `name` must stay `superschematic`, matching its directory. Keep
  `description` under 1024 characters: it decides when the skill
  triggers.
- Bump `version` in `agent/.claude-plugin/plugin.json` and the
  marketplace entry together.
- Check a change by giving an agent a task, such as "add a paginated,
  searchable product list with a permission", with and without the change,
  and comparing how much code it writes by hand.
