# Supervisor bootstrap is an AGENTS.md contract

- Date: 2026-08-07
- Status: accepted
- Issues: atqamz/secondhand#172
- PRs: none

## Context

A supervisor must load current fleet context before it acts, regardless of which supported harness the operator launches. A Claude-only session hook made that guarantee asymmetric, and separate native integrations would give every harness its own trust boundary, configuration surface, and compatibility schedule.

The source checkout also serves as a dogfood fleet home. Its project-owned instructions must survive fleet initialization, while the generated bootstrap must already be current so initialization does not dirty a clean clone.

## Decision

`AGENTS.md` is the canonical supervisor-bootstrap integration. `hand` owns one marked block containing the stable instruction to run `hand session start`; content outside that block remains project or operator owned. Claude consumes the same contract through the existing `CLAUDE.md` symlink when that name is otherwise absent.

Dynamic context stays in the command's bounded output rather than the instruction file. Mutating commands enforce their own prerequisites at command level, including refusing dispatch with an unknown harness before acquiring a worktree or herdr workspace.

Worker launches carry an explicit worker role and fleet-home path. `hand session start` refuses under that role, so a worker cannot initialize or act as a second supervisor from its isolated worktree.

The command help and focused tests own the behavioral details of initialization, session output, prerequisite errors, and worker isolation.

## Rejected alternatives

- Per-harness hooks, plugins, or adapters duplicate lifecycle and output contracts and require users to trust a different integration for each harness.
- A Claude hook plus best-effort instructions for other harnesses makes one implementation an implicit fallback and lets their startup behavior drift.
- Putting current fleet state in `AGENTS.md` makes durable instructions stale and turns initialization into a source-file mutation.
- Treating the instructions as the only guard lets a harness that misses them mutate external state before discovering a missing prerequisite.

## Consequences

The integration depends on each harness honoring repository instructions, and instruction-loading behavior can drift between harness versions. One visible, reviewable block limits that trust surface; `hand init`, `hand update`, and `hand doctor` expose managed-block drift without owning surrounding prose.

Operators maintain one startup contract instead of per-harness adapters. Existing `CLAUDE.md` files are never replaced. Workers remain isolated even when their worktrees contain the same source instructions, and command-level checks fail closed when an agent misses or misorders bootstrap guidance.
