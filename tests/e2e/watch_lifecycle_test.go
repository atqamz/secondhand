//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/atqamz/hand/internal/faketool"
	"github.com/atqamz/hand/internal/state"
)

// An `--until-event` incumbent with no event due is taken over by an explicit
// --takeover successor: the displaced watcher must exit 9 / watch-replaced with
// no synthetic event on stdout, and the successor must become the published owner.
func TestWatchUntilEventIncumbentIsReplacedByTakeover(t *testing.T) {
	home := seedOneTaskHome(t) // task-1 pane stays "working": no transition, no timeout expiry below

	incumbent := startHandBackground(t, home, "watch", "--until-event", "--poll", "30ms", "--timeout", "60s")
	incumbentRec := waitForCoherentOwner(t, home, incumbent.cmd.Process.Pid)

	successor := startHandBackground(t, home, "watch", "--until-event", "--poll", "30ms", "--timeout", "60s", "--takeover")

	got := incumbent.waitForExit(t, 10*time.Second, "an explicit takeover")
	if got.code != 9 {
		t.Fatalf("displaced until-event watch: exit %d, want 9 watch-replaced (stdout %q, stderr %q)", got.code, got.stdout, got.stderr)
	}
	if !strings.Contains(got.stderr, "watch-replaced") {
		t.Fatalf("stderr = %q, want kind watch-replaced", got.stderr)
	}
	if strings.TrimSpace(got.stdout) != "" {
		t.Fatalf("stdout = %q, want it empty: a takeover must not synthesize a fake event for exit 0", got.stdout)
	}

	succRec := waitForCoherentOwner(t, home, successor.cmd.Process.Pid)
	if succRec.Generation == incumbentRec.Generation {
		t.Fatalf("successor published the incumbent's generation %q, want a fresh one", succRec.Generation)
	}

	successor.interrupt(t, 10*time.Second)
}

func TestWatchConnectIsReplacedWhileHerdrIsBlocked(t *testing.T) {
	home := newHome(t)
	callLog := filepath.Join(t.TempDir(), "herdr-calls")
	faketool.Herdr{Hang: []string{"workspace list"}, Log: callLog, LogCommands: []string{"workspace list"}}.Install(t, binDir(t))

	incumbent := startHandBackground(t, home, "watch", "--poll", "30ms")
	waitForInvocation(t, callLog, "herdr workspace list", 10*time.Second)
	successor := startHandBackground(t, home, "watch", "--poll", "30ms", "--takeover")

	got := incumbent.waitForExit(t, 10*time.Second, "takeover during connect")
	if got.code != 9 || !strings.Contains(got.stderr, "watch-replaced") || strings.TrimSpace(got.stdout) != "" {
		t.Fatalf("blocked-connect incumbent: exit %d stdout %q stderr %q, want exit 9/watch-replaced and empty stdout", got.code, got.stdout, got.stderr)
	}
	waitForCoherentOwner(t, home, successor.cmd.Process.Pid)
	successor.interrupt(t, 10*time.Second)
}

func TestWatchConnectTimeoutWhileHerdrIsBlocked(t *testing.T) {
	home := newHome(t)
	callLog := filepath.Join(t.TempDir(), "herdr-calls")
	faketool.Herdr{Hang: []string{"workspace list"}, Log: callLog, LogCommands: []string{"workspace list"}}.Install(t, binDir(t))

	got := runHand(t, home, "watch", "--until-event", "--poll", "30ms", "--timeout", "100ms")
	if got.code != 4 || !strings.Contains(got.stderr, "no-event") || strings.TrimSpace(got.stdout) != "" {
		t.Fatalf("blocked-connect timeout: exit %d stdout %q stderr %q, want exit 4/no-event and empty stdout", got.code, got.stdout, got.stderr)
	}
}

func TestWatchArmProbeIsReplacedWhileHerdrIsBlocked(t *testing.T) {
	home := seedOneTaskHome(t)
	callLog := filepath.Join(t.TempDir(), "herdr-calls")
	faketool.Herdr{Hang: []string{"pane get"}, Log: callLog, LogCommands: []string{"pane get"}, AllowUnknownPane: true}.Install(t, binDir(t))

	incumbent := startHandBackground(t, home, "watch", "--until-event", "--poll", "30ms", "--timeout", "60s")
	waitForInvocation(t, callLog, "herdr pane get", 10*time.Second)
	successor := startHandBackground(t, home, "watch", "--until-event", "--poll", "30ms", "--timeout", "60s", "--takeover")

	got := incumbent.waitForExit(t, 10*time.Second, "takeover during arm probe")
	if got.code != 9 || strings.Contains(got.stderr, "arm-failed") || strings.TrimSpace(got.stdout) != "" {
		t.Fatalf("blocked-arm incumbent: exit %d stdout %q stderr %q, want exit 9 without arm-failed or stdout", got.code, got.stdout, got.stderr)
	}
	waitForCoherentOwner(t, home, successor.cmd.Process.Pid)
	successor.interrupt(t, 10*time.Second)
}

func TestWatchArmProbeTimeoutWhileHerdrIsBlocked(t *testing.T) {
	home := seedOneTaskHome(t)
	callLog := filepath.Join(t.TempDir(), "herdr-calls")
	faketool.Herdr{Hang: []string{"pane get"}, Log: callLog, LogCommands: []string{"pane get"}, AllowUnknownPane: true}.Install(t, binDir(t))

	got := runHand(t, home, "watch", "--until-event", "--poll", "30ms", "--timeout", "100ms")
	if got.code != 4 || strings.Contains(got.stderr, "arm-failed") || strings.TrimSpace(got.stdout) != "" {
		t.Fatalf("blocked-arm timeout: exit %d stdout %q stderr %q, want exit 4 without arm-failed or stdout", got.code, got.stdout, got.stderr)
	}
}

// Ordinary commands never enter watcher ownership: with a live streaming
// incumbent, a successful status / session start / send / spawn must leave the
// same watcher alive with the same ownership generation.
func TestOrdinaryCommandsDoNotEvictTheWatcher(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setup   func(t *testing.T, home string) string
		command func(t *testing.T, home string)
	}{
		{"status", setupWatchFakeHome, func(t *testing.T, home string) {
			assertInvocation(t, runHand(t, home, "status"), 0, "")
		}},
		{"session start", setupWatchFakeHome, func(t *testing.T, home string) {
			assertInvocation(t, runHand(t, home, "session", "start"), 0, "")
		}},
		{"send", setupSendFakeHome, func(t *testing.T, home string) {
			assertInvocation(t, runHand(t, home, "send", "task-1", "steer one"), 0, "")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := newHome(t)
			registerProject(t, home, "demo", "direct-pr")
			statusDir := tc.setup(t, home)

			watcher := startHandBackground(t, home, "watch", "--poll", "30ms")
			rec := waitForCoherentOwner(t, home, watcher.cmd.Process.Pid)
			// Swallow any watcher events as real fleet traffic; only nudge the fixture for send.
			if tc.name == "send" {
				setPaneStatus(t, statusDir, "pane-1", "idle")
				watcher.waitForStdout(t, "idle-unreported task-1", 5*time.Second)
			}

			tc.command(t, home)
			assertWatcherUnaffected(t, home, watcher, rec)
			watcher.interrupt(t, 10*time.Second)
		})
	}

	t.Run("spawn", func(t *testing.T) {
		home := newHome(t)
		registerProject(t, home, "demo", "local-only")
		writeBrief(t, home, "task-1")

		clonePath := filepath.Join(home, "projects", "demo")
		initGitRepo(t, clonePath)
		worktree := filepath.Join(home, "wt-task-1")
		runGitIn(t, clonePath, "worktree", "add", "-q", "-b", "task-1-branch", worktree)

		dir := binDir(t)
		invLog := filepath.Join(t.TempDir(), "invocations.log")
		writeFakeTreehouse(t, dir, worktree)
		writeFakeHerdrStaticLogged(t, dir, invLog, herdrIDs{WorkspaceID: "ws-1", TabID: "tab-1", PaneID: "pane-1", Label: "demo"})

		watcher := startHandBackground(t, home, "watch", "--poll", "30ms")
		rec := waitForCoherentOwner(t, home, watcher.cmd.Process.Pid)

		spawned := runHand(t, home, "spawn", "task-1", "demo")
		if spawned.code != 0 {
			t.Fatalf("spawn did not run while a watcher was attached: exit %d, stderr %q", spawned.code, spawned.stderr)
		}
		assertWatcherUnaffected(t, home, watcher, rec)
		watcher.interrupt(t, 10*time.Second)
	})
}

func setupWatchFakeHome(t *testing.T, home string) string {
	t.Helper()
	statusDir := t.TempDir()
	setPaneStatus(t, statusDir, "pane-1", "working")
	writeTaskAttempt(t, home, state.Task{ID: "task-1", Project: "demo", Kind: state.KindShip,
		CreatedAt: time.Now().UTC().Format(time.RFC3339)}, state.Attempt{Lifecycle: state.AttemptRunning,
		Worktree: filepath.Join(home, "wt-1"), Herdr: state.Herdr{PaneID: "pane-1"}})
	writeFakeHerdrWatch(t, binDir(t), statusDir, filepath.Join(t.TempDir(), "herdr-invocations.log"))
	return statusDir
}

func setupSendFakeHome(t *testing.T, home string) string {
	t.Helper()
	seedSendTask(t, home)
	statusDir := t.TempDir()
	setPaneStatus(t, statusDir, "pane-1", "working")
	// The watch fake doubles as a send fake: it answers workspace list (for the
	// polling watcher's connect), pane get, and the send-text/send-keys voids.
	writeFakeHerdrWatch(t, binDir(t), statusDir, filepath.Join(t.TempDir(), "herdr-invocations.log"))
	return statusDir
}

// Proves the incumbent survives an ordinary command: still alive, still the same
// published pid and generation, and still the sole owner (a plain non-takeover
// watch is still refused with exit 3).
func assertWatcherUnaffected(t *testing.T, home string, watcher *backgroundHand, rec ownerPublication) {
	t.Helper()
	now, err := readOwnerPublication(t, home)
	if err != nil {
		t.Fatalf("read owner publication after ordinary command: %v", err)
	}
	if now.PID != rec.PID || now.Generation != rec.Generation {
		t.Fatalf("ownership changed after an ordinary command: was pid %d gen %s, now pid %d gen %s",
			rec.PID, rec.Generation, now.PID, now.Generation)
	}
	refused := runHand(t, home, "watch", "--poll", "30ms")
	if refused.code != 3 || !strings.Contains(refused.stderr, "already attached") {
		t.Fatalf("after an ordinary command the incumbent no longer refuses ownership: exit %d stderr %q (want lock contention)",
			refused.code, refused.stderr)
	}
}

// A stale routing record cannot block a new owner when the kernel lock is free,
// and a malformed partial record is never a takeover target.
func TestWatchStartsOverStaleAndMalformedRoutingMetadata(t *testing.T) {
	home := seedOneTaskHome(t)

	// A fully-published stale record from a crashed predecessor whose lock is free:
	// the fresh watcher must acquire and publish its own generation.
	ownerPath := filepath.Join(state.Dir(home), "watch.owner")
	if err := os.WriteFile(ownerPath, []byte(fmt.Sprintf(`{"version":1,"generation":"%s","pid":%d}`, strings.Repeat("d", 32), 999999)), 0o644); err != nil {
		t.Fatal(err)
	}
	got := runHand(t, home, "watch", "--until-event", "--poll", "30ms", "--timeout", "100ms")
	if got.code != 4 {
		t.Fatalf("watch over a stale routing record (lock free): exit %d, want 4 no-event (stderr %q)", got.code, got.stderr)
	}

	// Same with malformed routing metadata: no destructive action, lock still decides.
	if err := os.WriteFile(ownerPath, []byte(`{truncated`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeOwnerFileRaw(t, home, strconv.Itoa(os.Getpid())+"\n"); err != nil {
		t.Fatal(err)
	}
	got = runHand(t, home, "watch", "--until-event", "--poll", "30ms", "--timeout", "100ms")
	if got.code != 4 {
		t.Fatalf("watch over malformed routing metadata: exit %d, want 4 no-event (stderr %q)", got.code, got.stderr)
	}
}

func writeOwnerFileRaw(t *testing.T, home, content string) error {
	t.Helper()
	return os.WriteFile(filepath.Join(state.Dir(home), "watch.pid"), []byte(content), 0o644)
}
