//go:build e2e

// Package e2e drives the built hand binary against a real temp home. It is the
// place for tests that exercise hand end-to-end rather than through cmd package
// internals; extend it rather than building a second harness. The e2e build tag
// keeps it out of `make test`; `make e2e` runs it.
package e2e

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/atqamz/secondhand/internal/state"
)

var handBin string

// The tools hand shells out to that this suite never runs for real. None of them may resolve on the
// hermetic PATH: a real one would silently answer in place of a test's fake, so the affected test would
// pass against reality instead of failing.
var backendsThisSuiteFakes = []string{"herdr", "treehouse", "gh", "no-mistakes"}

// Checks that invariant once, after TestMain has narrowed PATH to the fixture PATH and before any test
// runs, so a change that widens realBinsOnPath (or hands a whole directory to buildHermeticPath) fails the
// run loudly instead of quietly re-admitting a real backend.
func assertNoAmbientBackends() error {
	for _, name := range backendsThisSuiteFakes {
		if path, err := exec.LookPath(name); err == nil {
			return fmt.Errorf("%s resolves to %s on this suite's hermetic PATH; "+
				"this suite fakes %s and must not find a real one", name, path, name)
		}
	}
	return nil
}

// Builds the hand binary once for the whole package, then replaces PATH with a hermetic one (see
// fakes_test.go's buildHermeticPath) so neither the tests nor the hand processes they drive can reach a
// real herdr, treehouse or gh that happens to be installed.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "hand-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	handBin = filepath.Join(dir, "hand")
	// go test's result cache is keyed on this package's own inputs, not on this nested go build, so changing
	// production code alone will not invalidate a cached e2e run - pass -count=1 when checking red/green
	// behavior after a production-code-only edit.
	build := exec.Command("go", "build", "-o", handBin, ".")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build hand: %v: %s\n", err, out)
		os.Exit(1)
	}

	hermeticPath, err = buildHermeticPath(filepath.Join(dir, "path"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Setenv("PATH", hermeticPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := assertNoAmbientBackends(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Every hand process this suite drives inherits this environment and resolves HAND_HOME ahead of its
	// working directory, so a developer who exported one would otherwise have the suite spawn, merge and tear
	// down against their real fleet.
	if err := os.Unsetenv("HAND_HOME"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

type invocation struct {
	code   int
	stdout string
	stderr string
}

func runHand(t *testing.T, home string, args ...string) invocation {
	t.Helper()
	return runHandEnv(t, home, nil, args...)
}

func handProcessEnv(extraEnv ...string) []string {
	overridden := map[string]bool{"HAND_ROLE": true}
	for _, entry := range extraEnv {
		if name, _, ok := strings.Cut(entry, "="); ok {
			overridden[strings.ToUpper(name)] = true
		}
	}
	env := make([]string, 0, len(os.Environ())+len(extraEnv))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !overridden[strings.ToUpper(name)] {
			env = append(env, entry)
		}
	}
	return append(env, extraEnv...)
}

// Runs hand with explicit entries replacing ambient values; HAND_ROLE is absent unless a worker test
// opts in, so the suite's own role never changes the child being exercised.
func runHandEnv(t *testing.T, home string, extraEnv []string, args ...string) invocation {
	t.Helper()
	cmd := exec.Command(handBin, args...)
	cmd.Dir = home
	cmd.Env = handProcessEnv(extraEnv...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	code := 0
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run hand %v: %v", args, err)
		}
		code = exitErr.ExitCode()
	}
	t.Logf("$ hand %s\n  exit %d\n  stdout: %s\n  stderr: %s",
		strings.Join(args, " "), code, strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()))
	return invocation{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// Lets a test goroutine poll a background hand process's output while the process is still writing to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// Drives a long-running hand command (only `watch` today) so a test can observe its streaming output and
// stop it with the same SIGTERM signal.NotifyContext listens for in cmd/watch.go, rather than waiting for
// it to exit on its own.
type backgroundHand struct {
	cmd     *exec.Cmd
	args    []string
	stdout  *syncBuffer
	stderr  *syncBuffer
	reaping bool
}

func startHandBackground(t *testing.T, home string, args ...string) *backgroundHand {
	t.Helper()
	cmd := exec.Command(handBin, args...)
	cmd.Dir = home
	cmd.Env = handProcessEnv()
	stdout := &syncBuffer{}
	stderr := &syncBuffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start hand %v: %v", args, err)
	}
	b := &backgroundHand{cmd: cmd, args: args, stdout: stdout, stderr: stderr}
	t.Cleanup(func() {
		if b.reaping {
			return
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return b
}

func (b *backgroundHand) waitForStdout(t *testing.T, substr string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(b.stdout.String(), substr) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q on stdout; stdout=%q stderr=%q", substr, b.stdout.String(), b.stderr.String())
}

// Sends SIGTERM (the signal cmd/watch.go's signal.NotifyContext listens for) and waits for a clean exit,
// failing the test if the process doesn't exit within timeout.
func (b *backgroundHand) stop(t *testing.T, timeout time.Duration) invocation {
	t.Helper()
	if err := b.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal hand watch: %v", err)
	}
	return b.waitForExit(t, timeout, "SIGTERM")
}

// Reaps a process expected to exit on its own, unlike stop: that exit is the whole delivery mechanism of
// `hand watch --until-event`, so it has to be observed rather than caused by a signal.
func (b *backgroundHand) waitForExit(t *testing.T, timeout time.Duration, because string) invocation {
	t.Helper()
	b.reaping = true
	done := make(chan error, 1)
	go func() { done <- b.cmd.Wait() }()
	select {
	case err := <-done:
		code := 0
		if err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("wait hand watch: %v", err)
			}
			code = exitErr.ExitCode()
		}
		got := invocation{code: code, stdout: b.stdout.String(), stderr: b.stderr.String()}
		t.Logf("$ hand %s\n  exit %d after %s\n  stdout: %s\n  stderr: %s",
			strings.Join(b.args, " "), got.code, because, strings.TrimSpace(got.stdout), strings.TrimSpace(got.stderr))
		return got
	case <-time.After(timeout):
		_ = b.cmd.Process.Kill()
		t.Fatalf("hand watch did not exit within %s of %s; stdout=%q stderr=%q", timeout, because, b.stdout.String(), b.stderr.String())
		return invocation{}
	}
}

// Blocks until a fake binary's invocation log contains substr, giving a test a positive signal that a
// background process has actually reached a given call instead of guessing with a sleep.
func waitForInvocation(t *testing.T, logPath, substr string, timeout time.Duration) {
	t.Helper()
	waitForInvocations(t, logPath, substr, 1, timeout)
}

func waitForInvocations(t *testing.T, logPath, substr string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(logPath)
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("read invocation log %s: %v", logPath, err)
		}
		if strings.Count(string(data), substr) >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	data, _ := os.ReadFile(logPath)
	t.Fatalf("timed out waiting for %d occurrences of %q in invocation log; log=%q", want, substr, data)
}

func newHome(t *testing.T) string {
	t.Helper()
	isolateGitConfig(t)
	home := t.TempDir()
	if got := runHand(t, home, "init"); got.code != 0 {
		t.Fatalf("hand init: exit %d, stderr %q", got.code, got.stderr)
	}
	return home
}

func registerProject(t *testing.T, home, name, mode string) {
	t.Helper()
	line := fmt.Sprintf("- %s: https://example.com/%s.git mode=%s\n", name, name, mode)
	f, err := os.OpenFile(filepath.Join(home, "data", "projects.md"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
}

func writeConfig(t *testing.T, home, name, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config", name), []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A worker default is keyed by the harness it was chosen for, so a test declares one the way an operator
// does rather than writing config/ by hand and getting a file nothing reads.
func handConfigSet(t *testing.T, home, key, value string) {
	t.Helper()
	if got := runHand(t, home, "config", "set", key, value); got.code != 0 {
		t.Fatalf("hand config set %s %s: exit %d, stderr %q", key, value, got.code, got.stderr)
	}
}

func writeBrief(t *testing.T, home, id string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, "data", id), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "data", id, "brief.md"), []byte("# brief\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A failure arrives as a TOON document whose message field is quoted whenever
// it carries a character a reader would otherwise have to guess at, so an
// assertion about what an error says reads the field rather than the stream.
func errorMessage(t *testing.T, stderr string) string {
	t.Helper()
	for _, line := range strings.Split(stderr, "\n") {
		value, ok := strings.CutPrefix(line, "error: ")
		if !ok {
			continue
		}
		if !strings.HasPrefix(value, `"`) {
			return value
		}
		unquoted, err := strconv.Unquote(value)
		if err != nil {
			t.Fatalf("error field %q does not unquote: %v", value, err)
		}
		return unquoted
	}
	t.Fatalf("stderr = %q, want an error field in it", stderr)
	return ""
}

func assertInvocation(t *testing.T, got invocation, wantCode int, wantStderr string) {
	t.Helper()
	if got.code != wantCode {
		t.Fatalf("exit = %d, want %d (stderr %q, stdout %q)", got.code, wantCode, got.stderr, got.stdout)
	}
	if wantCode == 0 {
		if wantStderr != "" && !strings.Contains(got.stderr, wantStderr) {
			t.Fatalf("stderr = %q, want it to contain %q", got.stderr, wantStderr)
		}
		return
	}
	if msg := errorMessage(t, got.stderr); wantStderr != "" && !strings.Contains(msg, wantStderr) {
		t.Fatalf("error = %q, want it to contain %q", msg, wantStderr)
	}
	if want := fmt.Sprintf("\nexit: %d\n", wantCode); !strings.Contains(got.stderr, want) {
		t.Fatalf("stderr = %q, want it to report %q", got.stderr, want)
	}
	if strings.TrimSpace(got.stdout) != "" {
		t.Fatalf("stdout = %q, want errors on stderr only", got.stdout)
	}
}

func TestExitCodeZeroOnSuccess(t *testing.T) {
	home := newHome(t)
	registerProject(t, home, "demo", "direct-pr")

	for _, args := range [][]string{{"--version"}, {"init"}, {"project", "list"}, {"project"}, {"--help"}} {
		got := runHand(t, home, args...)
		if got.code != 0 {
			t.Fatalf("hand %v: exit = %d, want 0 (stderr %q)", args, got.code, got.stderr)
		}
		if strings.TrimSpace(got.stdout) == "" {
			t.Fatalf("hand %v: stdout empty, want structured output on stdout", args)
		}
	}
}

// The bare command is the one surface that has to introduce the binary and
// report the fleet at once, and only the real binary can say which executable
// answered.
func TestBareCommandNamesItselfAndReportsTheFleet(t *testing.T) {
	home := newHome(t)
	got := runHand(t, home)
	if got.code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", got.code, got.stderr)
	}
	for _, want := range []string{"tool: hand\n", "version: ", "exec: ", "home: ", "count: 0\n", "tasks[0]{"} {
		if !strings.Contains(got.stdout, want) {
			t.Fatalf("stdout = %q, want it to contain %q", got.stdout, want)
		}
	}
	if !strings.Contains(got.stdout, handBin) {
		t.Fatalf("stdout = %q, want it to name the executable that answered (%s)", got.stdout, handBin)
	}
}

func TestExitCodeTwoOnUsageError(t *testing.T) {
	home := newHome(t)

	cases := []struct {
		name       string
		args       []string
		wantStderr string
	}{
		{"unknown command", []string{"bogus-command"}, `unknown command "bogus-command"`},
		{"unknown project subcommand", []string{"project", "bogus"}, `unknown command "bogus"`},
		{"unknown completion subcommand", []string{"completion", "bogus"}, `unknown command "bogus"`},
		{"too few args", []string{"spawn", "only-one"}, "accepts 2 arg"},
		{"too many args", []string{"teardown", "task-1", "extra"}, "accepts 1 arg"},
		{"args on argless command", []string{"watch", "extra"}, "unknown command"},
		{"unknown flag", []string{"spawn", "--bogus", "task-1", "demo"}, "unknown flag: --bogus"},
		{"conflicting merge methods", []string{"merge", "task-1", "--squash", "--rebase"}, "only one of --squash, --merge, --rebase"},
		{"merge method with local", []string{"merge", "task-1", "--local", "--squash"}, "cannot be combined with --local"},
		{"invalid project URL", []string{"project", "add", "not-a-url"}, "invalid project URL"},
		{"invalid project mode", []string{"project", "add", "https://example.com/demo.git", "--mode", "bogus"}, "invalid project mode"},
		{"invalid poll interval", []string{"watch", "--poll", "nonsense"}, "invalid poll interval"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertInvocation(t, runHand(t, home, tc.args...), 2, tc.wantStderr)
		})
	}
}

func TestExitCodeThreeOnPreconditionFailure(t *testing.T) {
	cases := []struct {
		name       string
		setup      func(t *testing.T, home string)
		args       []string
		wantStderr string
	}{
		{
			name:       "task not found",
			args:       []string{"status", "nosuch"},
			wantStderr: `task "nosuch" not found`,
		},
		{
			name:       "teardown unknown task",
			args:       []string{"teardown", "nosuch"},
			wantStderr: `task "nosuch" not found`,
		},
		{
			name:       "merge unknown task",
			args:       []string{"merge", "nosuch"},
			wantStderr: `task "nosuch" not found`,
		},
		{
			name:       "promote unknown task",
			args:       []string{"promote", "nosuch"},
			wantStderr: `task "nosuch" not found`,
		},
		{
			name:       "send to unknown task",
			args:       []string{"send", "nosuch", "hello"},
			wantStderr: `task "nosuch" not found`,
		},
		{
			name:       "spawn on unregistered project",
			args:       []string{"spawn", "task-1", "nosuch"},
			wantStderr: `project "nosuch" not registered`,
		},
		{
			name:       "project sync unregistered",
			args:       []string{"project", "sync", "nosuch"},
			wantStderr: `project "nosuch" not registered`,
		},
		{
			name:       "project remove unregistered",
			args:       []string{"project", "remove", "nosuch"},
			wantStderr: `project "nosuch" not registered`,
		},
		{
			name:       "spawn without brief",
			setup:      func(t *testing.T, home string) { registerProject(t, home, "demo", "direct-pr") },
			args:       []string{"spawn", "task-1", "demo"},
			wantStderr: "brief not found at data/task-1/brief.md",
		},
		{
			name: "project add already registered",
			setup: func(t *testing.T, home string) {
				registerProject(t, home, "demo", "direct-pr")
			},
			args:       []string{"project", "add", "https://example.com/demo.git"},
			wantStderr: `project "demo" already registered`,
		},
		{
			name: "project remove with active task",
			setup: func(t *testing.T, home string) {
				registerProject(t, home, "demo", "direct-pr")
				if err := state.Write(home, state.Task{ID: "task-1", Project: "demo", Kind: state.KindShip}); err != nil {
					t.Fatal(err)
				}
			},
			args:       []string{"project", "remove", "demo"},
			wantStderr: `project "demo" has active tasks referencing it`,
		},
		{
			name: "teardown scout without report",
			setup: func(t *testing.T, home string) {
				registerProject(t, home, "demo", "direct-pr")
				if err := state.Write(home, state.Task{ID: "task-1", Project: "demo", Kind: state.KindScout}); err != nil {
					t.Fatal(err)
				}
			},
			args:       []string{"teardown", "task-1"},
			wantStderr: "report not found at data/task-1/report.md",
		},
		{
			name: "teardown ship without landed work",
			setup: func(t *testing.T, home string) {
				registerProject(t, home, "demo", "direct-pr")
				worktree := filepath.Join(home, "wt")
				initGitRepo(t, worktree)
				if err := state.Write(home, state.Task{ID: "task-1", Project: "demo", Kind: state.KindShip, Worktree: worktree}); err != nil {
					t.Fatal(err)
				}
			},
			args:       []string{"teardown", "task-1"},
			wantStderr: "no PR recorded for task-1",
		},
		{
			name: "promote a task that is not a scout",
			setup: func(t *testing.T, home string) {
				registerProject(t, home, "demo", "direct-pr")
				if err := state.Write(home, state.Task{ID: "task-1", Project: "demo", Kind: state.KindShip}); err != nil {
					t.Fatal(err)
				}
			},
			args:       []string{"promote", "task-1"},
			wantStderr: `task "task-1" is not a scout`,
		},
		{
			name: "merge an already merged task",
			setup: func(t *testing.T, home string) {
				registerProject(t, home, "demo", "direct-pr")
				if err := state.Write(home, state.Task{ID: "task-1", Project: "demo", Kind: state.KindShip, MergeExecuted: true}); err != nil {
					t.Fatal(err)
				}
			},
			args:       []string{"merge", "task-1"},
			wantStderr: "task task-1 already merged",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := newHome(t)
			if tc.setup != nil {
				tc.setup(t, home)
			}
			assertInvocation(t, runHand(t, home, tc.args...), 3, tc.wantStderr)
		})
	}
}

// Every command routes home.Resolve's failure through asPrecondition, so this
// covers that wiring for all of them at once: a regression downgrades the
// refusal to a plain exit 1.
func TestExitCodeThreeOutsideAnyFleetHome(t *testing.T) {
	assertInvocation(t, runHand(t, t.TempDir(), "status"), 3,
		"not inside a secondhand home; run `hand init` or set HAND_HOME")
}

// A HAND_HOME pointing at a directory that is not a fleet home refuses rather
// than silently falling back to the working directory, which here is a real
// home the fallback would have found.
func TestExitCodeThreeWhenHandHomeIsNotAFleetHome(t *testing.T) {
	home := newHome(t)
	notAHome := t.TempDir()

	got := runHandEnv(t, home, []string{"HAND_HOME=" + notAHome}, "status")
	assertInvocation(t, got, 3, fmt.Sprintf("HAND_HOME %q is not a secondhand home", notAHome))
}

func TestExitCodeOneOnGeneralError(t *testing.T) {
	cases := []struct {
		name       string
		config     map[string]string
		args       []string
		wantStderr string
	}{
		{
			name:       "malformed watch-interval config",
			config:     map[string]string{"watch-interval": "nonsense"},
			args:       []string{"watch"},
			wantStderr: "invalid poll interval",
		},
		{
			name:       "malformed stale-threshold config",
			config:     map[string]string{"stale-threshold": "not-a-number"},
			args:       []string{"watch", "--poll", "1h"},
			wantStderr: "invalid config/stale-threshold",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := newHome(t)
			for name, value := range tc.config {
				writeConfig(t, home, name, value)
			}
			assertInvocation(t, runHand(t, home, tc.args...), 1, tc.wantStderr)
		})
	}
}

// Marks the current test as already pointed at a scratch git config, so the helpers below can each demand
// isolation without clobbering an earlier setup (redirectGitRemote's insteadOf rules in particular).
const gitConfigIsolated = "HAND_E2E_GIT_CONFIG_ISOLATED"

// Points every git invocation in this test - the test's own and the ones hand shells out to - at a scratch
// config file, so the developer's real ~/.gitconfig (commit.gpgsign above all, which would drag gpg-agent
// into every commit these tests make) can never reach them.
func isolateGitConfig(t *testing.T) string {
	t.Helper()
	if os.Getenv(gitConfigIsolated) == "1" {
		return os.Getenv("GIT_CONFIG_GLOBAL")
	}
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	content := "[user]\n\tname = hand-e2e\n\temail = hand-e2e@example.invalid\n" +
		"[commit]\n\tgpgsign = false\n[tag]\n\tgpgsign = false\n[init]\n\tdefaultBranch = main\n"
	if err := os.WriteFile(cfg, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv(gitConfigIsolated, "1")
	// Returned rather than kept private so a caller can add its own rules to the same file.
	return cfg
}

func runGitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	isolateGitConfig(t)
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v: %s", args, err, out)
	}
	return string(out)
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitIn(t, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitIn(t, dir, "add", "README.md")
	runGitIn(t, dir, "commit", "-q", "-m", "initial commit")
}
