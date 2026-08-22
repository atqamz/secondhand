//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/atqamz/hand/internal/faketool"
)

// Everything hand or bootstrap.sh execs beyond these is faked per test and left unreachable (e2e_test.go).
// git, sh and cat back real production and fixture calls; the rest are plain POSIX utilities bootstrap.sh
// runs, never one of the backends (herdr, treehouse, gh, no-mistakes) this suite fakes instead.
var realBinsOnPath = []string{"git", "sh", "cat", "uname", "dirname", "basename", "awk", "sed", "head", "grep", "tr", "ls", "mkdir", "mktemp", "rm", "chmod", "cp", "mv", "dd", "gzip", "install", "sha256sum", "tar"}

// The PATH every test runs under, built once by TestMain from the inherited PATH.
var hermeticPath string

func buildHermeticPath(dir string) (string, error) {
	if err := os.Mkdir(dir, 0o755); err != nil {
		return "", err
	}
	for _, name := range realBinsOnPath {
		resolved, err := exec.LookPath(name)
		if err != nil {
			return "", fmt.Errorf("resolve %s, which this suite runs for real: %w", name, err)
		}
		// Each binary is symlinked in individually rather than given its own directory on PATH: on a real
		// machine git commonly lives in the same directory as real herdr and treehouse, so exposing that
		// directory would hand the suite straight back the tools it fakes.
		linkName := name
		if runtime.GOOS == "windows" {
			linkName += ".exe"
		}
		if err := os.Symlink(resolved, filepath.Join(dir, linkName)); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// Returns a directory prepended to the current PATH for the rest of the test, so fake binaries written
// there are found first. Prepending keeps this additive - a test can call binDir twice and keep both dirs.
func binDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// Hermetic only because TestMain runs first: by then PATH is already hermeticPath, so there is no ambient
	// PATH left to inherit here.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func writeFakeBin(t *testing.T, dir, name, caseBody string) {
	t.Helper()
	script := "#!/bin/sh\n" + caseBody
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// Writes a fake binary that dispatches caseBody on selector (the shell expression each case arm matches,
// e.g. "$1" or "$1 $2") and fails loudly on any invocation shape the test did not anticipate.
func writeFakeDispatch(t *testing.T, dir, name, logPath, selector, caseBody string) {
	t.Helper()
	script := ""
	// Every invocation is appended as "<name> <args...>", so a test can assert on which calls were and were
	// not made.
	if logPath != "" {
		script = fmt.Sprintf("echo \"%s $@\" >> %s\n", name, shellSingleQuote(logPath))
	}
	script += "if [ \"$1\" = \"--session\" ]; then shift 2; fi\n"
	script += fmt.Sprintf("case \"%s\" in\n%s\n  *) echo \"unexpected %s invocation: $@\" >&2; exit 1 ;;\nesac\n",
		selector, caseBody, name)
	writeFakeBin(t, dir, name, script)
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// The fixed set of workspace/tab/pane identifiers a static fake herdr hands back for a single
// spawn/promote + teardown lifecycle.
type herdrIDs struct {
	WorkspaceID string
	TabID       string
	PaneID      string
	Label       string
	PaneStatus  string // agent_status reported by "pane get"; defaults to "working" if empty
	Agent       string // harness process reported by pane get and pane process-info; defaults to claude
	Agents      []string
}

// The herdr fake for a spawn (or promote) followed by a teardown within one test: no workspace exists
// yet, so the command creates one, and the create's response carries the root tab and pane herdr makes
// alongside it - the ones the task renames and reuses instead of creating a second tab.
func writeFakeHerdrStatic(t *testing.T, dir string, ids herdrIDs) {
	t.Helper()
	// A test needing a workspace already open declares faketool.Herdr itself rather than going through
	// here.
	writeFakeHerdrStaticLogged(t, dir, "", ids)
}

// writeFakeHerdrStatic plus an invocation log, for tests that assert which herdr
// calls were made: that spawn reuses the workspace's own root tab rather than
// creating a second one, and that tearing that sole tab down closes the workspace.
func writeFakeHerdrStaticLogged(t *testing.T, dir, logPath string, ids herdrIDs) {
	t.Helper()
	// Real herdr creates a tab whenever it is asked to, but a generated fake has to know the identifiers
	// up front, so a few spares stand ready for the second and later task spawned into the workspace the
	// first one created. Running out is a loud failure, never a silent reuse of the first task's tab.
	spares := make([]faketool.HerdrTab, 4)
	for i := range spares {
		spares[i] = faketool.HerdrTab{
			ID:   fmt.Sprintf("%s-%d", ids.TabID, i+2),
			Pane: fmt.Sprintf("%s-%d", ids.PaneID, i+2),
		}
	}
	agent := ids.Agent
	if agent == "" {
		agent = "claude"
	}
	faketool.Herdr{
		Creates: []faketool.HerdrWorkspace{{ID: ids.WorkspaceID, Label: ids.Label, Tabs: []faketool.HerdrTab{
			{ID: ids.TabID, Label: "1", Pane: ids.PaneID},
		}}},
		TabCreates:    spares,
		PaneAgent:     agent,
		ProcessAgent:  agent,
		ProcessAgents: ids.Agents,
		PaneStatus:    ids.PaneStatus,
		Log:           logPath,
	}.Install(t, dir)
}

// A herdr fake for the watch scenario: "workspace list" always succeeds, satisfying watcher.Run's
// reachability probe, and "pane get <id>" reports whatever status sits in statusDir/<id>, letting a test
// drive per-task transitions while `hand watch` polls in the background by rewriting one file per task.
func writeFakeHerdrWatch(t *testing.T, dir, statusDir, logPath string) {
	t.Helper()
	// Both are query commands per internal/herdr/client.go's call() doc comment: real success is a non-null
	// result object on exit 0, real failure a non-zero exit plus a diagnostic on stderr (the same contract
	// cmd/status_test.go's writeFakeHerdrPaneStatus documents), which the "unreachable" sentinel reproduces.
	quotedStatusDir, quotedLog := shellSingleQuote(statusDir), shellSingleQuote(logPath)
	// The reported agent comes from statusDir/<id>.agent, empty unless a test calls setPaneAgent, which keeps
	// a scenario that never mentions an agent out of every harness-capability path. "pane read" answers with
	// statusDir/<id>.text - bare stdout, not a result envelope (client.go's PaneRead) - and the steers void.
	body := fmt.Sprintf(`  "workspace list") echo '{"result":{"workspaces":[]}}' ;;
  "pane get")
    status=$(cat %s/"$3" 2>/dev/null || echo idle)
    agent=$(cat %s/"$3".agent 2>/dev/null || echo "")
    echo "herdr pane get $3" >> %s
    if [ "$status" = unreachable ]; then
      echo "herdr: pane $3 not found" >&2
      exit 1
    fi
    printf '{"result":{"pane":{"pane_id":"%%s","tab_id":"t-1","workspace_id":"w-1","agent":"%%s","agent_status":"%%s"}}}\n' "$3" "$agent" "$status"
    ;;
  "pane read")
    echo "herdr pane read $3" >> %s
    cat %s/"$3".text 2>/dev/null
    ;;
  "pane send-text") echo "herdr pane send-text $3 $4" >> %s ;;
  "pane send-keys") echo "herdr pane send-keys $3 $4" >> %s ;;`,
		quotedStatusDir, quotedStatusDir, quotedLog, quotedLog, quotedStatusDir, quotedLog, quotedLog)
	// Each "pane get" is logged after the status read, never before: a test waiting on the Nth poll before
	// publishing would otherwise still be racing that poll's read. The failing branch logs too, so waiting on
	// the Nth probe works for a dark pane - one taken down and brought back mid-poll - as for a healthy one.
	writeFakeDispatch(t, dir, "herdr", "", "$1 $2", body)
}

// A herdr fake for the send scenario: "pane get" reports whatever status sits in statusDir/<pane-id>, so a
// test can free a busy composer while `hand send` is waiting on it, and "pane send-text"/"pane send-keys"
// answer with the empty stdout real herdr gives a void command (client.go's callVoid doc comment).
func writeFakeHerdrSend(t *testing.T, dir, statusDir, logPath string) {
	t.Helper()
	// Every invocation is logged with the pid of the hand process that made it, which is what lets a test tell
	// two concurrent senders apart; each pane status read is logged after the read, for the same reason
	// writeFakeHerdrWatch does it.
	quotedLog := shellSingleQuote(logPath)
	body := fmt.Sprintf(`  "pane get")
    status=$(cat %s/"$3" 2>/dev/null || echo idle)
    echo "sender=$PPID pane get $3" >> %s
    printf '{"result":{"pane":{"pane_id":"%%s","tab_id":"t-1","workspace_id":"w-1","agent":"claude","agent_status":"%%s"}}}\n' "$3" "$status"
    ;;
  "pane send-text") echo "sender=$PPID pane send-text $4" >> %s ;;
  "pane send-keys") echo "sender=$PPID pane send-keys $4" >> %s ;;`,
		shellSingleQuote(statusDir), quotedLog, quotedLog, quotedLog)
	writeFakeDispatch(t, dir, "herdr", "", "$1 $2", body)
}

func writeFakeHerdrUnprobeablePanes(t *testing.T, dir string) {
	t.Helper()
	body := `  "workspace list") echo '{"result":{"workspaces":[]}}' ;;
  "pane get") echo "herdr: pane $3 not found" >&2; exit 1 ;;`
	writeFakeDispatch(t, dir, "herdr", "", "$1 $2", body)
}

// Publishes a pane's status by atomic rename: the fake herdr cats these files from a concurrently polling
// `hand watch`, and a truncating in-place write would let it read a phantom empty status mid-update.
func setPaneStatus(t *testing.T, statusDir, paneID, status string) {
	t.Helper()
	publishPaneFile(t, statusDir, paneID, status)
}

// Publishes which agent the fake reports running in a pane, driving every harness-capability path the
// watcher takes.
func setPaneAgent(t *testing.T, statusDir, paneID, agent string) {
	t.Helper()
	publishPaneFile(t, statusDir, paneID+".agent", agent)
}

// Publishes the scrollback the fake answers `pane read` with.
func setPaneText(t *testing.T, statusDir, paneID, text string) {
	t.Helper()
	publishPaneFile(t, statusDir, paneID+".text", text)
}

func publishPaneFile(t *testing.T, statusDir, name, content string) {
	t.Helper()
	tmp := filepath.Join(statusDir, name+".tmp")
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(statusDir, name)); err != nil {
		t.Fatal(err)
	}
}

// A one-slot treehouse pool at worktreePath, plus any paths it leased out before the test began - a
// scout's worktree a promote hands back, say, which the real pool would refuse as unmanaged if it were
// never declared.
func writeFakeTreehouse(t *testing.T, dir, worktreePath string, alreadyLeased ...string) {
	t.Helper()
	// internal/faketool leases the slot exclusively, the way real treehouse's pool lock holds it, so no
	// test can build two live tasks on one slot - a fixture the real backend never produces - and prove
	// the collision guard against a state that never occurs.
	faketool.Treehouse{Slots: []string{worktreePath}, Held: alreadyLeased}.Install(t, dir)
}

// The same one-slot pool as a treehouse older than v2.1.0: it leases and frees the slot identically but
// reports no lease_id at all, which drives worktree.CheckCollision down its path-comparison fallback -
// the same branch a task row written before the lease_id column existed takes.
func writeFakeTreehouseWithoutLeaseIdentity(t *testing.T, dir, worktreePath string) {
	t.Helper()
	faketool.Treehouse{
		Slots:           []string{worktreePath},
		Banner:          "treehouse 0.7.4",
		NoLeaseIdentity: true,
	}.Install(t, dir)
}

// Frees a leased pool slot through the fake treehouse's own return arm. It stands in for the return
// `hand teardown` runs before it deletes the task's row: when that deletion fails, this is exactly the
// state left behind - the slot back in the pool, a row still naming it.
func returnFakeWorktree(t *testing.T, worktreePath string) {
	t.Helper()
	out, err := exec.Command("treehouse", "return", worktreePath).CombinedOutput()
	if err != nil {
		t.Fatalf("fake treehouse return %s: %v: %s", worktreePath, err, out)
	}
}

// Makes `git clone <matchURL> <dest>` resolve to a local repo instead of the network, via git's
// url.<target>.insteadOf mechanism, appending the rule to the scratch config isolateGitConfig already
// points this test's git invocations at.
func redirectGitRemote(t *testing.T, matchURL, localRepoPath string) {
	t.Helper()
	cfg := isolateGitConfig(t)
	f, err := os.OpenFile(cfg, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := fmt.Fprintf(f, "[url \"file://%s\"]\n\tinsteadOf = %s\n", localRepoPath, matchURL); err != nil {
		t.Fatal(err)
	}
}
