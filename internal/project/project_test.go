package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atqamz/secondhand/internal/store"
)

func TestDeriveName(t *testing.T) {
	cases := map[string]string{
		"https://github.com/org/repo":     "repo",
		"https://github.com/org/repo.git": "repo",
		"git@github.com:org/repo.git":     "repo",
		"local":                           "local",
	}
	for url, want := range cases {
		if got := DeriveName(url); got != want {
			t.Errorf("DeriveName(%q) = %q, want %q", url, got, want)
		}
	}
}

func TestParseRepoRef(t *testing.T) {
	cases := map[string]string{
		"kunchenguid/no-mistakes":                        "kunchenguid/no-mistakes",
		"kunchenguid/no-mistakes.git":                    "kunchenguid/no-mistakes",
		"https://github.com/kunchenguid/no-mistakes":     "kunchenguid/no-mistakes",
		"https://github.com/kunchenguid/no-mistakes.git": "kunchenguid/no-mistakes",
		"git@github.com:kunchenguid/no-mistakes.git":     "kunchenguid/no-mistakes",
	}
	for ref, want := range cases {
		got, ok := ParseRepoRef(ref)
		if !ok || got != want {
			t.Errorf("ParseRepoRef(%q) = %q, %v, want %q, true", ref, got, ok, want)
		}
	}
	// A slug with whitespace in it survives neither the projection nor reading it
	// back, so it must never reach a project row: the registry writes upstream as
	// a whitespace-separated field, and a truncated one rejects the whole line.
	for _, ref := range []string{"", "no-mistakes", "/no-mistakes", "kunchenguid/", "kunchenguid/no-mistakes/pull", "https://gitlab.com/a/b",
		"kunchen guid/no-mistakes", "kunchenguid/no mistakes", "kunchenguid/no-mistakes ", "https://github.com/kunchen guid/no-mistakes",
		"kunchenguid/no\tmistakes", "kunchenguid/no-mistakes\n"} {
		if got, ok := ParseRepoRef(ref); ok {
			t.Errorf("ParseRepoRef(%q) = %q, true, want it refused", ref, got)
		}
	}
}

func TestSetUpstreamRoundTripsThroughTheProjection(t *testing.T) {
	dir := t.TempDir()
	writeRegistry(t, dir, "# Projects\n\n- no-mistakes: https://github.com/atqamz/no-mistakes mode=no-mistakes\n")

	if err := SetUpstream(dir, "no-mistakes", "kunchenguid/no-mistakes"); err != nil {
		t.Fatal(err)
	}

	rendered, err := os.ReadFile(RegistryPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rendered), "upstream=kunchenguid/no-mistakes") {
		t.Fatalf("registry = %q, want the upstream rendered into the projection", rendered)
	}

	projects, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Upstream != "kunchenguid/no-mistakes" {
		t.Fatalf("got %+v, want the upstream on record", projects)
	}

	if err := SetUpstream(dir, "no-mistakes", ""); err != nil {
		t.Fatal(err)
	}
	projects, err = List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Upstream != "" {
		t.Fatalf("got %+v, want the upstream cleared", projects)
	}
	cleared, err := os.ReadFile(RegistryPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cleared), "upstream=") {
		t.Fatalf("registry = %q, want no upstream field once cleared", cleared)
	}
}

func TestSetUpstreamNotFound(t *testing.T) {
	dir := t.TempDir()
	writeRegistry(t, dir, "# Projects\n\n")

	err := SetUpstream(dir, "missing", "owner/repo")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetUpstream = %v, want ErrNotFound", err)
	}
}

// A hand-written registry line is imported before the database exists, so an upstream typed as
// a URL has to normalize on the way in, and one that resolves to no slug has to refuse the whole
// line rather than import a project whose upstream would silently never match.
func TestListNormalizesUpstreamFromTheRegistry(t *testing.T) {
	dir := t.TempDir()
	writeRegistry(t, dir, "- fork: https://github.com/atqamz/fork mode=direct-pr upstream=https://github.com/upstream/fork.git\n")

	projects, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Upstream != "upstream/fork" {
		t.Fatalf("got %+v, want upstream normalized to upstream/fork", projects)
	}

	other := t.TempDir()
	writeRegistry(t, other, "- fork: https://github.com/atqamz/fork mode=direct-pr upstream=nonsense\n")
	if _, err := List(other); err == nil {
		t.Fatal("List = nil error, want an unresolvable upstream to refuse the line")
	}
}

func writeRegistry(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(RegistryPath(dir), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestListParsesRegistry(t *testing.T) {
	dir := t.TempDir()
	writeRegistry(t, dir, "# Projects\n\n- nsr: https://github.com/yes2games/nsr mode=direct-pr\n- secondhand: local mode=local-only\n")

	projects, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 {
		t.Fatalf("got %d projects, want 2", len(projects))
	}
	if projects[0] != (Project{Name: "nsr", URL: "https://github.com/yes2games/nsr", Mode: "direct-pr"}) {
		t.Errorf("got %+v", projects[0])
	}
	if projects[1] != (Project{Name: "secondhand", URL: "local", Mode: "local-only"}) {
		t.Errorf("got %+v", projects[1])
	}
}

func TestListMissingFile(t *testing.T) {
	dir := t.TempDir()
	projects, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if projects != nil {
		t.Errorf("got %+v, want nil", projects)
	}
}

func TestMigrateWithoutProjectsDefersTheOneTimeLegacyImport(t *testing.T) {
	for _, test := range []struct {
		name string
		seed string
	}{
		{name: "missing registry"},
		{name: "empty registry", seed: "# Projects\n\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			if test.seed != "" {
				writeRegistry(t, dir, test.seed)
			}
			if err := Migrate(dir); err != nil {
				t.Fatal(err)
			}
			if projectMigrationDone(t, dir) {
				t.Fatal("migration without a project consumed the future legacy import")
			}

			writeRegistry(t, dir, "# Projects\n\n- first: local mode=local-only\n")
			if err := Migrate(dir); err != nil {
				t.Fatal(err)
			}
			projects, err := ListReadOnly(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(projects) != 1 || projects[0].Name != "first" {
				t.Fatalf("projects after the first real import = %+v, want first", projects)
			}
			if !projectMigrationDone(t, dir) {
				t.Fatal("non-empty legacy registry was imported without recording completion")
			}

			writeRegistry(t, dir, "# Projects\n\n- first: local mode=local-only\n- second: local mode=local-only\n")
			if err := Migrate(dir); err != nil {
				t.Fatal(err)
			}
			projects, err = ListReadOnly(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(projects) != 1 || projects[0].Name != "first" {
				t.Fatalf("projects after repeat migration = %+v, want the first import exactly once", projects)
			}
		})
	}
}

func projectMigrationDone(t *testing.T, dir string) bool {
	t.Helper()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	done, err := db.Migrated(legacyRegistryKey)
	if err != nil {
		t.Fatal(err)
	}
	return done
}

func TestListRejectsMalformedLine(t *testing.T) {
	dir := t.TempDir()
	writeRegistry(t, dir, "# Projects\n\nnot a project\n")

	if _, err := List(dir); err == nil {
		t.Fatal("expected malformed registry line to fail")
	}
}

func TestListRejectsUnknownMode(t *testing.T) {
	dir := t.TempDir()
	writeRegistry(t, dir, "- nsr: https://github.com/yes2games/nsr mode=unknown\n")

	if _, err := List(dir); err == nil {
		t.Fatal("expected unknown project mode to fail")
	}
}

func TestAddDuplicateRejected(t *testing.T) {
	dir := t.TempDir()
	writeRegistry(t, dir, "# Projects\n\n- nsr: https://github.com/yes2games/nsr mode=direct-pr\n")

	err := Add(dir, Project{Name: "nsr", URL: "https://github.com/other/nsr", Mode: "direct-pr"})
	if err == nil {
		t.Fatal("expected error for duplicate name")
	}
}

func TestAddRejectsUnknownMode(t *testing.T) {
	dir := t.TempDir()
	writeRegistry(t, dir, "# Projects\n")

	if err := Add(dir, Project{Name: "nsr", URL: "https://github.com/yes2games/nsr", Mode: "unknown"}); err == nil {
		t.Fatal("expected unknown project mode to fail")
	}

	projects, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		t.Fatalf("got %+v, want no projects", projects)
	}
}

func TestAddAppends(t *testing.T) {
	dir := t.TempDir()
	writeRegistry(t, dir, "# Projects\n\n")

	if err := Add(dir, Project{Name: "nsr", URL: "https://github.com/yes2games/nsr", Mode: "direct-pr"}); err != nil {
		t.Fatal(err)
	}

	projects, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Name != "nsr" {
		t.Fatalf("got %+v", projects)
	}
}

func TestAddAppendsAfterRegistryWithoutTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	writeRegistry(t, dir, "- existing: https://github.com/org/existing mode=direct-pr")

	if err := Add(dir, Project{Name: "new", URL: "https://github.com/org/new", Mode: "direct-pr"}); err != nil {
		t.Fatal(err)
	}

	projects, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 || projects[1].Name != "new" {
		t.Fatalf("got %+v", projects)
	}
}

func TestConcurrentAddsPreserveAllProjects(t *testing.T) {
	dir := t.TempDir()
	writeRegistry(t, dir, "# Projects\n")

	const count = 20
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		go func(i int) {
			errs <- Add(dir, Project{Name: fmt.Sprintf("project-%d", i), URL: "https://github.com/org/repo", Mode: "direct-pr"})
		}(i)
	}
	for i := 0; i < count; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}

	projects, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != count {
		t.Fatalf("got %d projects, want %d", len(projects), count)
	}
}

func TestFind(t *testing.T) {
	dir := t.TempDir()
	writeRegistry(t, dir, "# Projects\n\n- nsr: https://github.com/yes2games/nsr mode=direct-pr\n")

	p, ok, err := Find(dir, "nsr")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected to find nsr")
	}
	if p.URL != "https://github.com/yes2games/nsr" {
		t.Errorf("got %+v", p)
	}

	_, ok, err = Find(dir, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected not to find missing project")
	}
}

func TestRemove(t *testing.T) {
	dir := t.TempDir()
	writeRegistry(t, dir, "# Projects\n\n- nsr: https://github.com/yes2games/nsr mode=direct-pr\n- other: https://github.com/org/other mode=local-only\n")

	if err := Remove(dir, "nsr"); err != nil {
		t.Fatal(err)
	}

	projects, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Name != "other" {
		t.Fatalf("got %+v", projects)
	}
}

func TestRemoveNotFound(t *testing.T) {
	dir := t.TempDir()
	writeRegistry(t, dir, "# Projects\n\n")

	if err := Remove(dir, "missing"); err == nil {
		t.Fatal("expected error for missing project")
	}
}

// Puts a fake no-mistakes binary at the front of PATH, ignoring its arguments and always exiting
// 0, matching the real binary for `status`: initialized or not, it exits 0, so GateStatus reads
// the outcome from stdout text. `runs` refusals need fakeNoMistakesExit instead.
func fakeNoMistakes(t *testing.T, stdout string) {
	fakeNoMistakesExit(t, stdout, 0)
}

// fakeNoMistakes with an explicit exit code, for the invocations the real binary refuses non-zero:
// `no-mistakes runs` exits 1 on both "repo not initialized" and "not in a git repository", where
// `status` exits 0 on the same text. Flattening that to 0 would pass a real regression.
func fakeNoMistakesExit(t *testing.T, stdout string, code int) {
	t.Helper()
	bin := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\ncat <<'EOF'\n%s\nEOF\nexit %d\n", stdout, code)
	if err := os.WriteFile(filepath.Join(bin, "no-mistakes"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestGateStatusReady(t *testing.T) {
	fakeNoMistakes(t, "    repo:  /home/atqa/secondhand/projects/secondhand\n  remote:  git@github.com:atqamz/secondhand.git\n    gate:  /home/atqa/.no-mistakes/repos/0b474f2021dd.git\n  daemon:  running\n\n  no active run")

	got, err := GateStatus(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got != GateReady {
		t.Fatalf("got %v, want GateReady", got)
	}
}

// Covers both real histories from atqamz/secondhand#60 at once: a project never given a
// no-mistakes init, and one whose working_path went stale when the fleet home was renamed. Both
// print this text byte-for-byte, so a second test on the same literal asserts nothing.
func TestGateStatusNotInitialized(t *testing.T) {
	fakeNoMistakes(t, "repo not initialized (run 'no-mistakes init' first)")

	got, err := GateStatus(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got != GateNotInitialized {
		t.Fatalf("got %v, want GateNotInitialized", got)
	}
}

func TestGateStatusMissingBinaryIsDistinctFromNotInitialized(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	gotState, err := GateStatus(t.TempDir())
	if err == nil {
		t.Fatal("expected error when no-mistakes binary is not on PATH")
	}
	if gotState == GateNotInitialized {
		t.Fatal("missing binary must not report GateNotInitialized, it has a different remedy")
	}
	if strings.Contains(err.Error(), gateNotInitializedMarker) {
		t.Fatalf("err = %v, must not read as the not-initialized case", err)
	}
}

// Covers atqamz/secondhand#97's first clone-path outcome: clonePath exists but isn't a git
// repository. no-mistakes status still exits 0 printing this text verbatim, so without the branch
// GateStatus falls through to GateReady and lets a caller dispatch into an uncovered project.
func TestGateStatusNotGitRepo(t *testing.T) {
	fakeNoMistakes(t, "not in a git repository")

	dir := t.TempDir()
	gotState, err := GateStatus(dir)
	if err == nil {
		t.Fatal("expected error when clone path is not a git repository")
	}
	if gotState == GateNotInitialized {
		t.Fatal("not-a-git-repo must not report GateNotInitialized, it has a different (unrepairable) remedy")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Fatalf("err = %v, want it to name the clone path", err)
	}
}

// Covers atqamz/secondhand#97's second clone-path outcome: clonePath does not exist, so
// exec.Command's chdir fails before the binary runs. Without the os.Stat check this read as "binary
// not found or not runnable" - true in the letter, misleading in substance.
func TestGateStatusMissingClonePath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	gotState, err := GateStatus(missing)
	if err == nil {
		t.Fatal("expected error for a clone path that does not exist")
	}
	if gotState == GateNotInitialized {
		t.Fatal("missing clone path must not report GateNotInitialized, it has a different remedy")
	}
	if strings.Contains(err.Error(), "no-mistakes binary not found or not runnable") {
		t.Fatalf("err = %v, must not read as a binary problem", err)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Fatalf("err = %v, want it to name the missing clone path", err)
	}
}

func TestGateInitCommand(t *testing.T) {
	got := GateInitCommand("/home/atqa/secondhand/projects/secondhand")
	want := "cd /home/atqa/secondhand/projects/secondhand && no-mistakes init"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRemoveFailsClosedOnMalformedLine(t *testing.T) {
	dir := t.TempDir()
	content := "# Projects\n\n- nsr: https://github.com/yes2games/nsr mode=direct-pr\n\ncustom note\n- other: https://github.com/org/other mode=local-only\n"
	writeRegistry(t, dir, content)

	if err := Remove(dir, "nsr"); err == nil {
		t.Fatal("expected unrecognized line to fail closed")
	}
	if got, err := os.ReadFile(RegistryPath(dir)); err != nil || string(got) != content {
		t.Fatalf("registry changed after failed remove: %q, %v", got, err)
	}
}

// The projection is a read-modify-write over a whole file, so a second writer
// has to wait for the first: rendering from its own snapshot mid-write is how a
// registered project goes missing from data/projects.md.
func TestAddWaitsForTheRegistryLock(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- Add(dir, Project{Name: "alpha", URL: "https://example.com/alpha.git", Mode: ModeDirectPR})
	}()
	select {
	case err := <-done:
		unlock()
		t.Fatalf("add returned %v while the registry lock was held", err)
	case <-time.After(200 * time.Millisecond):
	}

	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("add never completed after the lock was released")
	}

	projection, err := os.ReadFile(RegistryPath(dir))
	if err != nil || !strings.Contains(string(projection), "- alpha: ") {
		t.Fatalf("projection = %q, %v, want alpha listed", projection, err)
	}
}
