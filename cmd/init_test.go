package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atqamz/secondhand/internal/home"
)

func TestInitCreatesTheHandDbMarker(t *testing.T) {
	t.Setenv("HAND_HOME", "")
	dir := t.TempDir()
	t.Chdir(dir)

	cmd := newInitCmd()
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "state", "hand.db")); err != nil {
		t.Fatalf("state/hand.db missing after init: %v", err)
	}
	ok, err := home.IsHome(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("got IsHome false right after init, want true")
	}
}

func TestInitSeedsEveryDataSkeletonFile(t *testing.T) {
	t.Setenv("HAND_HOME", "")
	dir := t.TempDir()
	t.Chdir(dir)

	cmd := newInitCmd()
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"data/backlog.md":      "# Backlog",
		"data/projects.md":     "# Projects",
		"data/operator.md":     "## Hard constraints",
		"data/learnings.md":    "# Learnings",
		"data/done-archive.md": "# Done archive",
		"data/note-archive.md": "# Note archive",
	}
	for rel, header := range want {
		got, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			t.Fatalf("%s missing after init: %v", rel, err)
		}
		if !strings.Contains(string(got), header) {
			t.Fatalf("%s = %q, want it to contain %q", rel, got, header)
		}
	}
}

// A home initialized before the layout gained a file picks the file up by
// re-running init, which must never cost it the content of one it already has.
func TestInitLeavesExistingDataFilesAlone(t *testing.T) {
	t.Setenv("HAND_HOME", "")
	dir := t.TempDir()
	t.Chdir(dir)

	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := "# Operator\n\n## Authority\n\nMerge without asking.\n"
	if err := os.WriteFile(filepath.Join(dir, "data", "operator.md"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newInitCmd()
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "data", "operator.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != existing {
		t.Fatalf("data/operator.md = %q, want unchanged %q", got, existing)
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "learnings.md")); err != nil {
		t.Fatalf("data/learnings.md missing: %v", err)
	}
}

// Whoever hits a seeding failure reads the message to decide where to look, so
// it names every file that failed and says the same thing on every run.
func TestInitSkeletonFilesReportsEveryFailureInAStableOrder(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	first := initSkeletonFiles(dir)
	if first == nil {
		t.Fatal("got nil, want an error when data/ is not a directory")
	}
	second := initSkeletonFiles(dir)
	if second == nil || second.Error() != first.Error() {
		t.Fatalf("run 2 = %v, want the same message as run 1 %v", second, first)
	}

	for _, rel := range []string{"data/backlog.md", "data/projects.md", "data/operator.md", "data/learnings.md", "data/done-archive.md", "data/note-archive.md"} {
		if !strings.Contains(first.Error(), rel) {
			t.Fatalf("got %q, want it to name %s", first, rel)
		}
	}
}

func TestInitIsIdempotentAboutTheHandDbMarker(t *testing.T) {
	t.Setenv("HAND_HOME", "")
	dir := t.TempDir()
	t.Chdir(dir)

	for i := 0; i < 2; i++ {
		cmd := newInitCmd()
		cmd.SetArgs([]string{})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, "state", "hand.db")); err != nil {
		t.Fatalf("state/hand.db missing after repeat init: %v", err)
	}
}

// Bootstrap answers nothing on the operator's behalf, so a fresh home leaves every worker default
// unwritten and hands the questions to the session that reads this document.
func TestInitWritesNoWorkerDefaultAndReportsWhatIsMissing(t *testing.T) {
	t.Setenv("HAND_HOME", "")
	t.Setenv("HAND_HARNESS", "unknown")
	dir := t.TempDir()
	t.Chdir(dir)

	cmd := newInitCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Join(dir, "config"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Fatalf("init wrote config/%s, want no worker default chosen for the operator", e.Name())
	}
	for _, want := range []string{
		"config_missing: 1\n",
		"harness,missing,none",
		"model,pending-harness,none",
		"hand config set <key> <value>",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("init output = %q, want it to contain %q", out.String(), want)
		}
	}
}

// The retired flag has to fail loudly rather than be silently accepted, so a script still passing it
// is told the configuration moved instead of appearing to have set something.
func TestInitRefusesTheRetiredSetupFlag(t *testing.T) {
	t.Setenv("HAND_HOME", "")
	t.Chdir(t.TempDir())

	root := newRootCmd("test")
	root.SetOut(new(bytes.Buffer))
	root.SetErr(new(bytes.Buffer))
	root.SetArgs([]string{"init", "--setup"})
	_, err := root.ExecuteC()
	if code := exitCodeFor(t, err); code != 2 {
		t.Fatalf("code = %d, want 2 (err = %v)", code, err)
	}
	if !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("err = %v, want it to name the flag as unknown", err)
	}
}

// Init runs in scripts and in CI, where stdin is closed or never written to. An open pipe nothing
// writes to is the shape that hangs, so the read has to be absent rather than merely unreached.
func TestInitNeverReadsStdin(t *testing.T) {
	t.Setenv("HAND_HOME", "")
	t.Chdir(t.TempDir())

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	stdin := os.Stdin
	os.Stdin = reader
	t.Cleanup(func() { os.Stdin = stdin })

	done := make(chan error, 1)
	go func() {
		cmd := newInitCmd()
		cmd.SetIn(reader)
		cmd.SetOut(new(bytes.Buffer))
		cmd.SetArgs([]string{})
		done <- cmd.Execute()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("init did not finish with nothing to read, want it never to block on stdin")
	}
}

func TestInitInstallsTheSessionHookAndSaysSoOnlyWhenItWroteIt(t *testing.T) {
	t.Setenv("HAND_HOME", "")
	dir := t.TempDir()
	t.Chdir(dir)

	for i, want := range []string{"session_hook: written\n", "session_hook: unchanged\n"} {
		cmd := newInitCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
		if !strings.Contains(out.String(), want) {
			t.Fatalf("run %d output = %q, want it to contain %q", i+1, out.String(), want)
		}
	}

	settings, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(settings), "SessionStart") {
		t.Fatalf("settings = %q, want a SessionStart hook in it", settings)
	}
}
