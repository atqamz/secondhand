package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atqamz/secondhand/internal/harness"
	"github.com/atqamz/secondhand/internal/project"
	"github.com/atqamz/secondhand/internal/state"
)

func TestSpawnCleanupReportsAllErrors(t *testing.T) {
	cause := errors.New("spawn failed")
	cleanup := errors.New("cleanup failed")

	err := reportSpawnCleanup(cause, cleanup)
	if !errors.Is(err, cause) {
		t.Fatalf("error %v does not preserve cause", err)
	}
	if !errors.Is(err, cleanup) {
		t.Fatalf("error %v does not preserve cleanup failure", err)
	}
	if !strings.Contains(err.Error(), "cleanup failed") {
		t.Fatalf("error %v does not report cleanup failure", err)
	}
}

// Covers the herdr calls a clean spawn makes: a pane herdr sees claude running in, painted past its
// startup frame with no first-run dialog, so confirmLaunch confirms on its first poll. It echoes an
// envelope for the void "pane run" too, which callVoid accepts (real shapes: internal/faketool/FIDELITY.md).
const fakeHerdrSpawnScript = `#!/bin/sh
cmd="$1 $2"
case "$cmd" in
"workspace list")
	printf '{"id":"cli:1","result":{"workspaces":[{"workspace_id":"wA","label":"hand:myproj","tab_count":1}]}}'
	;;
"tab create")
	printf '{"id":"cli:1","result":{"tab":{"tab_id":"wA:tB","workspace_id":"wA","label":"task-1"},"root_pane":{"pane_id":"wA:pC","tab_id":"wA:tB","agent_status":"idle"}}}'
	;;
"pane run")
 printf '{"id":"cli:1","result":{}}'
	;;
"pane get")
	printf '{"id":"cli:1","result":{"pane":{"pane_id":"%s","tab_id":"wA:tB","workspace_id":"wA","agent":"claude","agent_status":"idle"}}}' "$3"
	;;
"pane read")
	printf 'Welcome to Claude Code\n> \n  ? for shortcuts\n'
	;;
*)
	echo "unexpected herdr args: $@" >&2
	exit 1
	;;
esac
`

// Fakes "treehouse get" as always leasing worktreePath. Real treehouse writes a banner to stderr ahead
// of its JSON on "get" (internal/worktree/worktree.go's Get doc); omitted here since
// tests/e2e/fakes_test.go's writeFakeTreehouse covers the stdout-only parsing where the banner matters.
func writeFakeTreehouseGet(t *testing.T, bin, worktreePath string) {
	t.Helper()
	// A fresh lease_id per invocation, because that - not the recycled slot path - is what real treehouse
	// guarantees unique per acquisition, and what the spawn collision guard keys on.
	counter := filepath.Join(bin, ".treehouse-leases")
	script := "#!/bin/sh\nn=$(cat " + counter + " 2>/dev/null || echo 0)\nn=$((n+1))\necho \"$n\" > " + counter +
		"\nprintf '{\"path\":\"" + worktreePath + "\",\"lease_id\":\"lease-%s\"}' \"$n\"\n"
	if err := os.WriteFile(filepath.Join(bin, "treehouse"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func setupSpawnHome(t *testing.T, worktreePath, herdrScript string) string {
	t.Helper()
	useFastLaunchPolling(t)
	t.Setenv("HAND_HARNESS", harness.Claude)
	home := t.TempDir()

	if err := os.MkdirAll(filepath.Join(home, "data", "task-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := project.Add(home, project.Project{Name: "myproj", URL: "https://example.com/myproj.git", Mode: project.ModeDirectPR}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "data", "task-1", "brief.md"), []byte("do the thing"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "projects", "myproj"), 0o755); err != nil {
		t.Fatal(err)
	}

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(herdrScript), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeTreehouseGet(t, bin, worktreePath)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Chdir(home)
	mkFleetDirs(t, home)
	return home
}

func TestSpawnUsesDetectedHarnessWithoutConfiguredOverride(t *testing.T) {
	wt := filepath.Join(t.TempDir(), "wt")
	codexHerdr := strings.ReplaceAll(fakeHerdrSpawnScript, `"agent":"claude"`, `"agent":"codex"`)
	home := setupSpawnHome(t, wt, codexHerdr)
	t.Setenv("HAND_HARNESS", harness.Codex)

	cmd := newSpawnCmd()
	cmd.SetArgs([]string{"task-1", "myproj"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got, err := state.Read(home, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Harness != harness.Codex {
		t.Fatalf("harness = %q, want detected %q", got.Harness, harness.Codex)
	}
}

func TestSpawnConfiguredHarnessWinsOverDetectedHarness(t *testing.T) {
	wt := filepath.Join(t.TempDir(), "wt")
	home := setupSpawnHome(t, wt, fakeHerdrSpawnScript)
	t.Setenv("HAND_HARNESS", harness.Codex)
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config", "harness"), []byte(harness.Claude+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newSpawnCmd()
	cmd.SetArgs([]string{"task-1", "myproj"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got, err := state.Read(home, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Harness != harness.Claude {
		t.Fatalf("harness = %q, want configured %q", got.Harness, harness.Claude)
	}
}

func TestSpawnUnknownDetectedHarnessFailsBeforeWorktreeAcquisition(t *testing.T) {
	home := setupSpawnHome(t, filepath.Join(t.TempDir(), "wt"), fakeHerdrSpawnScript)
	t.Setenv("HAND_HARNESS", "unknown")
	bin := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]

	cmd := newSpawnCmd()
	cmd.SetArgs([]string{"task-1", "myproj"})
	err := cmd.Execute()
	assertExitCode3(t, err)
	if !strings.Contains(err.Error(), "current supervisor harness is unknown") {
		t.Fatalf("error = %v, want unknown-supervisor remedy", err)
	}
	if _, statErr := os.Stat(filepath.Join(bin, ".treehouse-leases")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("treehouse acquisition counter exists: %v", statErr)
	}
	if _, readErr := state.Read(home, "task-1"); !errors.Is(readErr, state.ErrTaskNotFound) {
		t.Fatalf("task state after refusal = %v", readErr)
	}
}

func TestSpawnHappyPath(t *testing.T) {
	wt := filepath.Join(t.TempDir(), "wt")
	home := setupSpawnHome(t, wt, fakeHerdrSpawnScript)

	cmd := newSpawnCmd()
	cmd.SetArgs([]string{"task-1", "myproj"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	got, err := state.Read(home, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Project != "myproj" || got.Kind != state.KindShip || got.Harness != "claude" {
		t.Fatalf("got %+v", got)
	}
	if got.Worktree != wt {
		t.Fatalf("got worktree %q, want %q", got.Worktree, wt)
	}
	if got.Herdr.WorkspaceID != "wA" || got.Herdr.TabID != "wA:tB" || got.Herdr.PaneID != "wA:pC" {
		t.Fatalf("got herdr %+v", got.Herdr)
	}
	if got.LeaseID != "lease-1" {
		t.Fatalf("got lease id %q, want the identity treehouse handed back", got.LeaseID)
	}
}

// Pins atqamz/secondhand#118: the plain-labelled "myproj" workspace here sorts first in "workspace list"
// and would have won the old bare-label lookup, so hand must resolve to its own "hand:myproj" whatever
// the order, never one it did not create (how a human's workspace collides: internal/faketool/FIDELITY.md).
const fakeHerdrTwoWorkspacesOneLabelScript = `#!/bin/sh
cmd="$1 $2"
case "$cmd" in
"workspace list")
	printf '{"id":"cli:1","result":{"workspaces":[{"workspace_id":"wHuman","label":"myproj","tab_count":1},{"workspace_id":"wA","label":"hand:myproj","tab_count":1}]}}'
	;;
"tab create")
	printf '{"id":"cli:1","result":{"tab":{"tab_id":"wA:tB","workspace_id":"wA","label":"task-1"},"root_pane":{"pane_id":"wA:pC","tab_id":"wA:tB","agent_status":"idle"}}}'
	;;
"pane run")
	printf '{"id":"cli:1","result":{}}'
	;;
"pane get")
	printf '{"id":"cli:1","result":{"pane":{"pane_id":"%s","tab_id":"wA:tB","workspace_id":"wA","agent":"claude","agent_status":"idle"}}}' "$3"
	;;
"pane read")
	printf 'Welcome to Claude Code\n> \n  ? for shortcuts\n'
	;;
*)
	echo "unexpected herdr args: $@" >&2
	exit 1
	;;
esac
`

func TestSpawnIgnoresSameLabelledWorkspaceHandDidNotCreate(t *testing.T) {
	wt := filepath.Join(t.TempDir(), "wt")
	home := setupSpawnHome(t, wt, fakeHerdrTwoWorkspacesOneLabelScript)

	cmd := newSpawnCmd()
	cmd.SetArgs([]string{"task-1", "myproj"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	got, err := state.Read(home, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Herdr.WorkspaceID != "wA" {
		t.Fatalf("got workspace %q, want hand's own hand:myproj workspace wA, not the same-labelled one it did not create", got.Herdr.WorkspaceID)
	}
}

func TestSpawnPersistsResolvedNotDeclaredTierValues(t *testing.T) {
	wt := filepath.Join(t.TempDir(), "wt")
	home := setupSpawnHome(t, wt, fakeHerdrSpawnScript)
	briefPath := filepath.Join(home, "data", "task-1", "brief.md")
	if err := os.WriteFile(briefPath, []byte("---\nmodel: brief-model\neffort: brief-effort\n---\n# Title\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newSpawnCmd()
	cmd.SetArgs([]string{"task-1", "myproj", "--model", "flag-model"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	got, err := state.Read(home, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "flag-model" {
		t.Fatalf("got model %q, want the flag to win over the brief's declared %q", got.Model, "brief-model")
	}
	if got.Effort != "brief-effort" {
		t.Fatalf("got effort %q, want the brief's declared value since no flag or config overrides it", got.Effort)
	}
}

func TestSpawnScoutFlag(t *testing.T) {
	wt := filepath.Join(t.TempDir(), "wt")
	home := setupSpawnHome(t, wt, fakeHerdrSpawnScript)

	cmd := newSpawnCmd()
	cmd.SetArgs([]string{"task-1", "myproj", "--scout"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	got, err := state.Read(home, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != state.KindScout {
		t.Fatalf("got kind %q, want scout", got.Kind)
	}
}

func TestSpawnRejectsUnregisteredProject(t *testing.T) {
	setupSpawnHome(t, filepath.Join(t.TempDir(), "wt"), fakeHerdrSpawnScript)

	cmd := newSpawnCmd()
	cmd.SetArgs([]string{"task-1", "unknown-proj"})
	err := cmd.Execute()
	assertExitCode3(t, err)
	if !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("got err %v, want not registered", err)
	}
}

func TestSpawnRejectsAlreadyActiveTask(t *testing.T) {
	home := setupSpawnHome(t, filepath.Join(t.TempDir(), "wt"), fakeHerdrSpawnScript)
	if err := state.Write(home, state.Task{ID: "task-1"}); err != nil {
		t.Fatal(err)
	}

	cmd := newSpawnCmd()
	cmd.SetArgs([]string{"task-1", "myproj"})
	err := cmd.Execute()
	assertExitCode3(t, err)
	if !strings.Contains(err.Error(), "already active") {
		t.Fatalf("got err %v, want already active", err)
	}
}

// A hold survives the teardown of the task it was set on, so the id it names can
// be free while its question is still open. Reusing that id has to refuse rather
// than reattach the old question to unrelated work.
func TestSpawnRejectsHeldIDWithNoTaskRow(t *testing.T) {
	home := setupSpawnHome(t, filepath.Join(t.TempDir(), "wt"), fakeHerdrSpawnScript)
	if err := state.SetHold(home, state.Hold{ID: "task-1", Kind: state.HoldKindOperator, Reason: "needs a call"}); err != nil {
		t.Fatal(err)
	}

	cmd := newSpawnCmd()
	cmd.SetArgs([]string{"task-1", "myproj"})
	err := cmd.Execute()
	assertExitCode3(t, err)
	if !strings.Contains(err.Error(), "open hold") || !strings.Contains(err.Error(), "hand hold clear task-1") {
		t.Fatalf("got err %v, want an open-hold refusal naming the remedy", err)
	}
	if _, readErr := state.Read(home, "task-1"); !errors.Is(readErr, state.ErrTaskNotFound) {
		t.Fatalf("got %v, want no task row written for a refused spawn", readErr)
	}
}

func TestSpawnAcceptsIDWhoseHoldWasCleared(t *testing.T) {
	home := setupSpawnHome(t, filepath.Join(t.TempDir(), "wt"), fakeHerdrSpawnScript)
	if err := state.SetHold(home, state.Hold{ID: "task-1", Kind: state.HoldKindOperator, Reason: "needs a call"}); err != nil {
		t.Fatal(err)
	}
	if err := state.ClearHold(home, "task-1"); err != nil {
		t.Fatal(err)
	}

	cmd := newSpawnCmd()
	cmd.SetArgs([]string{"task-1", "myproj"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Read(home, "task-1"); err != nil {
		t.Fatal(err)
	}
}

func TestSpawnRejectsMissingBrief(t *testing.T) {
	home := setupSpawnHome(t, filepath.Join(t.TempDir(), "wt"), fakeHerdrSpawnScript)
	if err := os.Remove(filepath.Join(home, "data", "task-1", "brief.md")); err != nil {
		t.Fatal(err)
	}

	cmd := newSpawnCmd()
	cmd.SetArgs([]string{"task-1", "myproj"})
	err := cmd.Execute()
	assertExitCode3(t, err)
	if !strings.Contains(err.Error(), "brief not found") {
		t.Fatalf("got err %v, want brief not found", err)
	}
}

func TestSpawnRejectsUnrecognizedHarness(t *testing.T) {
	setupSpawnHome(t, filepath.Join(t.TempDir(), "wt"), fakeHerdrSpawnScript)

	cmd := newSpawnCmd()
	cmd.SetArgs([]string{"task-1", "myproj", "--harness", "nonexistent"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "not recognized") {
		t.Fatalf("got err %v, want not recognized", err)
	}
	if code := exitCodeFor(t, err); code != 2 {
		t.Fatalf("code = %d, want 2 (err = %v)", code, err)
	}
}

// A row with no lease identity - written before the lease_id column existed -
// is still guarded by worktree path, and that guard is still wired into spawn.
func TestSpawnDetectsWorktreeCollisionAgainstARowWithNoLeaseIdentity(t *testing.T) {
	wt := filepath.Join(t.TempDir(), "wt")
	home := setupSpawnHome(t, wt, fakeHerdrSpawnScript)
	if err := state.Write(home, state.Task{ID: "other-task", Worktree: wt}); err != nil {
		t.Fatal(err)
	}

	cmd := newSpawnCmd()
	cmd.SetArgs([]string{"task-1", "myproj"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "collision") {
		t.Fatalf("got err %v, want collision", err)
	}

	if exists, err := state.Exists(home, "task-1"); err != nil || exists {
		t.Fatalf("state written after collision: exists=%v err=%v", exists, err)
	}
}

// A row left behind by a teardown whose state.Delete failed still names the pool
// slot treehouse has since freed and handed out again. Under a lease of its own
// that is not a collision, and the spawn has to go through.
func TestSpawnAllowsAReusedWorktreePathUnderAFreshLease(t *testing.T) {
	wt := filepath.Join(t.TempDir(), "wt")
	home := setupSpawnHome(t, wt, fakeHerdrSpawnScript)
	if err := state.Write(home, state.Task{ID: "stale-task", Worktree: wt, LeaseID: "lease-0"}); err != nil {
		t.Fatal(err)
	}

	cmd := newSpawnCmd()
	cmd.SetArgs([]string{"task-1", "myproj"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("got err %v, want the spawn to proceed past a stale row on the same path", err)
	}

	got, err := state.Read(home, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.LeaseID != "lease-1" {
		t.Fatalf("got lease id %q, want the identity treehouse handed back", got.LeaseID)
	}
}

// Logs every invocation to $HERDR_CALL_LOG and fails "pane run" so a spawn always fails after tab
// creation, with $HERDR_WS_EXISTS_FLAG driving both leak scenarios, created and pre-existing. Bare exit
// 1, not herdr's void-failure shape: spawn.go branches only on whether PaneRun errored (client_test.go).
const fakeHerdrLeakScript = `#!/bin/sh
echo "$@" >> "$HERDR_CALL_LOG"
cmd="$1 $2"
case "$cmd" in
"workspace list")
	if [ -e "$HERDR_WS_EXISTS_FLAG" ]; then
		printf '{"id":"cli:1","result":{"workspaces":[{"workspace_id":"wA","label":"hand:myproj","tab_count":2}]}}'
	else
		printf '{"id":"cli:1","result":{"workspaces":[]}}'
	fi
	;;
"workspace create")
	printf '{"id":"cli:1","result":{"workspace":{"workspace_id":"wA","label":"myproj"},"tab":{"tab_id":"wA:tB","workspace_id":"wA","label":"1"},"root_pane":{"pane_id":"wA:pC","tab_id":"wA:tB","agent_status":"idle"}}}'
	;;
"tab create")
	printf '{"id":"cli:1","result":{"tab":{"tab_id":"wA:tB","workspace_id":"wA","label":"task-1"},"root_pane":{"pane_id":"wA:pC","tab_id":"wA:tB","agent_status":"idle"}}}'
	;;
"tab rename")
	printf '{"id":"cli:1","result":{"tab":{"tab_id":"wA:tB","workspace_id":"wA","label":"task-1"}}}'
	;;
"pane run")
	exit 1
	;;
"workspace close")
	printf '{"id":"cli:1","result":{"type":"ok"}}'
	;;
"tab list")
	printf '{"id":"cli:1","result":{"tabs":[{"tab_id":"wA:root","workspace_id":"wA","label":"root"},{"tab_id":"wA:tB","workspace_id":"wA","label":"task-1"}]}}'
	;;
"tab close")
	printf '{"id":"cli:1","result":{"type":"ok"}}'
	;;
*)
	echo "unexpected herdr args: $@" >&2
	exit 1
	;;
esac
`

func setupSpawnLeakEnv(t *testing.T, workspaceExists bool) string {
	t.Helper()
	callLog := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("HERDR_CALL_LOG", callLog)

	flag := filepath.Join(t.TempDir(), "ws-exists")
	if workspaceExists {
		if err := os.WriteFile(flag, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HERDR_WS_EXISTS_FLAG", flag)
	return callLog
}

// Launches fine but reports a pane sitting on a dialog nothing answers, the shape confirmLaunch must
// fail rather than record as a spawned task.
const fakeHerdrStuckPaneScript = `#!/bin/sh
echo "$@" >> "$HERDR_CALL_LOG"
cmd="$1 $2"
case "$cmd" in
"workspace list")
	printf '{"id":"cli:1","result":{"workspaces":[]}}'
	;;
"workspace create")
	printf '{"id":"cli:1","result":{"workspace":{"workspace_id":"wA","label":"myproj"},"tab":{"tab_id":"wA:tB","workspace_id":"wA","label":"1"},"root_pane":{"pane_id":"wA:pC","tab_id":"wA:tB","agent_status":"idle"}}}'
	;;
"tab rename")
	printf '{"id":"cli:1","result":{"tab":{"tab_id":"wA:tB","workspace_id":"wA","label":"task-1"}}}'
	;;
"pane run")
	printf '{"id":"cli:1","result":{}}'
	;;
"pane get")
	printf '{"id":"cli:1","result":{"pane":{"pane_id":"%s","tab_id":"wA:tB","workspace_id":"wA","agent":"claude","agent_status":"idle"}}}' "$3"
	;;
"pane read")
	printf 'Some brand new dialog\n> 1. Sure\n  2. Nope\n\nEnter to confirm\n'
	;;
"workspace close")
	printf '{"id":"cli:1","result":{"type":"ok"}}'
	;;
*)
	echo "unexpected herdr args: $@" >&2
	exit 1
	;;
esac
`

func TestSpawnRollsBackWhenWorkerNeverStarts(t *testing.T) {
	wt := filepath.Join(t.TempDir(), "wt")
	home := setupSpawnHome(t, wt, fakeHerdrStuckPaneScript)
	expectLaunchTimeout()
	callLog := setupSpawnLeakEnv(t, false)

	cmd := newSpawnCmd()
	cmd.SetArgs([]string{"task-1", "myproj"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "confirm worker started") {
		t.Fatalf("got err %v, want the spawn to fail on launch confirmation", err)
	}

	if exists, existsErr := state.Exists(home, "task-1"); existsErr != nil || exists {
		t.Fatalf("state written for a worker that never started: exists=%v err=%v", exists, existsErr)
	}
	calls, readErr := os.ReadFile(callLog)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(calls), "workspace close wA") {
		t.Fatalf("calls = %q, want the workspace hand created to be closed", calls)
	}
}

func TestSpawnFailureClosesWorkspaceItCreated(t *testing.T) {
	wt := filepath.Join(t.TempDir(), "wt")
	setupSpawnHome(t, wt, fakeHerdrLeakScript)
	callLog := setupSpawnLeakEnv(t, false)

	cmd := newSpawnCmd()
	cmd.SetArgs([]string{"task-1", "myproj"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected spawn to fail")
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "workspace close wA") {
		t.Fatalf("calls = %q, want the workspace hand created to be closed", calls)
	}
}

// Answers "workspace create" as a herdr predating the tab/root_pane fields would, reporting the workspace
// and omitting both - the partial-response shape atqamz/secondhand#74 fixes. It accepts and logs
// "workspace close" too, since the workspace exists by then and a test could otherwise pass unfixed.
const fakeHerdrPartialWorkspaceCreateScript = `#!/bin/sh
echo "$@" >> "$HERDR_CALL_LOG"
cmd="$1 $2"
case "$cmd" in
"workspace list")
	printf '{"id":"cli:1","result":{"workspaces":[]}}'
	;;
"workspace create")
	printf '{"id":"cli:1","result":{"workspace":{"workspace_id":"wA","label":"myproj"}}}'
	;;
"workspace close")
	printf '{"id":"cli:1","result":{"type":"ok"}}'
	;;
*)
	echo "unexpected herdr args: $@" >&2
	exit 1
	;;
esac
`

func TestSpawnPartialWorkspaceCreateLeavesNoWorkspaceBehind(t *testing.T) {
	wt := filepath.Join(t.TempDir(), "wt")
	home := setupSpawnHome(t, wt, fakeHerdrPartialWorkspaceCreateScript)
	callLog := setupSpawnLeakEnv(t, false)

	cmd := newSpawnCmd()
	cmd.SetArgs([]string{"task-1", "myproj"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "missing workspace, tab, or root pane") {
		t.Fatalf("got err %v, want the partial workspace_created response rejected", err)
	}

	if exists, existsErr := state.Exists(home, "task-1"); existsErr != nil || exists {
		t.Fatalf("state written for a partial workspace_created response: exists=%v err=%v", exists, existsErr)
	}
	calls, readErr := os.ReadFile(callLog)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(calls), "workspace close wA") {
		t.Fatalf("calls = %q, want the workspace herdr created to be closed", calls)
	}
}

// Answers "workspace create" in full and then fails the rename of the root tab into the task's - the
// one failure that lands between herdr creating the workspace and the caller arming its own rollback,
// so the workspace has to be closed by the acquisition step itself.
const fakeHerdrTabRenameFailureScript = `#!/bin/sh
echo "$@" >> "$HERDR_CALL_LOG"
cmd="$1 $2"
case "$cmd" in
"workspace list")
	printf '{"id":"cli:1","result":{"workspaces":[]}}'
	;;
"workspace create")
	printf '{"id":"cli:1","result":{"workspace":{"workspace_id":"wA","label":"myproj"},"tab":{"tab_id":"wA:tB","workspace_id":"wA","label":"1"},"root_pane":{"pane_id":"wA:pC","tab_id":"wA:tB","agent_status":"idle"}}}'
	;;
"tab rename")
	exit 1
	;;
"workspace close")
	printf '{"id":"cli:1","result":{"type":"ok"}}'
	;;
*)
	echo "unexpected herdr args: $@" >&2
	exit 1
	;;
esac
`

func TestSpawnTabRenameFailureClosesWorkspaceItCreated(t *testing.T) {
	wt := filepath.Join(t.TempDir(), "wt")
	home := setupSpawnHome(t, wt, fakeHerdrTabRenameFailureScript)
	callLog := setupSpawnLeakEnv(t, false)

	cmd := newSpawnCmd()
	cmd.SetArgs([]string{"task-1", "myproj"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "herdr tab rename failed") {
		t.Fatalf("got err %v, want the tab rename failure surfaced", err)
	}

	if exists, existsErr := state.Exists(home, "task-1"); existsErr != nil || exists {
		t.Fatalf("state written for a failed tab rename: exists=%v err=%v", exists, existsErr)
	}
	calls, readErr := os.ReadFile(callLog)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(calls), "workspace close wA") {
		t.Fatalf("calls = %q, want the workspace hand created to be closed", calls)
	}
}

func TestSpawnFailureKeepsPreexistingWorkspace(t *testing.T) {
	wt := filepath.Join(t.TempDir(), "wt")
	setupSpawnHome(t, wt, fakeHerdrLeakScript)
	callLog := setupSpawnLeakEnv(t, true)

	cmd := newSpawnCmd()
	cmd.SetArgs([]string{"task-1", "myproj"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected spawn to fail")
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "workspace close") {
		t.Fatalf("calls = %q, want the pre-existing shared workspace left open", calls)
	}
	if !strings.Contains(string(calls), "tab close wA:tB") {
		t.Fatalf("calls = %q, want the task's own tab closed", calls)
	}
}
