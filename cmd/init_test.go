package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/atqamz/hand/internal/harness"
	"github.com/atqamz/hand/internal/home"
	"github.com/atqamz/hand/internal/registry"
	"github.com/atqamz/hand/internal/store"
)

func snapshotInitTarget(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		key := fmt.Sprintf("%s|%o", rel, info.Mode())
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			key += "|" + string(data)
		}
		snapshot[rel] = key
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func seedLegacyInitTarget(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data", "projects.md"), []byte("# Projects\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestInitRefusesForeignNonEmptyTargetWithoutMutation(t *testing.T) {
	t.Setenv("HAND_HOME", "")
	parent := t.TempDir()
	target := filepath.Join(parent, "foreign fleet")
	if err := os.MkdirAll(filepath.Join(target, "nested empty"), 0o751); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(target, "nested empty", "notes.txt")
	if err := os.WriteFile(foreign, []byte("do not adopt\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	before := snapshotInitTarget(t, target)

	cmd := newInitCmd()
	cmd.SetArgs([]string{target})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("init error = %v, want foreign non-empty refusal", err)
	}
	if after := snapshotInitTarget(t, target); !reflect.DeepEqual(after, before) {
		t.Fatalf("foreign target changed: before=%v after=%v", before, after)
	}
}

func TestInitRefusesAnUnsafeRecognizedTargetWithoutMutation(t *testing.T) {
	t.Setenv("HAND_HOME", "")
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(target, "state", "hand.db")
	if err := os.WriteFile(dbPath, []byte("not sqlite"), 0o640); err != nil {
		t.Fatal(err)
	}
	before := snapshotInitTarget(t, target)

	cmd := newInitCmd()
	cmd.SetArgs([]string{target})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "unsafe to reconcile") {
		t.Fatalf("init error = %v, want unsafe-state refusal", err)
	}
	if after := snapshotInitTarget(t, target); !reflect.DeepEqual(after, before) {
		t.Fatalf("unsafe target changed: before=%v after=%v", before, after)
	}
}

func TestInitAllowsHandOwnedRuntimeAlongsideTheTarget(t *testing.T) {
	t.Setenv("HAND_HOME", "")
	target := t.TempDir()
	runtimeRoot := filepath.Join(target, ".secondhand")
	if err := os.MkdirAll(runtimeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SECONDHAND_HOME", runtimeRoot)

	cmd := newInitCmd()
	cmd.SetArgs([]string{target})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("init with Hand-owned runtime: %v", err)
	}
}

func TestInitRefusesForeignContentAlongsideHandOwnedRuntime(t *testing.T) {
	t.Setenv("HAND_HOME", "")
	target := t.TempDir()
	runtimeRoot := filepath.Join(target, ".secondhand")
	if err := os.MkdirAll(runtimeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "notes.txt"), []byte("foreign\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SECONDHAND_HOME", runtimeRoot)

	cmd := newInitCmd()
	cmd.SetArgs([]string{target})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("init error = %v, want foreign content refusal", err)
	}
}

func TestInitAllowsExistingAgentsFileForHandMigration(t *testing.T) {
	t.Setenv("HAND_HOME", "")
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "AGENTS.md"), []byte("legacy instructions\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newInitCmd()
	cmd.SetArgs([]string{target})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("init with legacy AGENTS.md: %v", err)
	}
}

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

func TestInitRegistersDurableFleetIdentity(t *testing.T) {
	t.Setenv("HAND_HOME", "")
	userHome := t.TempDir()
	setTestUserHome(t, userHome)
	dir := t.TempDir()
	t.Chdir(dir)

	cmd := newInitCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	fleetID, err := db.FleetID()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "fleet_id: "+fleetID+"\n") || !strings.Contains(out.String(), "registry: registered\n") {
		t.Fatalf("init output = %q, want identity and registry outcome", out.String())
	}

	registryDB, err := registry.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registryDB.Close() }()
	fleets, err := registryDB.List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(fleets) != 1 || fleets[0].ID != fleetID || fleets[0].State != registry.StateReady {
		t.Fatalf("registered fleets = %+v, want ready %s", fleets, fleetID)
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

	seedLegacyInitTarget(t, dir)
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

// Bootstrap persists nothing on the operator's behalf while still reporting the detected harness
// and the harness-native tier defaults a worker will actually inherit.
func TestInitWritesNoWorkerOverrideAndReportsEffectiveDefaults(t *testing.T) {
	t.Setenv("HAND_HOME", "")
	t.Setenv("HAND_HARNESS", harness.Codex)
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
		"config_missing: 0\n",
		"harness,detected,codex",
		"model,native-default,none",
		"effort,native-default,none",
		"detects the current harness",
		"native model and effort defaults",
		"only to persist an explicit worker override",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("init output = %q, want it to contain %q", out.String(), want)
		}
	}
}

func TestInitHelpDescribesConfiguredAndUnknownEffectiveSettings(t *testing.T) {
	for _, test := range []struct {
		name       string
		detected   string
		configured string
		want       string
		unwant     string
	}{
		{
			name:       "configured",
			detected:   harness.Codex,
			configured: harness.Claude,
			want:       "uses the configured worker harness and any explicit model or effort overrides",
			unwant:     "detects the current harness",
		},
		{
			name:     "unknown",
			detected: "unknown",
			want:     "no supported worker harness is configured or detected",
			unwant:   "uses its native model and effort defaults",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HAND_HOME", "")
			t.Setenv("HAND_HARNESS", test.detected)
			dir := t.TempDir()
			t.Chdir(dir)
			if test.configured != "" {
				seedLegacyInitTarget(t, dir)
				if err := os.MkdirAll(filepath.Join(dir, "config"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "config", "harness"), []byte(test.configured+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			cmd := newInitCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), test.want) {
				t.Fatalf("init output = %q, want %q", out.String(), test.want)
			}
			if strings.Contains(out.String(), test.unwant) {
				t.Fatalf("init output = %q, do not want %q for %s settings", out.String(), test.unwant, test.name)
			}
		})
	}
}

// The retired flag has to fail loudly rather than be silently accepted, so a script still passing it
// is told the configuration moved instead of appearing to have set something.
func TestInitRefusesTheRetiredSetupFlag(t *testing.T) {
	t.Setenv("HAND_HOME", "")
	t.Chdir(t.TempDir())

	root := newRootCmd(devBuild("test"))
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

func TestInitRemovesTheSessionHookAndSaysSoOnlyWhenItChangedSettings(t *testing.T) {
	t.Setenv("HAND_HOME", "")
	dir := t.TempDir()
	t.Chdir(dir)
	seedLegacyInitTarget(t, dir)
	writeOwnedSessionHook(t, dir)

	for i, want := range []string{"session_hook: removed\n", "session_hook: unchanged\n"} {
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
		if !strings.Contains(out.String(), "AGENTS.md and its CLAUDE.md reference carry the startup integration across harnesses") {
			t.Fatalf("run %d output = %q, want cross-harness startup help", i+1, out.String())
		}
	}

	settings, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(settings), "/old/path/hand") || !strings.Contains(string(settings), "/usr/bin/custom") {
		t.Fatalf("settings = %q, want only the unrelated hook preserved", settings)
	}
}

func TestInitInstallsTheBundledSkillIntoEveryDestination(t *testing.T) {
	t.Setenv("HAND_HOME", "")
	dir := t.TempDir()
	t.Chdir(dir)

	var out bytes.Buffer
	cmd := newInitCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "skill_conflicts: 0\n") {
		t.Fatalf("output = %q, want zero skill conflicts on a fresh home", out.String())
	}

	for _, rel := range []string{
		filepath.Join(".claude", "skills", "secondhand"),
		filepath.Join(".grok", "skills", "secondhand"),
		filepath.Join(".pi", "skills", "secondhand"),
		filepath.Join(".agents", "skills", "secondhand"),
	} {
		if _, err := os.Stat(filepath.Join(dir, rel, "SKILL.md")); err != nil {
			t.Fatalf("%s/SKILL.md missing after init: %v", rel, err)
		}
	}
}

func TestInitReportsASkillDestinationConflictWithoutOverwritingTheForeignFile(t *testing.T) {
	t.Setenv("HAND_HOME", "")
	dir := t.TempDir()
	t.Chdir(dir)
	seedLegacyInitTarget(t, dir)
	claudeSkillDir := filepath.Join(dir, ".claude", "skills", "secondhand")
	if err := os.MkdirAll(claudeSkillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := "# Someone else's skill\n"
	if err := os.WriteFile(filepath.Join(claudeSkillDir, "SKILL.md"), []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newInitCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "skill_conflicts: 1\n") {
		t.Fatalf("output = %q, want one skill conflict reported", out.String())
	}
	if !strings.Contains(out.String(), "already hold a foreign file") {
		t.Fatalf("output = %q, want help text naming the conflict", out.String())
	}
	got, err := os.ReadFile(filepath.Join(claudeSkillDir, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != foreign {
		t.Fatalf("got %q, want the foreign file left exactly as written", got)
	}
	// A conflict at one destination must not block the others.
	if _, err := os.Stat(filepath.Join(dir, ".grok", "skills", "secondhand", "SKILL.md")); err != nil {
		t.Fatalf("unaffected destination not installed: %v", err)
	}
}
