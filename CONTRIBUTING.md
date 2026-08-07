# Contributing

## Getting started

git clone https://github.com/atqamz/secondhand
cd secondhand
nix develop
make build
make test

`nix develop` is optional and provides the full toolchain: Go, golangci-lint, gopls, gotools, and gcc (gcc is required because `make test` runs with `-race`, which needs CGO).
Without Nix, install those yourself.

To dogfood the tool from its own checkout, enter `nix develop`, run `make build` whenever `./hand` is absent or stale, and launch any supported harness from the checkout root.
The tracked `AGENTS.md` tells a main session to initialize the checkout when `state/hand.db` is absent and to run `./hand session start` before supervising.

Every directory `hand init` creates at the checkout root is gitignored, so the fleet home lives alongside the source without ever being committed.
The tracked managed block is already current, so initialization preserves the source-owned instructions and leaves a clean checkout unchanged.

## Making changes

1. Open an issue describing the intent, design, or proposal, and get agreement there before writing code. This applies to any contribution, no matter the size. See "Reporting issues" below for what to include.
2. Fork and branch from main.
3. Make changes.
4. make lint && make test
5. make e2e if you changed CLI behavior (end-to-end suite, excluded from make test).
6. make contract if you changed how hand calls herdr, treehouse or gh, and you have those installed. It checks the records in internal/faketool/FIDELITY.md against the real tools, skipping whichever is absent, so CI never runs it.
7. nix build .#default if you changed Go dependencies (CI builds the flake, and a stale vendorHash in flake.nix fails it).
8. Open a PR whose body carries a closing keyword (Closes, Fixes, or Resolves) directly preceding a fully qualified atqamz/secondhand#N, on its own line. A bare #N links but reads ambiguously outside the repo, and a reference without the keyword links the issue without ever closing it.

Commits use conventional commits: feat:, fix:, chore:, etc.
release-please handles versioning and changelogs from these.

## Comments

The default is no comment.
Add one only for a why the code cannot show: a hidden constraint, a subtle invariant, a workaround for a specific bug.
Restating code, narrating what, banners, and doc comments on the obvious are noise no linter can catch, so they stay a reviewer's call.

Two rules bound the comments that clear that bar, enforced by `make lint` rather than by a reviewer reading a diff:

1. A comment may not open with the identifier it documents.
2. A comment block may not exceed three lines.

Consecutive `//` lines are one block, and neither a bare `//` line inside a run nor a blank line above a doc comment breaks it.
Both are ways of writing six lines of prose in front of one declaration while satisfying a three-line rule, and the blank-line form also drops the first half out of godoc.
Rule 1 applies wherever Go's doc convention does not: unexported declarations, everything in `_test.go`, and comments inside function bodies.
An exported declaration's doc comment is required by convention to open with its name, so it is exempt from rule 1, but not from rule 2.
Exempt from both rules: the package doc comment, directives (`//go:build`, `//go:generate`, `//nolint`, `// #nosec`), and files carrying the generated-code header.

Rule 2 will occasionally be wrong, because a genuinely subtle invariant sometimes needs a fourth line.
That is accepted: a rule that is right most of the time and mechanically enforced binds harder than one that is right always and enforced never.
Behavior a caller depends on belongs with its implementation, command help, and focused tests.
User or contributor guidance belongs in README.md, AGENTS.md, or this file.
`docs/adr/README.md` owns the narrower bar for durable architectural rationale.

`go run ./tools/commentlint .` runs the check alone and prints one `file:line:column` per violation.

## Reporting issues

Open a GitHub issue with repro steps, OS, arch, and hand --version.
