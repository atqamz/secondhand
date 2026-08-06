//go:build e2e

package e2e

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Runs hand with stdin the caller chooses and a deadline, because the failure being tested for is a
// process that never returns rather than one that returns the wrong thing.
func runHandStdin(t *testing.T, home string, stdin *os.File, args ...string) invocation {
	t.Helper()
	cmd := exec.Command(handBin, args...)
	cmd.Dir = home
	cmd.Env = handProcessEnv()
	cmd.Stdin = stdin
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start hand %v: %v", args, err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		code := 0
		if err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("wait hand %v: %v", args, err)
			}
			code = exitErr.ExitCode()
		}
		t.Logf("$ hand %s\n  exit %d\n  stdout: %s\n  stderr: %s",
			strings.Join(args, " "), code, strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()))
		return invocation{code: code, stdout: stdout.String(), stderr: stderr.String()}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("hand %v never returned with nothing to read on stdin; stdout=%q stderr=%q",
			args, stdout.String(), stderr.String())
		return invocation{}
	}
}

// An open pipe nothing ever writes to, which is the stdin a read blocks forever on. /dev/null returns EOF
// immediately, so it catches a read that fails but not one that waits.
func openPipeStdin(t *testing.T) *os.File {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	return reader
}

func devNull(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case entry.IsDir():
			snapshot[rel] = "directory"
		case entry.Type()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			snapshot[rel] = "symlink:" + target
		default:
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			snapshot[rel] = "file:" + string(content)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func assertTreeUnchanged(t *testing.T, root string, before map[string]string) {
	t.Helper()
	after := snapshotTree(t, root)
	if len(after) != len(before) {
		t.Fatalf("tree %s has %d entries after session start, want %d", root, len(after), len(before))
	}
	for path, want := range before {
		if got, ok := after[path]; !ok || got != want {
			t.Fatalf("tree %s entry %q changed", root, path)
		}
	}
}

// Bootstrap runs in scripts and CI, and session start runs at every supervising session, so neither may
// ever depend on something being on stdin.
func TestInitAndSessionStartNeverBlockOnStdin(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stdin func(*testing.T) *os.File
	}{
		{"dev-null", devNull},
		{"unwritten-pipe", openPipeStdin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateGitConfig(t)
			home := t.TempDir()

			initialized := runHandStdin(t, home, tc.stdin(t), "init")
			if initialized.code != 0 {
				t.Fatalf("hand init: exit %d, stderr %q", initialized.code, initialized.stderr)
			}

			session := runHandStdin(t, home, tc.stdin(t), "session", "start")
			if session.code != 0 {
				t.Fatalf("hand session start: exit %d, stderr %q", session.code, session.stderr)
			}
		})
	}
}

// This is the installed-binary first run: operator-owned instructions survive initialization, and the
// generated block carries the next supervising session into the complete bootstrap document.
func TestFirstRunInstalledCLIBootstrapsADetectedSession(t *testing.T) {
	isolateGitConfig(t)
	home := t.TempDir()
	const preamble = "# My fleet\n\nKeep this project instruction.\n"
	if err := os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte(preamble), 0o644); err != nil {
		t.Fatal(err)
	}

	initialized := runHandEnv(t, home, []string{"HAND_HARNESS=codex"}, "init")
	if initialized.code != 0 {
		t.Fatalf("hand init: exit %d, stderr %q", initialized.code, initialized.stderr)
	}
	agents, err := os.ReadFile(filepath.Join(home, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	const managedBootstrap = `<!-- hand:generated:start -->
## Secondhand supervisor bootstrap

Before responding or acting in a supervising session, run ` + "`hand session start`" + `.
Do not run supervisor bootstrap when ` + "`HAND_ROLE=worker`" + `.
<!-- hand:generated:end -->
`
	wantAgents := preamble + "\n" + managedBootstrap
	if string(agents) != wantAgents {
		t.Fatalf("AGENTS.md = %q, want exact preserved preamble plus current managed bootstrap %q", agents, wantAgents)
	}

	session := runHandEnv(t, home, []string{"HAND_HARNESS=codex"}, "session", "start")
	if session.code != 0 {
		t.Fatalf("hand session start: exit %d, stderr %q", session.code, session.stderr)
	}
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
}

func TestSessionStartRefusesWorkerRole(t *testing.T) {
	home := newHome(t)
	got := runHandEnv(t, home, []string{"HAND_ROLE=worker"}, "session", "start")
	assertInvocation(t, got, 3, "supervisor session bootstrap is unavailable when HAND_ROLE=worker")
	if !strings.Contains(got.stderr, "kind: precondition") {
		t.Fatalf("stderr = %q, want a precondition document", got.stderr)
	}
}

// Harness choice is a dispatch prerequisite, so spawn must refuse before either acquisition backend can
// mutate external state.
func TestSpawnWithUnknownHarnessRefusesBeforeAcquisition(t *testing.T) {
	home := newHome(t)
	registerProject(t, home, "demo", "local-only")
	writeBrief(t, home, "task-1")
	if err := os.MkdirAll(filepath.Join(home, "projects", "demo"), 0o755); err != nil {
		t.Fatal(err)
	}

	invocationLog := filepath.Join(t.TempDir(), "acquisition.log")
	body := "echo invoked >> " + shellSingleQuote(invocationLog) + "\nexit 99\n"
	dir := binDir(t)
	writeFakeBin(t, dir, "treehouse", body)
	writeFakeBin(t, dir, "herdr", body)

	got := runHandEnv(t, home, []string{"HAND_HARNESS=unknown"}, "spawn", "task-1", "demo", "--skip-gate-check")
	assertInvocation(t, got, 3, "current supervisor harness is unknown")
	if logged, err := os.ReadFile(invocationLog); err == nil {
		t.Fatalf("acquisition log = %q, want treehouse and herdr untouched", logged)
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestSourceCheckoutDogfoodBootstrap(t *testing.T) {
	isolateGitConfig(t)
	tracked, err := os.ReadFile(filepath.Join("..", "..", "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "AGENTS.md"), tracked, 0o644); err != nil {
		t.Fatal(err)
	}

	initialized := runHandEnv(t, home, []string{"HAND_HARNESS=codex"}, "init")
	if initialized.code != 0 {
		t.Fatalf("hand init: exit %d, stderr %q", initialized.code, initialized.stderr)
	}
	if !strings.Contains(initialized.stdout, "agents_md: unchanged") {
		t.Fatalf("init stdout = %q, want tracked AGENTS.md already current", initialized.stdout)
	}
	after, err := os.ReadFile(filepath.Join(home, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(tracked) {
		t.Fatal("hand init changed the copied tracked AGENTS.md")
	}

	session := runHandEnv(t, home, []string{"HAND_HARNESS=codex"}, "session", "start")
	if session.code != 0 {
		t.Fatalf("hand session start: exit %d, stderr %q", session.code, session.stderr)
	}
	if !strings.Contains(session.stdout, "session_bootstrap: complete") {
		t.Fatalf("session stdout = %q, want completed bootstrap", session.stdout)
	}
}

func TestWorkerWorktreeNeverBootstrapsSupervisor(t *testing.T) {
	home := newHome(t)
	worktree := filepath.Join(home, "projects", "secondhand-worktree")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	fleetBefore := snapshotTree(t, home)
	worktreeBefore := snapshotTree(t, worktree)

	got := runHandEnv(t, worktree, []string{"HAND_HOME=" + home, "HAND_ROLE=worker"}, "session", "start")
	assertInvocation(t, got, 3, "supervisor session bootstrap is unavailable when HAND_ROLE=worker")
	assertTreeUnchanged(t, home, fleetBefore)
	assertTreeUnchanged(t, worktree, worktreeBefore)
}

// The retired flag has to refuse rather than be ignored, so a script still passing it is told the
// configuration moved instead of appearing to have set something.
func TestInitRefusesTheRetiredSetupFlag(t *testing.T) {
	isolateGitConfig(t)
	got := runHand(t, t.TempDir(), "init", "--setup")
	if got.code != 2 {
		t.Fatalf("hand init --setup: exit %d, want 2", got.code)
	}
	if !strings.Contains(got.stderr, "unknown flag") {
		t.Fatalf("stderr = %q, want it to name the flag as unknown", got.stderr)
	}
}
