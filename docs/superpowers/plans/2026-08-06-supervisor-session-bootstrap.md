# Supervisor Session Bootstrap Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every supported supervisor harness initialize and load a Secondhand fleet through one instruction-driven `hand session start` contract without an implicit Claude fallback.

**Architecture:** A detector in `internal/harness` identifies the current supervisor, while optional persisted values remain worker overrides. A small managed `AGENTS.md` block invokes a read-only session command that composes operator context, effective defaults, projects, backlog, and fleet state into one bounded TOON document. Worker launches carry an explicit role and fleet home, and the obsolete Claude-only hook is removed only after the portable path is installed.

**Tech Stack:** Go, Cobra, SQLite-backed existing state APIs, TOON through `internal/axi`, shell process inspection through `ps`, Go unit/integration/e2e tests, Nix development environment.

## Global Constraints

- Track the feature in [atqamz/secondhand#172](https://github.com/atqamz/secondhand/issues/172); the broader lifecycle audit remains [#171](https://github.com/atqamz/secondhand/issues/171).
- Add no third-party dependency.
- Keep `hand session start` read-only, non-interactive, idempotent, and bounded.
- Preserve every byte outside Secondhand's existing `<!-- hand:generated:start -->` and `<!-- hand:generated:end -->` markers.
- Preserve existing worker configuration files as explicit overrides; never fall back implicitly to Claude.
- Use `internal/axi` for command output and the root error document for failures.
- Do not add a supervisor lock, watcher auto-start, wake queue, or state repair.
- Comments must satisfy `tools/commentlint`: no documented identifier at the start and no comment block over three lines.
- Run focused tests after every red/green cycle and commit each completed task with a conventional commit.

---

## File Structure

- Create `internal/harness/detect.go`: current-harness detection and its production process-ancestry reader.
- Create `internal/harness/detect_test.go`: pure detection precedence and parsing tests.
- Modify `cmd/config.go`: optional-override semantics and effective worker configuration.
- Modify `cmd/config_test.go`: detected/native-default/unknown configuration contracts.
- Modify `cmd/spawn.go`: detected harness resolution and removal of the Claude fallback.
- Modify `cmd/spawn_test.go`: detected, configured, and unknown dispatch behavior.
- Modify `internal/harness/harness.go`: worker role and fleet-home launch environment.
- Modify `internal/harness/harness_test.go`: exact launch-prefix coverage.
- Modify `internal/agentsmd/agentsmd.go`: compact managed bootstrap, supervisor instruction list, safe append, and marker validation.
- Modify `internal/agentsmd/agentsmd_test.go`: append/refresh/doctor and instruction coverage.
- Modify `cmd/doctor_test.go`: malformed and duplicate managed-block reporting.
- Create `cmd/session.go`: `hand session start`, context readers, bounded backlog summary, and next-action selection.
- Create `cmd/session_test.go`: output, role, error, and priority tests.
- Modify `cmd/root.go` and `cmd/root_test.go`: register `session` and share the in-home overview path with bare `hand`.
- Modify `cmd/status.go` and focused tests: separate fleet-state rendering from fleet-specific help so session startup can own one priority order.
- Modify `internal/sessionhook/sessionhook.go` and tests: remove only the owned Claude hook.
- Modify `cmd/init.go`, `cmd/init_test.go`, `cmd/update.go`, and update tests: call hook retirement and report its outcome.
- Modify `AGENTS.md`: add source-checkout dogfood bootstrap plus the current managed block.
- Modify `README.md` and `CONTRIBUTING.md`: document both FTUE journeys.
- Create `docs/adr/supervisor-bootstrap-is-an-agents-md-contract.md`: record the durable instruction-first decision.
- Modify `tests/e2e/init_config_test.go` and helpers: exercise both FTUE paths and worker isolation.

---

### Task 1: Detect the Current Harness

**Files:**
- Create: `internal/harness/detect.go`
- Create: `internal/harness/detect_test.go`

**Interfaces:**
- Produces: `type Detection struct { Name string; Source string }`
- Produces: `func DetectCurrent() (Detection, error)`
- Consumes later: `cmd/config.go` resolves an absent worker override from `Detection.Name`; `cmd/session.go` renders both fields.

- [ ] **Step 1: Write failing precedence and matching tests**

Cover the explicit override, nearest process, interpreter arguments, stale marker conflict, marker-only fallback, bounded ancestry, and unknown result:

```go
func TestDetectPrefersNearestProcessOverInheritedMarker(t *testing.T) {
	got, err := detectCurrent("", []processInfo{
		{name: ".codex-wrapped", args: "codex"},
		{name: "bash", args: "bash"},
	}, map[string]string{"CLAUDECODE": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if got != (Detection{Name: Codex, Source: "process"}) {
		t.Fatalf("got %#v, want codex from process", got)
	}
}

func TestDetectUsesVerifiedMarkerWhenProcessIsUnknown(t *testing.T) {
	got, err := detectCurrent("", []processInfo{{name: "bash"}}, map[string]string{"PI_CODING_AGENT": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if got != (Detection{Name: Pi, Source: "environment"}) {
		t.Fatalf("got %#v, want pi from environment", got)
	}
}

func TestDetectCanBeForcedUnknown(t *testing.T) {
	got, err := detectCurrent("unknown", []processInfo{{name: "claude"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != (Detection{Name: "", Source: "override"}) {
		t.Fatalf("got %#v, want explicit unknown", got)
	}
}
```

Use table cases for `claude`, `.codex-wrapped`, `opencode`, `grok`, exact `pi`, `pi-signed`, and `node /path/to/opencode`.

- [ ] **Step 2: Run the detector tests and verify red**

Run: `go test ./internal/harness -run 'TestDetect' -count=1`

Expected: FAIL because `Detection`, `processInfo`, and `detectCurrent` do not exist.

- [ ] **Step 3: Implement the pure detector and bounded production ancestry reader**

Use these exact contracts and precedence:

```go
const maxAncestorDepth = 8

type Detection struct {
	Name   string
	Source string
}

type processInfo struct {
	name string
	args string
}

func DetectCurrent() (Detection, error) {
	override := os.Getenv("HAND_HARNESS")
	if override == "unknown" {
		return Detection{Source: "override"}, nil
	}
	ancestors := currentProcessAncestry(os.Getpid(), maxAncestorDepth)
	env := map[string]string{
		"CLAUDECODE":       os.Getenv("CLAUDECODE"),
		"CODEX_THREAD_ID":  os.Getenv("CODEX_THREAD_ID"),
		"PI_CODING_AGENT":  os.Getenv("PI_CODING_AGENT"),
		"GROK_AGENT":       os.Getenv("GROK_AGENT"),
	}
	return detectCurrent(override, ancestors, env)
}
```

`detectCurrent` validates a non-empty override with `IsSupported`, scans ancestors from nearest to farthest, checks verified markers only after ancestry, and returns `Detection{Source: "unknown"}` when nothing matches. `currentProcessAncestry` invokes `ps -o ppid=,comm=,args= -p <pid>` at most eight times; a failed lookup returns the partial ancestry so marker fallback still works. Inspect arguments only for `node`, `python`, and `python3` ancestors.

- [ ] **Step 4: Run focused and package tests**

Run: `go test ./internal/harness -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/harness/detect.go internal/harness/detect_test.go
git commit -m "feat(harness): detect current supervisor"
```

---

### Task 2: Resolve Optional Worker Overrides

**Files:**
- Modify: `cmd/config.go`
- Modify: `cmd/config_test.go`
- Modify: `cmd/spawn.go`
- Modify: `cmd/spawn_test.go`
- Modify: `cmd/root_test.go`

**Interfaces:**
- Consumes: `harness.DetectCurrent() (harness.Detection, error)`
- Produces: `func currentWorkerConfig(home string) (workerConfig, error)`
- Produces: `func readWorkerConfig(home string, detected harness.Detection) workerConfig`
- Produces: `workerConfig.harness` as the effective dispatch harness and `workerConfig.detection` as supervisor identity.
- Consumed later: `cmd/session.go` renders effective settings and selects an unknown-harness action.

- [ ] **Step 1: Replace mandatory-default tests with detected/native-default tests**

Set `HAND_HARNESS` explicitly in command tests so they never depend on the test runner's parent process:

```go
func TestConfigUsesDetectedHarnessAndNativeTierDefaults(t *testing.T) {
	home := setupConfigHome(t)
	t.Setenv("HAND_HARNESS", harness.Codex)

	cfg, err := currentWorkerConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	assertSetting(t, cfg, settingHarness, stateDetected, harness.Codex)
	assertSetting(t, cfg, settingModel, stateNativeDefault, "")
	assertSetting(t, cfg, settingEffort, stateNativeDefault, "")
}

func TestConfigReportsOneMissingDecisionWhenDetectionIsUnknown(t *testing.T) {
	home := setupConfigHome(t)
	t.Setenv("HAND_HARNESS", "unknown")
	cfg, err := currentWorkerConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.harness != "" || configMissing(cfg) != 1 {
		t.Fatalf("cfg = %#v, want only harness missing", cfg)
	}
}
```

Update applicability expectations: supported but unset model/effort is `native-default`; unsupported remains `unsupported`; only an unknown effective harness is `pending-harness` for those two rows.

Add spawn tests proving `HAND_HARNESS=codex` selects Codex without `config/harness`, an explicit configured harness wins, and `HAND_HARNESS=unknown` fails with exit 3 before worktree acquisition.

- [ ] **Step 2: Run focused tests and verify red**

Run: `go test ./cmd -run 'Test(Config|Spawn|BareInvocation)' -count=1`

Expected: FAIL on the old missing/pending states and Claude fallback.

- [ ] **Step 3: Implement effective settings**

Add states and helpers:

```go
const (
	stateConfigured    = "configured"
	stateDetected      = "detected"
	stateNativeDefault = "native-default"
	stateMissing       = "missing"
	stateUnsupported   = "unsupported"
	statePendingHarness = "pending-harness"
)

type workerConfig struct {
	detection harness.Detection
	harness   string
	settings  []workerSetting
}

func currentWorkerConfig(fleetHome string) (workerConfig, error) {
	detected, err := harness.DetectCurrent()
	if err != nil {
		return workerConfig{}, err
	}
	return readWorkerConfig(fleetHome, detected), nil
}
```

`readWorkerConfig` reads `config/harness` first. A configured value gets `configured`; otherwise a supported detected value gets `detected`; otherwise the harness row is `missing`. For applicable model/effort, a keyed file gets `configured` and absence gets `native-default`. Preserve `unsupported` and `pending-harness` distinctions.

Change `workerConfigHelp` so only an unknown harness asks the operator to run `hand config set harness <name>`. Update `hand config` copy from “missing defaults” to “effective worker defaults and optional overrides.”

- [ ] **Step 4: Remove the spawn fallback and keep explicit precedence**

Resolve in this order inside `newSpawnCmd`:

```go
harnessFromFlag := harnessName != ""
if !harnessFromFlag {
	cfg, err := currentWorkerConfig(home)
	if err != nil {
		return err
	}
	harnessName = cfg.harness
	if harnessName == "" {
		return &ExitError{Err: fmt.Errorf("current supervisor harness is unknown and no worker harness override is configured; run hand config set harness <name>"), Code: 3}
	}
}
```

Change the flag help to `agent harness to launch (default: config/harness, then the detected supervisor harness)`.

Pass the effective harness into `writeWorkerSetting` so `hand config set model/effort` can key an override to a detected harness. If neither a configured nor detected harness exists, retain exit 3. Do not migrate an old unkeyed model/effort to Claude when `config/harness` is absent; leave the ambiguous legacy file untouched and add a regression test replacing `TestMigrateWorkerSettingsKeysToClaudeWhenNoHarnessIsConfigured`.

- [ ] **Step 5: Run focused and full command tests**

Run: `go test ./cmd -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/config.go cmd/config_test.go cmd/spawn.go cmd/spawn_test.go cmd/root_test.go
git commit -m "feat(config): inherit detected harness"
```

---

### Task 3: Mark Worker Launches Explicitly

**Files:**
- Modify: `internal/harness/harness.go`
- Modify: `internal/harness/harness_test.go`
- Modify: `cmd/spawn.go`
- Modify: `cmd/spawn_test.go`

**Interfaces:**
- Produces: `const RoleEnv = "HAND_ROLE"`, `const HomeEnv = "HAND_HOME"`, and `const WorkerRole = "worker"` in `internal/harness`.
- Extends: `harness.Options` with `FleetHome string`.
- Consumed later: `cmd/session.go` refuses when `os.Getenv(harness.RoleEnv) == harness.WorkerRole`.

- [ ] **Step 1: Write exact launch-environment tests**

```go
func TestBuildCarriesWorkerRoleAndFleetHome(t *testing.T) {
	for _, name := range Names() {
		got, err := Build(name, Options{
			Worktree: "/tmp/wt",
			Brief: "/tmp/brief.md",
			FleetHome: "/tmp/fleet home",
		})
		if err != nil {
			t.Fatalf("Build(%q): %v", name, err)
		}
		want := "HAND_ROLE=worker HAND_HOME='/tmp/fleet home'"
		if !strings.Contains(got, want) {
			t.Fatalf("Build(%q) = %q, want %q", name, got, want)
		}
	}
}
```

Extend `TestSpawnHappyPath` to assert the fake herdr log contains the absolute home and worker role before the harness executable.

- [ ] **Step 2: Run focused tests and verify red**

Run: `go test ./internal/harness ./cmd -run 'Test(BuildCarriesWorker|SpawnHappyPath)' -count=1`

Expected: FAIL because `FleetHome` and the environment prefix do not exist.

- [ ] **Step 3: Prefix every worker launch**

Add the constants and field, then construct the final command as:

```go
env := ""
if opts.FleetHome != "" {
	env = RoleEnv + "=" + WorkerRole + " " + HomeEnv + "=" + shellQuote(opts.FleetHome) + " "
}
return fmt.Sprintf("cd %s && %s%s", shellQuote(opts.Worktree), env, launch), nil
```

Pass `FleetHome: home` from `newSpawnCmd`. Preserve each harness's existing environment variables after the common role/home prefix.

- [ ] **Step 4: Run harness and spawn tests**

Run: `go test ./internal/harness ./cmd -run 'Test(Build|Spawn)' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/harness/harness.go internal/harness/harness_test.go cmd/spawn.go cmd/spawn_test.go
git commit -m "feat(spawn): mark worker process role"
```

---

### Task 4: Harden the Managed Instruction Contract

**Files:**
- Modify: `internal/agentsmd/agentsmd.go`
- Modify: `internal/agentsmd/agentsmd_test.go`
- Modify: `cmd/doctor_test.go`

**Interfaces:**
- Produces: `func SupervisorInstructions() []string`
- Preserves: existing marker spellings and `OperatorDecisionRule`.
- Consumed later: `cmd/session.go` emits `SupervisorInstructions()` as its static operating contract.

- [ ] **Step 1: Write failing append and malformed-marker tests**

```go
func TestRefreshAppendsManagedBlockToUnmarkedAgentsMd(t *testing.T) {
	dir := makeWorkspace(t)
	path := filepath.Join(dir, "AGENTS.md")
	original := "# Project rules\n\nKeep this byte-for-byte.\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := Refresh(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !changed || !strings.HasPrefix(string(got), original) || !strings.Contains(string(got), beginMarker) {
		t.Fatalf("got %q, want unchanged prefix plus managed block", got)
	}
}

func TestRefreshRefusesDuplicateOrUnpairedMarkers(t *testing.T) {
	for name, content := range map[string]string{
		"missing end": beginMarker + "\nmissing end\n",
		"end only": endMarker + "\n",
		"duplicate": generatedBlock() + generatedBlock(),
	} {
		t.Run(name, func(t *testing.T) {
			dir := makeWorkspace(t)
			path := filepath.Join(dir, "AGENTS.md")
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Refresh(dir); err == nil {
				t.Fatal("Refresh succeeded, want malformed-marker error")
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != content {
				t.Fatalf("got %q, want unchanged %q", got, content)
			}
		})
	}
}
```

Replace the existing marker-less informational `doctor` expectation: after this change, `Refresh` repairs absence, while `Check` still reports absence when a user removes the block after initialization. Add duplicate/unpaired violations with exact line numbers where available.

- [ ] **Step 2: Run agentsmd and doctor tests and verify red**

Run: `go test ./internal/agentsmd ./cmd -run 'Test(Refresh|Check|Doctor)' -count=1`

Expected: FAIL because unmarked files remain untouched and duplicates are accepted as one span.

- [ ] **Step 3: Replace the large generated body with a compact bootstrap**

Keep the existing markers and make the generated body state only the startup boundary:

```go
const generatedBody = `## Secondhand supervisor bootstrap

Before responding or acting in a supervising session, run ` + "`hand session start`" + `.
Do not run supervisor bootstrap when ` + "`HAND_ROLE=worker`" + `.
`
```

Move the durable operating rules into a cloned list returned by `SupervisorInstructions`. Include exact lines for: reading operator constraints, project matching, backlog/brief creation, report vocabulary, watch re-arming, explicit merge authorization, delivery before teardown, no edits under `projects/`, absolute paths, hold commands, archive ownership, targeted learnings reads, operator-decision semantics, and TOON/error-kind interpretation.

- [ ] **Step 4: Implement safe append and marker classification**

Change `mergeGenerated` to return `(string, error)` and classify the entire file:

```go
func mergeGenerated(content string) (string, error) {
	startCount := strings.Count(content, beginMarker)
	endCount := strings.Count(content, endMarker)
	switch {
	case startCount == 0 && endCount == 0:
		separator := ""
		if content != "" && !strings.HasSuffix(content, "\n") {
			separator = "\n"
		}
		if content != "" {
			separator += "\n"
		}
		return content + separator + generatedBlock(), nil
	case startCount != 1 || endCount != 1:
		return "", fmt.Errorf("AGENTS.md has malformed or duplicate hand:generated markers")
	}
	start, end, ok := generatedBlockSpan(content)
	if !ok {
		return "", fmt.Errorf("AGENTS.md has malformed hand:generated markers")
	}
	return content[:start] + strings.TrimSuffix(generatedBlock(), "\n") + content[end:], nil
}
```

Keep `Refresh` atomic and do not touch `CLAUDE.md` behavior. Extend `Check` to report absent, duplicate, reversed, and unpaired markers without panicking or treating an arbitrary span as managed.

- [ ] **Step 5: Run focused and package tests**

Run: `go test ./internal/agentsmd ./cmd -run 'Test(Refresh|Check|Doctor)' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/agentsmd/agentsmd.go internal/agentsmd/agentsmd_test.go cmd/doctor_test.go
git commit -m "feat(agentsmd): enforce supervisor bootstrap"
```

---

### Task 5: Add `hand session start`

**Files:**
- Create: `cmd/session.go`
- Create: `cmd/session_test.go`
- Modify: `cmd/root.go`
- Modify: `cmd/root_test.go`
- Modify: `cmd/status.go`
- Modify: `cmd/status_test.go`

**Interfaces:**
- Consumes: `currentWorkerConfig`, `agentsmd.SupervisorInstructions`, `harness.RoleEnv`, `harness.WorkerRole`, `project.List`, `fleetViews`, and existing task columns.
- Produces: `func newSessionCmd(version string) *cobra.Command`
- Produces: `func runSessionStart(cmd *cobra.Command, version string) error`
- Produces: `type backlogSummary struct { Items []string; Queued int }`
- Produces: `func readBacklogSummary(path string, limit int) (backlogSummary, error)`
- Produces: `func sessionNextAction(cfg workerConfig, projectCount int, backlog backlogSummary, views []taskView, holds []state.Hold) string`

- [ ] **Step 1: Write command-shape, role, and error tests**

```go
func TestSessionStartEmitsCompleteBoundedDigest(t *testing.T) {
	home := setupSessionHome(t)
	t.Setenv("HAND_HARNESS", harness.Codex)
	out := runSessionStartForTest(t, "test")
	for _, want := range []string{
		"session_bootstrap: complete\n",
		"supervisor_harness: codex\n",
		"supervisor_harness_source: override\n",
		"harness,detected,codex",
		"model,native-default,none",
		"operator:",
		"projects[0]",
		"tasks[0]",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("out = %q, want %q", out, want)
		}
	}
}

func TestSessionStartRefusesWorkerRole(t *testing.T) {
	setupSessionHome(t)
	t.Setenv(harness.RoleEnv, harness.WorkerRole)
	err := executeSessionStart(t)
	assertExitCode(t, err, 3)
}
```

Add tests for outside-home refusal, missing `data/operator.md`, missing backlog, full operator content, backlog truncation with recovery text, and no stdin read.

- [ ] **Step 2: Write next-action table tests**

Use one table in priority order:

```go
tests := []struct {
	name        string
	cfg         workerConfig
	projects    int
	backlog     backlogSummary
	views       []taskView
	holds       []state.Hold
	want        string
}{
	{"unknown harness", unknownConfig(), 0, backlogSummary{}, nil, nil, "hand config set harness"},
	{"attention before hold", detectedConfig(), 1, backlogSummary{}, []taskView{{task: state.Task{ID: "x"}, unacked: true}}, []state.Hold{{ID: "y"}}, "hand status x"},
	{"hold before no projects", detectedConfig(), 0, backlogSummary{}, nil, []state.Hold{{ID: "x"}}, "hand status x"},
	{"first project", detectedConfig(), 0, backlogSummary{}, nil, nil, "hand project add"},
	{"queued work", detectedConfig(), 1, backlogSummary{Queued: 1}, nil, nil, "prepare the queued task"},
	{"active workers", detectedConfig(), 1, backlogSummary{}, []taskView{{task: state.Task{ID: "x"}}}, nil, "hand watch --until-event"},
	{"idle", detectedConfig(), 1, backlogSummary{}, nil, nil, "fleet is ready and idle"},
}
```

The attention case establishes that an unacknowledged worker event wins within priority two; holds win over project registration when there is no task attention.

- [ ] **Step 3: Run session tests and verify red**

Run: `go test ./cmd -run 'TestSession' -count=1`

Expected: FAIL because the session command and helpers do not exist.

- [ ] **Step 4: Separate fleet-state rendering from fleet help**

Refactor without changing `hand status` output:

```go
func appendFleetState(doc *axi.Doc, views []taskView, holds []state.Hold, cols []axi.Column[taskView]) int {
	attention := 0
	for _, view := range views {
		if needsAttention(view) {
			attention++
		}
	}
	doc.Int("count", len(views))
	doc.Int("attention", attention)
	doc.Int("held", len(holds))
	axi.Table(doc, "tasks", views, cols)
	axi.Table(doc, "holds", holds, holdFields)
	return attention
}
```

`appendFleet` calls this helper and then keeps its existing `fleetHelp`. Add a focused regression asserting `hand status` output is unchanged.

- [ ] **Step 5: Implement bounded backlog parsing**

Read `data/backlog.md` with `bufio.Scanner`. Emit only `##` headings and top-level `- ` or `* ` item lines, cap items at 80, count items under `## Queue` or `## Queued`, and append one final truncation item naming `data/backlog.md` when more exist. Do not emit indented bodies.

- [ ] **Step 6: Implement the session command and shared in-home overview**

Register a runnable `session` group with `start` beneath it. `runSessionStart` performs:

```go
if os.Getenv(harness.RoleEnv) == harness.WorkerRole {
	return &ExitError{Err: fmt.Errorf("supervisor session bootstrap is unavailable when %s=%s", harness.RoleEnv, harness.WorkerRole), Code: 3}
}
fleetHome, err := home.Resolve()
if err != nil {
	return asPrecondition(err)
}
```

Then read `data/operator.md`, `data/backlog.md`, `project.List`, `currentWorkerConfig`, and `fleetViews`. Render in this order:

```text
session_bootstrap
tool/version/exec/home
supervisor_harness/supervisor_harness_source
config_missing/config table
operator
instructions list
project_count/projects table
backlog list
count/attention/held/tasks/holds
help with one prioritized action
```

Use a compact project table with `name`, `mode`, `url`, and `upstream`. Render the full operator file through `doc.Field("operator", strings.TrimSuffix(contents, "\n"))`; `axi.Value` safely quotes multiline content.

Render an empty detected name as `supervisor_harness: unknown`, not `none`. A missing required context file returns an exit-3 `ExitError` naming its absolute path and `hand init <absolute-home>` as the recovery command; malformed project or state data retains the owning reader's existing error.

Make bare `hand` retain its friendly outside-home output. Inside a home, it calls the same overview renderer as `hand session start`, so the two cannot drift. Bare `hand` must also carry `session_bootstrap: complete` when it resolves a home.

- [ ] **Step 7: Run command and root tests**

Run: `go test ./cmd -count=1`

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add cmd/session.go cmd/session_test.go cmd/root.go cmd/root_test.go cmd/status.go cmd/status_test.go
git commit -m "feat(session): add supervisor bootstrap"
```

---

### Task 6: Retire the Claude-Only Hook Safely

**Files:**
- Modify: `internal/sessionhook/sessionhook.go`
- Modify: `internal/sessionhook/sessionhook_test.go`
- Modify: `cmd/init.go`
- Modify: `cmd/init_test.go`
- Modify: `cmd/update.go`
- Modify: `cmd/update_test.go`

**Interfaces:**
- Replaces installation use with: `func Remove(dir, exe string) (bool, error)`.
- Preserves: `handArgs` ownership recognition for the current executable and any executable whose basename is `hand`.

- [ ] **Step 1: Replace installation tests with removal-preservation tests**

```go
func TestRemoveDeletesOnlyOwnedHandHook(t *testing.T) {
	dir := mkHome(t)
	writeSettings(t, dir, map[string]any{"hooks": map[string]any{
		"SessionStart": []any{
			map[string]any{"matcher": "startup", "hooks": []any{
				map[string]any{"type": "command", "command": "/old/path/hand"},
				map[string]any{"type": "command", "command": "/usr/bin/custom"},
			}},
		},
	}})
	changed, err := Remove(dir, "/new/path/hand")
	if err != nil {
		t.Fatal(err)
	}
	if !changed || !reflect.DeepEqual(hookCommands(t, readSettings(t, dir)), []string{"/usr/bin/custom"}) {
		t.Fatalf("settings did not preserve only the unrelated hook")
	}
}
```

Also cover no file, no owned hook, owned-only matcher removal, unrelated matcher preservation, invalid JSON refusal, unexpected shapes, moved binary names, and idempotence.

- [ ] **Step 2: Run hook tests and verify red**

Run: `go test ./internal/sessionhook -count=1`

Expected: FAIL because `Remove` does not exist and `Refresh` still installs.

- [ ] **Step 3: Implement surgical removal**

Read existing settings only when the file exists. Walk `hooks.SessionStart`, filter owned commands out of each matcher's nested `hooks`, remove a matcher only when that filtering leaves it empty, and remove the `SessionStart` key only when no matchers remain. Do not normalize or delete unrelated empty maps. Write atomically only when changed.

Keep shape validation and `handArgs`; delete the installation branch once no caller or test uses `Refresh`.

- [ ] **Step 4: Change init and update reporting**

`hand init` calls `sessionhook.Remove` after refreshing `AGENTS.md` and reports `session_hook: removed` or `unchanged`. Its help says startup is carried by `AGENTS.md`/`CLAUDE.md`, not a Claude hook.

`hand update` also calls `Remove`; rename local `hooked` to `hookRemoved`, change warnings to “retire the session hook,” and keep the existing non-fatal post-update warning behavior. Update exact output tests.

- [ ] **Step 5: Run package tests**

Run: `go test ./internal/sessionhook ./cmd -run 'Test(Init|Update|Remove)' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/sessionhook/sessionhook.go internal/sessionhook/sessionhook_test.go cmd/init.go cmd/init_test.go cmd/update.go cmd/update_test.go
git commit -m "refactor(session): retire Claude-only hook"
```

---

### Task 7: Complete Both FTUE Journeys

**Files:**
- Modify: `AGENTS.md`
- Modify: `README.md`
- Modify: `CONTRIBUTING.md`
- Create: `docs/adr/supervisor-bootstrap-is-an-agents-md-contract.md`
- Modify: `tests/e2e/init_config_test.go`
- Modify: `tests/e2e/e2e_test.go` only if a reusable invocation helper is needed.

**Interfaces:**
- Consumes: the completed `hand init`, `hand session start`, detection override, managed block, and worker-role contracts.
- Produces: user-facing entry paths and whole-binary proof.

- [ ] **Step 1: Write installed-CLI FTUE e2e tests**

Replace `TestFirstRunConfigurationHappensAfterBootstrap` with a flow that initializes an empty home, verifies an existing unmarked project preamble survives with the managed block appended, then starts a detected Codex session:

```go
initialized := runHandEnv(t, home, []string{"HAND_HARNESS=codex"}, "init")
session := runHandEnv(t, home, []string{"HAND_HARNESS=codex"}, "session", "start")
for _, want := range []string{
	"session_bootstrap: complete",
	"supervisor_harness: codex",
	"harness,detected,codex",
	"model,native-default,none",
	"hand project add",
} {
	if !strings.Contains(session.stdout, want) {
		t.Fatalf("session stdout = %q, want %q", session.stdout, want)
	}
}
```

Keep the unwritten-pipe test for both `init` and `session start`. Add `HAND_ROLE=worker` e2e coverage expecting a precondition document and add an unknown-harness case proving `spawn` refuses before invoking fake treehouse/herdr.

Add `TestSourceCheckoutDogfoodBootstrap` by copying the repository's tracked `AGENTS.md` into a temporary directory, running the built local `hand init`, asserting `agents_md: unchanged`, and then running `hand session start` with `HAND_HARNESS=codex`. This pins the clean-checkout invariant without mutating the real checkout.

Add `TestWorkerWorktreeNeverBootstrapsSupervisor` using a nested `projects/secondhand-worktree` directory, `HAND_HOME=<temporary-home>`, and `HAND_ROLE=worker`. Invoke `hand session start` from that directory and assert exit 3, no new `state/hand.db` below the worktree, and no fleet mutation.

- [ ] **Step 2: Run e2e tests and verify the remaining red cases**

Run: `go test -tags=e2e -run 'Test(Init|Session|FirstRun)' ./tests/e2e -count=1`

Expected: FAIL until source instructions and final output copy are aligned.

- [ ] **Step 3: Update the tracked source instructions**

Add a project-owned section before the managed block in `AGENTS.md`:

```md
## Supervising from this checkout

In a main session, when `HAND_ROLE` is not `worker`, bootstrap before responding or acting.
If `state/hand.db` is absent, run `make build` in the documented Nix development environment and then `./hand init`.
Run `./hand session start` after initialization and at the start of every later supervising session.
```

Append the exact current generated block so `hand init` is a no-op on the tracked file. Keep existing source-development rules outside the markers.

- [ ] **Step 4: Update user documentation and ADR**

README quick start documents:

```sh
mkdir ~/fleet
cd ~/fleet
hand init
# Launch any supported harness here; AGENTS.md tells it to run hand session start.
```

CONTRIBUTING documents the source path: enter `nix develop`, run `make build` when needed, launch a harness, and let the tracked instructions initialize and start the dogfood supervisor.

The ADR records: context, decision to use one managed instruction block plus the Claude symlink, rejected per-harness adapters, trust/version-drift trade-offs, command-level prerequisite enforcement, and worker-role isolation. Keep behavioral details in command help and tests.

- [ ] **Step 5: Run e2e and documentation-owned tests**

Run: `go test -tags=e2e -timeout=10m ./tests/e2e/...`

Expected: PASS.

Run: `go test ./internal/agentsmd ./cmd -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add AGENTS.md README.md CONTRIBUTING.md docs/adr/supervisor-bootstrap-is-an-agents-md-contract.md tests/e2e/init_config_test.go tests/e2e/e2e_test.go
git commit -m "docs: define harness-agnostic FTUE"
```

---

### Task 8: Verify the Integrated Change

**Files:**
- Modify only files required by failures found during verification.

**Interfaces:**
- Validates every interface and acceptance criterion above as one integrated binary.

- [ ] **Step 1: Run formatting and lint**

Run: `make lint`

Expected: PASS with no formatting, vet, golangci-lint, or commentlint findings.

- [ ] **Step 2: Build every package**

Run: `go build ./...`

Expected: PASS.

- [ ] **Step 3: Run race-enabled tests**

Run: `go test -race ./...`

Expected: PASS.

- [ ] **Step 4: Run the complete e2e suite**

Run: `make e2e`

Expected: PASS.

- [ ] **Step 5: Check the diff and repository state**

Run: `git diff --check && git status --short && git log --oneline -10`

Expected: no whitespace errors; only intentional changes are present; each implementation task has its own conventional commit.

- [ ] **Step 6: Commit verification-only fixes if any**

If verification required a code or documentation correction, stage only those exact files and commit:

```bash
git commit -m "fix(session): satisfy bootstrap verification"
```

If every command passed without changes, do not create an empty commit.
