# Supervisor Session Bootstrap Design

Date: 2026-08-06

## Goal

Make both supported first-use journeys converge on one reliable, harness-agnostic supervisor bootstrap:

1. An operator installs `hand`, initializes an empty fleet directory, and launches a coding harness there.
2. An operator clones Secondhand, launches a coding harness in the source checkout, and lets that supervisor initialize the checkout as a dogfood fleet home when needed.

In either journey, the supervisor loads current fleet context before answering or mutating fleet state. The startup contract must not depend on a harness-specific hook.

## Scope

This design covers the supervisor journey from initialization through the first actionable fleet overview:

- fleet-home initialization;
- durable supervisor instructions;
- source-checkout dogfood initialization;
- current-harness detection;
- worker-default resolution;
- session context assembly;
- worker-role isolation;
- first-project and active-fleet next steps;
- migration away from the Claude-only session hook;
- focused unit, command, integration, and end-to-end verification.

The audit of every later CLI lifecycle is tracked separately in [issue #171](https://github.com/atqamz/secondhand/issues/171). Supervisor-session locking, automatic watcher startup, state repair, wake queues, and lifecycle changes after spawn are outside this change.

## Findings

Bare `hand` is currently a partial session-start command. It reports configuration and fleet state, but it does not establish an explicit bootstrap contract or load all context the generated workflow requires.

The current integration is asymmetric. `hand init` installs a Claude Code `SessionStart` hook, while Codex, Pi, OpenCode, and Grok depend on instruction files or their own extension systems. Maintaining native adapters would create separate trust, configuration, output, and compatibility contracts for every harness.

The current `internal/agentsmd.Refresh` intentionally leaves an existing unmarked `AGENTS.md` untouched. In the Secondhand source checkout, `hand init` therefore reports `agents_md: unchanged` and never installs fleet instructions. This reproduced the FTUE failure that prompted this design.

Configuration is also advisory rather than enforced. The generated instructions say to settle missing worker defaults before dispatch, but `hand spawn` silently falls back to Claude when `config/harness` is absent. A supervisor can therefore miss the prose-only setup and still dispatch with a value nobody chose.

Firstmate demonstrates the useful shape of one ordered startup digest and dynamic harness detection, but its supervisor lock, mutating bootstrap sweeps, wake drain, and full context dump are not required for Secondhand's focused FTUE fix.

## Decisions

### Instruction-first integration

`AGENTS.md` is the canonical bootstrap integration. Claude continues to consume the same content through the existing `CLAUDE.md` symlink. Native session hooks are not required for correctness and the existing Secondhand-owned Claude hook is retired.

The instruction file contains a small stable command contract. Dynamic fleet state stays in command output rather than being copied into generated prose.

This is intentionally instruction-first rather than prose-only. Commands that mutate fleet state continue to validate their own prerequisites, so an agent that misses the startup instruction cannot silently dispatch with invented defaults.

### Canonical command

Add `hand session start` as the sole supervisor-session bootstrap command. It is:

- read-only;
- non-interactive;
- idempotent;
- bounded in output size;
- valid only in a fleet home;
- invalid in a process marked as a worker.

Bare `hand` remains a compatible fleet overview and delegates to the same session-start rendering path. Documentation and generated instructions name `hand session start` so the lifecycle intent is explicit.

### Managed instruction block

`hand init` owns only content between `hand:begin` and `hand:end` markers.

- If `AGENTS.md` is absent, initialization creates it with the managed block.
- If a valid marked block exists, initialization refreshes only that span.
- If an unmarked `AGENTS.md` exists, initialization appends the managed block without changing the existing bytes.
- Duplicate, malformed, missing, or stale markers are findings owned by `hand doctor`.
- Existing content outside the markers is preserved byte for byte.
- An existing `CLAUDE.md` is preserved. When it is absent, initialization creates the existing symlink to `AGENTS.md`.

The generic fleet-home block says, in substance:

```md
<!-- hand:begin -->
## Secondhand supervisor bootstrap

Before responding or acting in a supervising session, run `hand session start`.
Do not run supervisor bootstrap when `HAND_ROLE=worker`.
<!-- hand:end -->
```

The complete fleet operating contract is returned by `hand session start` or referenced through its targeted next steps. The managed block stays short enough to remain prominent in every supported harness's instruction context.

### Source-checkout dogfood path

The repository's own tracked `AGENTS.md` carries the managed supervisor block plus a source-specific rule outside the generic generated content.

At the start of a main session in the Secondhand source checkout:

1. If `HAND_ROLE=worker`, skip supervisor bootstrap.
2. If `state/hand.db` is absent, build the local binary with `make build` in the documented development environment and run `./hand init`.
3. Run `./hand session start`.

If the development toolchain is not already active, the instruction points to `nix develop` as the authoritative setup rather than embedding a second build procedure.

`hand session start` does not initialize an arbitrary directory. The source-specific instruction owns the exceptional dogfood initialization because only the source checkout can justify it.

### Worker-role boundary

Every worker launched by `hand spawn` receives:

```text
HAND_ROLE=worker
HAND_HOME=<absolute fleet home>
```

The role marker prevents a repository's copied `AGENTS.md` from initializing a worker worktree as a new fleet home. The absolute home keeps existing CLI resolution deterministic from a worker checkout.

`hand session start` refuses when `HAND_ROLE=worker`. This turns an instruction mistake into a clear precondition error rather than a second supervisor bootstrap.

### Harness detection and worker defaults

Add one current-harness detector under `internal/harness`. Detection precedence is:

1. an explicit, validated override used by controlled launches and tests;
2. the nearest recognizable process ancestor;
3. a verified harness environment marker;
4. `unknown`.

Process ancestry precedes inherited environment markers so a stale marker from a parent supervisor or terminal multiplexer cannot override the harness actually running the command. Matching is bounded and recognizes wrapper names such as `.codex-wrapped`; interpreter arguments are inspected only when the executable name is an otherwise ambiguous interpreter.

The detector returns the harness and the source of the decision. Supported names remain owned by `internal/harness`.

Worker defaults become optional overrides:

```text
effective harness = config/harness, otherwise detected supervisor harness
effective model   = configured harness-specific model, otherwise harness native default
effective effort  = configured harness-specific effort, otherwise harness native default
```

Existing configuration files retain their meaning as explicit overrides. An absent model or effort is no longer a missing FTUE answer. Harnesses already know their own native model and effort defaults, and those values cannot be detected consistently from an instruction-driven shell command.

If the current harness is `unknown` and no harness override exists, session bootstrap still renders the available read-only context. It reports one unresolved harness decision, and `hand spawn` refuses until the operator sets `config/harness` explicitly.

There is no implicit Claude fallback.

## Session-Start Data Flow

`hand session start` performs this ordered pipeline:

```text
resolve fleet home
-> reject worker role
-> detect supervisor harness
-> resolve optional worker overrides
-> read operator constraints
-> read project registry
-> summarize backlog, tasks, holds, and unacknowledged events
-> choose the highest-priority next action
-> render one TOON document
```

The output begins with an explicit completion marker:

```text
session_bootstrap: complete
```

It then reports:

- fleet home and executable identity;
- detected supervisor harness and detection source;
- effective worker harness, model, and effort, including whether each came from configuration, detection, or a harness-native default;
- the full contents of `data/operator.md`;
- a compact project registry;
- a bounded backlog identity summary;
- current task and hold summaries;
- unacknowledged worker events;
- targeted paths for any follow-up read;
- the single highest-priority next action.

The command does not bulk-print `data/learnings.md` or full status histories. The generated workflow continues to require targeted reads when a task touches those records. This keeps startup context bounded while preserving the information necessary to choose the next action.

### Next-action priority

The help block selects actions in this order:

1. Resolve an unknown harness when no explicit worker override exists.
2. Act on an unacknowledged worker event or active hold.
3. Register the first project when none exists.
4. Prepare or dispatch queued work.
5. Arm watcher supervision when workers are active.
6. Report that the fleet is configured and idle.

The command may include supporting follow-up lines, but it does not emit contradictory equal-priority actions such as suggesting spawn before any project exists.

## Error Handling

- Outside a fleet home, `hand session start` returns a precondition error with an exact `hand init` remedy.
- Under `HAND_ROLE=worker`, it returns a precondition error naming the role boundary.
- An unknown harness is reported in successful read-only output. It becomes a dispatch precondition only when no configured harness exists.
- Missing or corrupt required fleet files return an error naming the absolute path and the supported recovery command.
- Session startup never reads stdin, launches a worker, arms a watcher, acquires a supervisor lock, drains events, or repairs state.
- A failed optional summary does not hide already discovered high-priority state. Error documents follow the existing root error contract.

## Migration

`hand init` and the update path remove only the Secondhand-owned command from `.claude/settings.json`. Every unrelated matcher, hook, permission, and setting is preserved. An empty owned matcher left after removal is removed; unrelated empty structures are not normalized as a side effect.

The existing worker configuration files require no data migration. Reporting changes from mandatory missing values to effective override/native-default values.

The repository's tracked `AGENTS.md` is updated in the implementation change so dogfood initialization does not dirty a clean checkout merely by refreshing an already-current managed block.

## Verification

Focused tests cover:

- harness detection for supported executables, wrappers, bounded ancestry, interpreter arguments, conflicting stale markers, explicit overrides, and unknown processes;
- effective configured, detected, native-default, unsupported, and unknown worker settings;
- insertion into an existing unmarked `AGENTS.md` with byte-for-byte preservation outside the appended block;
- marked refresh, duplicate markers, malformed markers, missing markers, and `hand doctor` findings;
- preservation of an existing `CLAUDE.md` and creation of the symlink only when absent;
- safe removal of the owned Claude hook with unrelated settings preserved;
- worker launch environment and worker-role bootstrap refusal;
- deterministic bounded session output and every next-action priority branch;
- no stdin reads in initialization or session startup;
- an installed-CLI empty-directory FTUE;
- the source-checkout dogfood FTUE with the local binary;
- prevention of source-checkout bootstrap inside a spawned worker worktree.

Verification commands are:

```sh
make lint
go build ./...
go test -race ./...
make e2e
```

External-tool contract tests are required only if implementation changes observed `herdr`, `treehouse`, or `gh` behavior.

## Documentation

Update command help, README quick start, CONTRIBUTING dogfood guidance, the generated fleet-home instructions, and the authoritative focused tests beside each behavior.

Add a narrow ADR recording why Secondhand chose one managed instruction contract over per-harness native session adapters. The ADR owns only that durable architectural decision; behavioral details remain beside implementation, help, and focused tests.

## Research Basis

- [Firstmate session start](https://github.com/kunchenguid/firstmate/blob/main/bin/fm-session-start.sh) demonstrates one ordered digest and makes its locking and mutation boundaries explicit.
- [Firstmate harness detection](https://github.com/kunchenguid/firstmate/blob/main/bin/fm-harness.sh) documents verified environment markers and bounded ancestry matching.
- [Claude Code hooks](https://code.claude.com/docs/en/hooks) provide a native `SessionStart` event but recommend static instructions for static context.
- [Codex AGENTS.md](https://learn.chatgpt.com/docs/agent-configuration/agents-md) documents per-session instruction discovery and composition.
- [Codex hooks](https://learn.chatgpt.com/docs/hooks) provide `SessionStart`, including resume, clear, and compaction sources, but require project-hook trust.
- [Pi extensions](https://github.com/badlogic/pi-mono/blob/main/packages/coding-agent/docs/extensions.md) provide a `session_start` event and project-local extensions.
- [OpenCode plugins](https://opencode.ai/docs/plugins/) provide startup-loaded project plugins and session events.
- [OpenCode rules](https://opencode.ai/docs/rules/) load project `AGENTS.md` directly.
- [Grok CLI](https://github.com/superagent-ai/grok-cli#hooks) supports `SessionStart` and `AGENTS.md`, while its current hook loader intentionally accepts user settings only.
