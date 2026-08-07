//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atqamz/secondhand/internal/state"
)

// Drives `hand promote` through the built binary: the brief-missing failure path first, then a clean
// promotion asserting the scout's old worktree/herdr identifiers are actually released (not just replaced)
// and that identity fields carry over unchanged while role fields are fully replaced.
func TestPromoteScoutToShip(t *testing.T) {
	t.Setenv("HAND_HARNESS", "claude")
	home := newHome(t)
	registerProject(t, home, "demo", "direct-pr")

	clonePath := filepath.Join(home, "projects", "demo")
	if err := os.MkdirAll(clonePath, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(home, "data", "task-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "data", "task-1", "report.md"), []byte("# report\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	createdAt := time.Now().UTC().Format(time.RFC3339)
	scoutPaneStart := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	oldWorktree := filepath.Join(home, "wt-scout-old")
	if err := state.Write(home, state.Task{
		ID: "task-1", Project: "demo", Kind: state.KindScout,
		Worktree:      oldWorktree,
		LeaseID:       "lease-old",
		Herdr:         state.Herdr{WorkspaceID: "ws-old", TabID: "tab-old", PaneID: "pane-old"},
		CreatedAt:     createdAt,
		PaneStartedAt: scoutPaneStart,
	}); err != nil {
		t.Fatal(err)
	}

	dir := binDir(t)
	newWorktree := filepath.Join(home, "wt-ship-new")
	invocationLog := filepath.Join(t.TempDir(), "invocations.log")
	// Return (worktree.go) only checks CombinedOutput's error on success, never its content, so the "ok" line
	// stands in harmlessly for real treehouse return's actual (silent) output; likewise "pane run" below is a
	// void command whose real success is empty stdout (callVoid doc, client.go), and it checks only env.Error.
	writeFakeDispatch(t, dir, "treehouse", invocationLog, "$1", `  get) printf '{"path":"%s","lease_id":"lease-new"}\n' `+shellSingleQuote(newWorktree)+` ;;
  return) echo ok ;;`)
	// The scout's old workspace holds a second tab, so releasing the scout is a
	// tab close rather than closeTaskTab's sole-tab workspace-close shortcut.
	writeFakeDispatch(t, dir, "herdr", invocationLog, "$1 $2", `  "workspace list") echo '{"result":{"workspaces":[]}}' ;;
  "workspace create") echo '{"result":{"workspace":{"workspace_id":"ws-new","label":"demo","tab_count":1},"tab":{"tab_id":"tab-new","workspace_id":"ws-new","label":"1"},"root_pane":{"pane_id":"pane-new","tab_id":"tab-new","workspace_id":"ws-new","agent_status":"working"}}}' ;;
  "tab rename") echo '{"result":{"tab":{"tab_id":"tab-new","workspace_id":"ws-new","label":"task-1"}}}' ;;
  "pane run") echo '{"result":{}}' ;;
  "pane read") printf 'Welcome to Claude Code\n> \n  ? for shortcuts\n' ;;
  "tab list")
    case "$4" in
      ws-old) echo '{"result":{"tabs":[{"tab_id":"tab-old","workspace_id":"ws-old","label":"demo"},{"tab_id":"tab-other","workspace_id":"ws-old","label":"other"}]}}' ;;
      ws-new) echo '{"result":{"tabs":[{"tab_id":"tab-new","workspace_id":"ws-new","label":"demo"}]}}' ;;
      *) echo "unexpected tab list workspace: $4" >&2; exit 1 ;;
    esac
    ;;
  "tab close") echo '{"result":{}}' ;;
  "workspace close") echo '{"result":{}}' ;;
  "pane get") printf '{"result":{"pane":{"pane_id":"%s","tab_id":"tab-old","workspace_id":"ws-old","agent":"claude","agent_status":"done"}}}\n' "$3" ;;`)

	missingBrief := runHand(t, home, "promote", "task-1")
	assertInvocation(t, missingBrief, 3, "brief not found at data/task-1/brief.md")

	writeBrief(t, home, "task-1")

	promoted := runHand(t, home, "promote", "task-1")
	if promoted.code != 0 {
		t.Fatalf("promote: exit %d, stderr %q", promoted.code, promoted.stderr)
	}
	if !strings.Contains(promoted.stdout, "result: promoted\nkind: ship\nwas: scout\nproject: demo\nharness: claude\n") {
		t.Fatalf("promote stdout = %q, want it to announce the scout->ship transition", promoted.stdout)
	}

	task, err := state.Read(home, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if task.Kind != state.KindShip {
		t.Fatalf("task.Kind = %q, want %q after promote", task.Kind, state.KindShip)
	}
	if task.Project != "demo" || task.CreatedAt != createdAt {
		t.Fatalf("task identity changed: Project=%q CreatedAt=%q, want Project=demo CreatedAt=%q unchanged", task.Project, task.CreatedAt, createdAt)
	}
	if task.Worktree != newWorktree {
		t.Fatalf("task.Worktree = %q, want the new worktree %q", task.Worktree, newWorktree)
	}
	if task.LeaseID != "lease-new" {
		t.Fatalf("task.LeaseID = %q, want the new lease identity, not the scout's", task.LeaseID)
	}
	if task.Herdr.WorkspaceID != "ws-new" || task.Herdr.TabID != "tab-new" || task.Herdr.PaneID != "pane-new" {
		t.Fatalf("task.Herdr = %+v, want the new ws-new/tab-new/pane-new identifiers", task.Herdr)
	}
	if task.Herdr.Session != "default" {
		t.Fatalf("task.Herdr.Session = %q, want %q", task.Herdr.Session, "default")
	}
	if task.Harness != "claude" {
		t.Fatalf("task.Harness = %q, want the default harness recorded", task.Harness)
	}
	if task.PaneStartedAt == "" || task.PaneStartedAt == scoutPaneStart {
		t.Fatalf("task.PaneStartedAt = %q, want it restamped off the scout's %q: parked's silence floor is anchored to it", task.PaneStartedAt, scoutPaneStart)
	}

	logData, err := os.ReadFile(invocationLog)
	if err != nil {
		t.Fatal(err)
	}
	log := string(logData)
	if !strings.Contains(log, "herdr tab list --workspace ws-old") {
		t.Fatalf("invocation log = %q, want promote to have queried the OLD workspace's tabs to release them", log)
	}
	if !strings.Contains(log, "herdr tab close tab-old") {
		t.Fatalf("invocation log = %q, want promote to have actually closed the OLD tab, not just listed it", log)
	}
	if strings.Contains(log, "herdr tab close tab-new") || strings.Contains(log, "herdr workspace close ws-new") {
		t.Fatalf("invocation log = %q, want promote to leave the NEW ship tab open", log)
	}
	if !strings.Contains(log, "herdr tab rename tab-new task-1") {
		t.Fatalf("invocation log = %q, want promote to rename the new workspace's own root tab to the task id", log)
	}
	if strings.Contains(log, "herdr tab create --workspace ws-new") {
		t.Fatalf("invocation log = %q, want promote to reuse the new workspace's root tab instead of creating a second one", log)
	}
	if !strings.Contains(log, "treehouse return "+oldWorktree) {
		t.Fatalf("invocation log = %q, want promote to have returned the OLD worktree %s", log, oldWorktree)
	}
}
