package cmd

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/atqamz/secondhand/internal/harness"
	"github.com/atqamz/secondhand/internal/state"
	"github.com/atqamz/secondhand/internal/store"
)

func setupSessionHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	mkFleetDirs(t, home)
	if err := initMarker(home); err != nil {
		t.Fatal(err)
	}
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
	out, _, err := executeRootForTest(t, "test", in, "session", "start")
	return out, err
}

func executeRootForTest(t *testing.T, version string, in io.Reader, args ...string) (string, string, error) {
	t.Helper()
	root := newRootCmd(version)
	root.SetArgs(args)
	var out bytes.Buffer
	var errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	if in != nil {
		root.SetIn(in)
	}
	_, err := root.ExecuteC()
	return out.String(), errOut.String(), err
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
			for _, want := range []string{path, "hand init '" + home + "'"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("err = %q, want %q", err, want)
				}
			}
		})
	}
}

func TestSessionStartRecoveryCommandQuotesFleetHomeForPOSIXShell(t *testing.T) {
	parent := t.TempDir()
	home := filepath.Join(parent, "fleet path's `printf injected`;printf injected")
	mkFleetDirs(t, home)
	writeSessionContext(t, home, "operator", "# Backlog\n")
	if err := os.Remove(filepath.Join(home, "data", "operator.md")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAND_HOME", home)
	t.Setenv("HAND_HARNESS", harness.Codex)
	t.Setenv(harness.RoleEnv, "")
	t.Chdir(parent)

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "hand"), []byte("#!/bin/sh\nprintf '%s\\n' \"$#\" \"$1\" \"$2\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := executeSessionStart(t, nil)
	assertExitCode(t, err, 3)
	message := err.Error()
	start := strings.LastIndex(message, "; run `")
	end := strings.LastIndex(message, "` to restore it")
	if start < 0 || end <= start {
		t.Fatalf("err = %q, want an executable recovery command", message)
	}
	recovery := message[start+len("; run `") : end]
	got, runErr := exec.Command("sh", "-c", recovery).CombinedOutput()
	if runErr != nil {
		t.Fatalf("run recovery %q: %v: %s", recovery, runErr, got)
	}
	want := "2\ninit\n" + home + "\n"
	if string(got) != want {
		t.Fatalf("recovery argv = %q, want %q", got, want)
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

func TestSessionOverviewsDoNotMutateFleetState(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{"session start", []string{"session", "start"}},
		{"bare hand", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			if err := initLayout(home); err != nil {
				t.Fatal(err)
			}
			seedDB, err := store.Open(home)
			if err != nil {
				t.Fatal(err)
			}
			if err := seedDB.Close(); err != nil {
				t.Fatal(err)
			}
			writeSessionContext(t, home, "operator", "# Backlog\n")
			t.Setenv("HAND_HOME", home)
			t.Setenv("HAND_HARNESS", harness.Codex)
			t.Setenv(harness.RoleEnv, "")

			db, err := store.Open(home)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.AddProject(store.Project{Name: "sqlite-project", URL: "local", Mode: "local-only"}); err != nil {
				t.Fatal(err)
			}
			if err := db.WriteTask(store.Task{ID: "sqlite-task", Project: "sqlite-project", Kind: store.KindShip}); err != nil {
				t.Fatal(err)
			}
			if err := db.SetHold(store.Hold{ID: "sqlite-hold", Kind: store.HoldKindOperator, Reason: "waiting"}); err != nil {
				t.Fatal(err)
			}
			migrated, err := db.Migrated("projects.md")
			if err != nil {
				t.Fatal(err)
			}
			if migrated {
				t.Fatal("fresh initialized home already has the project migration marker")
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(filepath.Join(home, "data", "projects.md"),
				[]byte("# Projects\n\n- legacy-project: local mode=local-only\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			legacyTask, err := json.Marshal(store.Task{ID: "legacy-task", Project: "legacy-project", Kind: store.KindShip})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, "state", "legacy-task.json"), legacyTask, 0o644); err != nil {
				t.Fatal(err)
			}

			before := snapshotFleetTree(t, home)
			out, _, err := executeRootForTest(t, "test", nil, test.args...)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"sqlite-project", "sqlite-task", "sqlite-hold"} {
				if !strings.Contains(out, want) {
					t.Fatalf("out = %q, want current SQLite-backed %q", out, want)
				}
			}
			after := snapshotFleetTree(t, home)
			if !slices.Equal(after, before) {
				t.Fatalf("fleet tree changed:\nbefore: %v\nafter:  %v", before, after)
			}
		})
	}
}

func TestOldFleetRequiresExplicitRecoveryBeforeReadOnlyOverview(t *testing.T) {
	home := t.TempDir()
	if err := initLayout(home); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	downgradeSessionStore(t, home)
	writeSessionContext(t, home, "operator", "# Backlog\n")
	if err := os.WriteFile(filepath.Join(home, "data", "projects.md"),
		[]byte("# Projects\n\n- legacy-project: local mode=local-only\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	legacyTask, err := json.Marshal(store.Task{ID: "legacy-task", Project: "legacy-project", Kind: store.KindShip})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "state", "legacy-task.json"), legacyTask, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAND_HOME", home)
	t.Setenv("HAND_HARNESS", harness.Codex)
	t.Setenv(harness.RoleEnv, "")

	beforeRecovery := snapshotFleetTree(t, home)
	remedy := "hand init '" + home + "'"
	for _, args := range [][]string{{"session", "start"}, nil} {
		_, _, err = executeRootForTest(t, "test", nil, args...)
		if err == nil {
			t.Fatalf("overview %q opened an older schema read-only", args)
		}
		if !strings.Contains(err.Error(), remedy) || strings.Contains(err.Error(), "hand update") {
			t.Errorf("err = %q, want only the exact recovery %q", err, remedy)
		}
		if afterRefusal := snapshotFleetTree(t, home); !slices.Equal(afterRefusal, beforeRecovery) {
			t.Fatalf("read-only refusal changed the fleet:\nbefore: %v\nafter:  %v", beforeRecovery, afterRefusal)
		}
	}

	if _, _, err := executeRootForTest(t, "test", nil, "init", home); err != nil {
		t.Fatalf("run advertised recovery %q: %v", remedy, err)
	}
	afterRecovery := snapshotFleetTree(t, home)
	for i, args := range [][]string{{"session", "start"}, nil} {
		out, _, err := executeRootForTest(t, "test", nil, args...)
		if err != nil {
			t.Fatalf("overview %d: %v", i+1, err)
		}
		for _, want := range []string{"legacy-project", "legacy-task"} {
			if !strings.Contains(out, want) {
				t.Fatalf("overview %d = %q, want migrated %q", i+1, out, want)
			}
		}
		if afterOverview := snapshotFleetTree(t, home); !slices.Equal(afterOverview, afterRecovery) {
			t.Fatalf("overview %d mutated the recovered fleet:\nbefore: %v\nafter:  %v", i+1, afterRecovery, afterOverview)
		}
	}
}

func downgradeSessionStore(t *testing.T, home string) {
	t.Helper()
	db, err := sql.Open("sqlite", store.Path(home))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, column := range []string{
		"send_undelivered_message", "send_undelivered_at", "lease_id", "delivered_at",
		"delivered_reason", "pane_started_at", "parked_fired_for", "report_digest",
		"usage_limit_retry_at", "usage_limit_attempts",
	} {
		if _, err := db.Exec("ALTER TABLE task DROP COLUMN " + column); err != nil {
			t.Fatalf("drop task.%s: %v", column, err)
		}
	}
	if _, err := db.Exec("ALTER TABLE project DROP COLUMN upstream"); err != nil {
		t.Fatalf("drop project.upstream: %v", err)
	}
	if _, err := db.Exec("PRAGMA user_version = 0"); err != nil {
		t.Fatal(err)
	}
}

func snapshotFleetTree(t *testing.T, root string) []string {
	t.Helper()
	var snapshot []string
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entry := rel + " " + info.Mode().String()
		switch {
		case info.Mode().IsRegular():
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entry += fmt.Sprintf(" %x", sha256.Sum256(contents))
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			entry += " -> " + target
		}
		snapshot = append(snapshot, entry)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func writeFreshVersionCheck(t *testing.T, home string) {
	t.Helper()
	contents := `{"checked_at":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","latest":"v9.0.0"}`
	if err := os.WriteFile(filepath.Join(home, "state", ".version-check"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSessionOverviewsSkipReleasedVersionCheck(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{"session start", []string{"session", "start"}},
		{"bare hand", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := setupSessionHome(t)
			writeFreshVersionCheck(t, home)

			_, errOut, err := executeRootForTest(t, "v0.1.0", nil, test.args...)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(errOut, "A new version of hand is available") {
				t.Fatalf("stderr = %q, want the read-only overview to skip the version-check path", errOut)
			}
		})
	}
}

func TestSessionStartWorkerRefusalPrecedesReleasedVersionCheck(t *testing.T) {
	home := setupSessionHome(t)
	writeFreshVersionCheck(t, home)
	t.Setenv(harness.RoleEnv, harness.WorkerRole)

	_, errOut, err := executeRootForTest(t, "v0.1.0", nil, "session", "start")
	assertExitCode(t, err, 3)
	if errOut != "" {
		t.Fatalf("stderr before worker refusal = %q, want no version-check output", errOut)
	}
}

func TestNormalCommandKeepsReleasedVersionNotice(t *testing.T) {
	home := setupSessionHome(t)
	writeFreshVersionCheck(t, home)

	_, errOut, err := executeRootForTest(t, "v0.1.0", nil, "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut, "A new version of hand is available: v0.1.0 -> v9.0.0") {
		t.Fatalf("stderr = %q, want normal commands to retain the released-version notice", errOut)
	}
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
