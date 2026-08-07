# Secondhand

`hand` is a CLI that manages a fleet of coding agents from a fleet home (`data/`, `state/`, this file).
This checkout is the tool's own source, not a fleet home itself - there is no `state/hand.db` here, so `internal/home.IsHome` reports false.
`internal/agentsmd`'s `generatedBody` is the authoritative fleet-home template; its tests own refresh and `hand doctor` behavior, and `hand --help` is the command reference.

## Rules

- Behavioral contracts belong beside their implementation, command help, and focused tests. `docs/adr/README.md` owns the narrow bar for durable architectural rationale.
- Comments obey two rules `make lint` enforces through `tools/commentlint`: a comment may not open with the identifier it documents, and a comment block may not exceed three lines. CONTRIBUTING.md's "Comments" section owns the bar, exemptions, and reasoning.
- Command output goes through `internal/axi` as TOON and every failure through `cmd/root.go`'s error document; `hand watch`'s event stream is the exception. Package and command tests own these shapes.
- Harness/herdr syntax, exit enforcement, watch's stdout/errOut split, and first-run prompt handling are owned by their implementations and closest tests under `internal/harness`, `internal/herdr`, `internal/watcher`, and `cmd`.
- `herdr`, `treehouse` and `gh` are faked once in `internal/faketool` for every suite. `internal/faketool/FIDELITY.md` records observed external behavior and `tests/contract` (`make contract`) rechecks it. Extend the shared fake, never hand-write another.
- Test, release, and write conventions live as doc comments: `tests/e2e` (`fakes_test.go`, `e2e_test.go`), GitHub access via `gh` (`internal/ghutil`), AGENTS.md refresh (`internal/agentsmd`), atomic writes (`internal/atomicfile`).
- Dev environment is Nix-based (`flake.nix`, `CONTRIBUTING.md`); `make lint`, `go build ./...`, and `go test -race ./...` verify inside `nix develop`.

## Maintaining this file

Keep knowledge useful to almost every future agent session. Point to authoritative code, tests, or commands instead of repeating them, and prefer rewriting or pruning existing entries over appending new ones.

## Supervising from this checkout

In a main session, when `HAND_ROLE` is not `worker`, bootstrap before responding or acting.
If `state/hand.db` is absent, run `make build` in the documented Nix development environment and then `./hand init`.
Run `./hand session start` after initialization and at the start of every later supervising session.

<!-- hand:generated:start -->
## Secondhand supervisor bootstrap

Before responding or acting in a supervising session, run `hand session start`.
Do not run supervisor bootstrap when `HAND_ROLE=worker`.
<!-- hand:generated:end -->
