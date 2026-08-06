# Migration boundary report

## Root-cause trace (before implementation)

1. `hand session start` and bare `hand` both reach `renderSessionOverview` and use
   `project.ListReadOnly`, `state.ListReadOnly`, and `state.ListHoldsReadOnly`.
2. Those readers call `store.OpenReadOnly`. It opens `state/hand.db` with SQLite
   `mode=ro`/`query_only`, reads `PRAGMA user_version`, and intentionally refuses a
   schema older or newer than this binary without migrating it.
3. The older-schema error currently advertises both `hand init` and `hand update`.
   It does not name the resolved fleet path, so a bare `hand init` can target the
   wrong working directory.
4. `hand init <home>` runs `initLayout` and then `initMarker`. `initMarker` calls
   `store.Open`, which runs `migrateSchema` and then `migrateLegacy`. This upgrades
   SQLite and imports legacy `state/*.json` task files, but it does not import the
   legacy project registry.
5. Project import belongs to `project.List`'s private `openRegistry` path:
   `store.Open` runs first, then `importLegacyRegistry` parses
   `data/projects.md`, inserts its rows, and writes `migrated:projects.md`. The
   init path never crosses that boundary, so the advertised remedy can leave the
   next read-only overview with no imported legacy projects.
6. `hand update` checks the latest tag before resolving a fleet home. Its
   current-version/no-op branch returns immediately, so it migrates nothing.
   The newer-version branch refreshes instructions and skeleton files after
   replacing the binary, but likewise never opens the state/project registry.

The defect is therefore the recovery contract, not the read-only opener: startup
now has an explicit refusal boundary, but no single advertised command is
guaranteed to run all three existing migrations.

## Design and implementation plan

The narrow fix is to expose the already-composed project registry opener as one
explicit migration operation: it calls `store.Open` (schema plus legacy tasks),
then the existing legacy project import, then closes the database. `hand init`
will call that operation at its existing marker step. `OpenReadOnly` will
advertise only `hand init <shell-quoted resolved-home>`; `hand update` remains a
self-update command and needs no output or no-op-path changes.

Rejected alternatives:

- Migrating inside `OpenReadOnly` would violate the startup read-only contract.
- Teaching `hand update` to migrate still leaves an unnecessary network/release
  dependency in state recovery and changes its current-version output contract.
- A new migration package or second implementation would duplicate the existing
  `store.Open`/`openRegistry` sequencing.

- [x] Add a failing old-fleet command regression that proves refusal, exact
  recovery, migrated task/project rendering, and tree equality across both
  post-recovery overviews.
- [x] Expand `OpenReadOnly` regressions for missing, older, newer, and URI-special
  database paths, with whole-tree immutability assertions.
- [x] Add the minimal explicit project migration operation and wire init to it.
- [x] Make the older-schema recovery command exact and shell-safe; remove the
  stale `hand update` remedy.
- [x] Correct README startup/migration wording and the stale agentsmd test
  diagnostic, then run focused and full verification.

## Reproduction and TDD evidence

RED command:

```text
go test ./cmd -run '^TestOldFleetRequiresExplicitRecoveryBeforeReadOnlyOverview$' -count=1 -v
```

The old-schema overview failed without changing the snapshotted fleet tree, but
its error advertised `run hand init or hand update` instead of the exact fleet
home. The test then ran `hand init <home>`: schema migration and legacy task
import succeeded (`legacy-task` rendered), while `project_count` remained zero
and `legacy-project` did not render. This proves the recovery command stops
between the store migration and `project.openRegistry` boundary.

The store RED run independently failed only the older-schema case because its
message still advertised the generic init/update pair. Missing database, newer
schema, current rows, write refusal, and URI-special fleet paths already
preserved the whole tree. After the fix all five `OpenReadOnly` cases pass.

## Implemented boundary

`project.Migrate` now gives bootstrap one explicit entrypoint over the existing
ordered work: `store.Open` upgrades schema and imports legacy task JSON, then
`importLegacyRegistry` imports `data/projects.md` under its existing migration
lock. `hand init` calls it at the former marker-open step. The older-schema
read-only error advertises only the guaranteed, POSIX-shell-quoted
`hand init <resolved-home>` command; `hand update` is unchanged.

The full old-fleet regression runs both read-only overview forms before
recovery, runs the advertised init command, renders the imported task and
project in both session and bare overviews, and compares the complete tree to
the post-recovery snapshot after each overview.

## Verification

- `go test ./cmd ./internal/store ./internal/project ./internal/state ./internal/agentsmd -count=1`
  passed; `cmd` completed in 48.748s.
- `nix develop -c make lint` passed `go vet`, `golangci-lint` with `0 issues`,
  and `tools/commentlint`.
- `nix develop -c go build ./...` exited 0.
- `nix develop -c go test -race ./...` passed every package; `cmd` completed
  in 63.042s and `internal/store` in 8.355s.
- `git diff --check` passed.
