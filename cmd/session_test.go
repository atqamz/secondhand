package cmd

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atqamz/secondhand/internal/harness"
	"github.com/atqamz/secondhand/internal/state"
	"github.com/atqamz/secondhand/internal/store"
)

func setupSessionHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	mkFleetDirs(t, home)
	t.Chdir(home)
	t.Setenv("HAND_HARNESS", harness.Codex)
	t.Setenv(harness.RoleEnv, "")
	writeSessionContext(t, home, "## Hard constraints\nKeep the fleet observable.\n", "# Backlog\n\n## Queue\n")
	return home
}

func writeSessionContext(t *testing.T, home, operator, backlog string) {
	t.Helper()
	for name, contents := range map[string]string{
		"operator.md": operator,
		"backlog.md":  backlog,
	} {
		if err := os.WriteFile(filepath.Join(home, "data", name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func executeSessionStart(t *testing.T, in io.Reader) (string, error) {
	t.Helper()
	root := newRootCmd("test")
	root.SetArgs([]string{"session", "start"})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(new(bytes.Buffer))
	if in != nil {
		root.SetIn(in)
	}
	_, err := root.ExecuteC()
	return out.String(), err
}

func runSessionStartForTest(t *testing.T) string {
	t.Helper()
	out, err := executeSessionStart(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSessionStartEmitsCompleteBoundedDigest(t *testing.T) {
	home := setupSessionHome(t)
	writeSessionContext(t, home,
		"## Hard constraints\nKeep every line.\nIncluding: punctuation.\n",
		"# Backlog\n\n## Queue\n- queued-task\n  private implementation body\n")

	out := runSessionStartForTest(t)
	for _, want := range []string{
		"session_bootstrap: complete\n",
		"tool: hand\n",
		"version: test\n",
		"exec:",
		"home: " + home + "\n",
		"supervisor_harness: codex\n",
		"supervisor_harness_source: override\n",
		"harness,detected,codex",
		"model,native-default,none",
		"operator: \"## Hard constraints\\nKeep every line.\\nIncluding: punctuation.\"\n",
		"instructions[",
		"projects[0]{name,mode,url,upstream}:\n",
		"backlog[2]:\n",
		"count: 0\n",
		"attention: 0\n",
		"held: 0\n",
		"tasks[0]{id,state,reported,age,flags}:\n",
		"holds[0]{id,kind,detail,age}:\n",
		"help[1]:\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("out = %q, want %q", out, want)
		}
	}
	if strings.Contains(out, "private implementation body") {
		t.Fatalf("out = %q, want indented backlog bodies omitted", out)
	}
}

func TestSessionStartRefusesWorkerRoleBeforeReadingContext(t *testing.T) {
	home := setupSessionHome(t)
	if err := os.Remove(filepath.Join(home, "data", "operator.md")); err != nil {
		t.Fatal(err)
	}
	t.Setenv(harness.RoleEnv, harness.WorkerRole)

	_, err := executeSessionStart(t, nil)
	assertExitCode(t, err, 3)
	if want := "supervisor session bootstrap is unavailable when HAND_ROLE=worker"; !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %q, want %q", err, want)
	}
}

func TestSessionStartRefusesOutsideFleetHome(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HAND_HOME", "")
	t.Setenv("HAND_HARNESS", harness.Codex)
	t.Setenv(harness.RoleEnv, "")

	_, err := executeSessionStart(t, nil)
	assertExitCode(t, err, 3)
	if !strings.Contains(err.Error(), "run `hand init`") {
		t.Fatalf("err = %q, want the supported hand init remedy", err)
	}
}

func TestSessionStartMissingRequiredContextNamesPathAndRecovery(t *testing.T) {
	for _, name := range []string{"operator.md", "backlog.md"} {
		t.Run(name, func(t *testing.T) {
			home := setupSessionHome(t)
			path := filepath.Join(home, "data", name)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}

			_, err := executeSessionStart(t, nil)
			assertExitCode(t, err, 3)
			for _, want := range []string{path, "hand init " + home} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("err = %q, want %q", err, want)
				}
			}
		})
	}
}

func TestSessionStartPreservesFleetReaderErrorOwnership(t *testing.T) {
	home := setupSessionHome(t)
	if err := os.Remove(store.Path(home)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(store.Path(home), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := executeSessionStart(t, nil)
	if err == nil {
		t.Fatal("got nil, want the project/state store error")
	}
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		t.Fatalf("err = %v, want the owning fleet reader's general error, not ExitError", err)
	}
}

type readFunc func([]byte) (int, error)

func (f readFunc) Read(p []byte) (int, error) { return f(p) }

func TestSessionStartNeverReadsStdin(t *testing.T) {
	setupSessionHome(t)
	read := false
	in := readFunc(func([]byte) (int, error) {
		read = true
		return 0, errors.New("stdin must not be read")
	})

	if _, err := executeSessionStart(t, in); err != nil {
		t.Fatal(err)
	}
	if read {
		t.Fatal("session start read stdin")
	}
}

func TestSessionOverviewsDoNotMigrateLegacyConfig(t *testing.T) {
	home := setupSessionHome(t)
	configDir := filepath.Join(home, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"harness": harness.Codex, "model": "legacy-model"} {
		if err := os.WriteFile(filepath.Join(configDir, name), []byte(value+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	assertUnmigrated := func() {
		t.Helper()
		if _, err := os.Stat(filepath.Join(configDir, "model")); err != nil {
			t.Fatalf("legacy model was moved: %v", err)
		}
		if _, err := os.Stat(filepath.Join(configDir, "model.codex")); !os.IsNotExist(err) {
			t.Fatalf("keyed model stat error = %v, want not-exist", err)
		}
	}

	if _, err := executeSessionStart(t, nil); err != nil {
		t.Fatal(err)
	}
	assertUnmigrated()
	runBareRoot(t)
	assertUnmigrated()
}

func TestReadBacklogSummaryBoundsIdentityLinesAndCountsTheWholeQueue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backlog.md")
	contents := "# Backlog\n## Queue\n- first\n  hidden detail\n* second\n- third\n### hidden subsection\n## Notes\n- note\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := readBacklogSummary(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"## Queue", "- first", "* second"}
	if len(got.Items) != 4 {
		t.Fatalf("items = %#v, want three identity lines plus one recovery line", got.Items)
	}
	for i := range want {
		if got.Items[i] != want[i] {
			t.Fatalf("items = %#v, want prefix %#v", got.Items, want)
		}
	}
	if last := got.Items[len(got.Items)-1]; !strings.Contains(last, "truncated") || !strings.Contains(last, "data/backlog.md") {
		t.Fatalf("last item = %q, want truncation and recovery path", last)
	}
	if got.Queued != 3 {
		t.Fatalf("queued = %d, want all three queued items counted beyond the output bound", got.Queued)
	}
}

func TestSessionNextActionUsesExactPriority(t *testing.T) {
	unknownConfig := workerConfig{}
	detectedConfig := workerConfig{harness: harness.Codex}
	tests := []struct {
		name     string
		cfg      workerConfig
		projects int
		backlog  backlogSummary
		views    []taskView
		holds    []state.Hold
		want     string
	}{
		{"unknown harness", unknownConfig, 0, backlogSummary{}, nil, nil, "hand config set harness"},
		{"attention before hold", detectedConfig, 1, backlogSummary{}, []taskView{{task: state.Task{ID: "x"}, unacked: true}}, []state.Hold{{ID: "y"}}, "hand status x"},
		{"hold before no projects", detectedConfig, 0, backlogSummary{}, nil, []state.Hold{{ID: "x"}}, "hand status x"},
		{"first project", detectedConfig, 0, backlogSummary{}, nil, nil, "hand project add"},
		{"queued work", detectedConfig, 1, backlogSummary{Queued: 1}, nil, nil, "prepare the queued task"},
		{"active workers", detectedConfig, 1, backlogSummary{}, []taskView{{task: state.Task{ID: "x"}}}, nil, "hand watch --until-event"},
		{"idle", detectedConfig, 1, backlogSummary{}, nil, nil, "fleet is ready and idle"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := sessionNextAction(test.cfg, test.projects, test.backlog, test.views, test.holds)
			if !strings.Contains(got, test.want) {
				t.Fatalf("action = %q, want it to contain %q", got, test.want)
			}
		})
	}
}
