package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func openTemp(t *testing.T) (*DB, string) {
	t.Helper()
	home := t.TempDir()
	db, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, home
}

func sampleTask() Task {
	return Task{
		ID: "fix-login", Project: "nsr", Kind: KindShip, Harness: "claude",
		Model: "opus", Effort: "high", Worktree: "/w/nsr", Brief: "data/fix-login/brief.md",
		Herdr:           Herdr{Session: "default", WorkspaceID: "wA", TabID: "wA:tB", PaneID: "wA:pC"},
		PR:              "https://github.com/o/nsr/pull/1",
		MergeExecuted:   true,
		MergeExecutedAt: "2026-07-24T12:00:00Z",
		ReportOffset:    42,
		ReportDigest:    "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
		MergeAnnounced:  true,
		DoneVerified:    true,
		CreatedAt:       "2026-07-24T10:00:00Z",

		StatusChangedAt: "2026-07-24T11:00:00Z", StatusChangedFor: "working",
		LastReportState: "working", LastReportNote: "on it",

		PaneStartedAt:  "2026-07-24T10:30:00Z",
		ParkedFiredFor: "2026-07-24T11:30:00.123456789Z",

		UsageLimitRetryAt:  "2026-07-24T15:00:00Z",
		UsageLimitAttempts: 2,

		SendUndeliveredMessage: "stop and wait for review",
		SendUndeliveredAt:      "2026-07-24T13:00:00Z",

		LeaseID: "5fe5412a4aabdeb85a148d6d73eb42d8",
	}
}

func TestWriteReadPreservesEveryField(t *testing.T) {
	db, _ := openTemp(t)
	want := sampleTask()
	if err := db.WriteTask(want); err != nil {
		t.Fatal(err)
	}

	got, found, err := db.ReadTask(want.ID)
	if err != nil || !found {
		t.Fatalf("ReadTask = %v, %v", found, err)
	}
	if got != want {
		t.Fatalf("round trip lost a field:\ngot  %+v\nwant %+v", got, want)
	}
}

func TestWriteTaskOverwritesInPlace(t *testing.T) {
	db, _ := openTemp(t)
	task := sampleTask()
	if err := db.WriteTask(task); err != nil {
		t.Fatal(err)
	}
	task.PR = "https://github.com/o/nsr/pull/2"
	if err := db.WriteTask(task); err != nil {
		t.Fatal(err)
	}

	tasks, err := db.ListTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].PR != task.PR {
		t.Fatalf("ListTasks = %+v", tasks)
	}
}

func TestReadTaskReportsAMissingTaskWithoutAnError(t *testing.T) {
	db, _ := openTemp(t)
	_, found, err := db.ReadTask("nope")
	if err != nil || found {
		t.Fatalf("ReadTask = %v, %v", found, err)
	}
}

func TestDeleteTask(t *testing.T) {
	db, _ := openTemp(t)
	if err := db.WriteTask(sampleTask()); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteTask("fix-login"); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteTask("fix-login"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("second delete = %v, want ErrTaskNotFound", err)
	}
	exists, err := db.TaskExists("fix-login")
	if err != nil || exists {
		t.Fatalf("TaskExists = %v, %v", exists, err)
	}
}

func TestProjectsKeepRegistrationOrder(t *testing.T) {
	db, _ := openTemp(t)
	for _, name := range []string{"nsr", "universe", "yes2infra"} {
		if err := db.AddProject(Project{Name: name, URL: "git@github.com:o/" + name + ".git", Mode: "direct-pr"}); err != nil {
			t.Fatal(err)
		}
	}

	projects, err := db.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range projects {
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "nsr,universe,yes2infra" {
		t.Fatalf("order = %v", names)
	}
}

func TestAddProjectRejectsADuplicateName(t *testing.T) {
	db, _ := openTemp(t)
	p := Project{Name: "nsr", URL: "git@github.com:o/nsr.git", Mode: "direct-pr"}
	if err := db.AddProject(p); err != nil {
		t.Fatal(err)
	}
	if err := db.AddProject(p); !errors.Is(err, ErrProjectExists) {
		t.Fatalf("got %v, want ErrProjectExists", err)
	}
}

func TestRemoveProject(t *testing.T) {
	db, _ := openTemp(t)
	if err := db.AddProject(Project{Name: "nsr", URL: "u", Mode: "direct-pr"}); err != nil {
		t.Fatal(err)
	}
	removed, err := db.RemoveProject("nsr")
	if err != nil || !removed {
		t.Fatalf("RemoveProject = %v, %v", removed, err)
	}
	removed, err = db.RemoveProject("nsr")
	if err != nil || removed {
		t.Fatalf("second RemoveProject = %v, %v", removed, err)
	}
}

func writeLegacyTask(t *testing.T, home string, task Task) {
	t.Helper()
	if err := os.MkdirAll(Dir(home), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(Dir(home), task.ID+".json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOpenImportsLegacyTaskFiles(t *testing.T) {
	home := t.TempDir()
	want := sampleTask()
	writeLegacyTask(t, home, want)

	db, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	got, found, err := db.ReadTask(want.ID)
	if err != nil || !found {
		t.Fatalf("ReadTask = %v, %v", found, err)
	}
	if got != want {
		t.Fatalf("import lost a field:\ngot  %+v\nwant %+v", got, want)
	}
	if _, err := os.Stat(filepath.Join(Dir(home), want.ID+".json")); !os.IsNotExist(err) {
		t.Fatalf("legacy file still in state/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(LegacyDir(home), want.ID+".json")); err != nil {
		t.Fatalf("legacy file not preserved under state/migrated: %v", err)
	}
}

// A legacy file written before pane_started_at existed must land the value the schema
// migration's backfill would have given it: the import is an INSERT the backfill never runs
// over, and an empty pane start slides parked's floor back to the row's creation.
func TestLegacyImportBackfillsThePaneStart(t *testing.T) {
	for _, tc := range []struct {
		name            string
		statusChangedAt string
		want            string
	}{
		{"from the last observed status change", "2026-07-24T11:00:00Z", "2026-07-24T11:00:00Z"},
		{"from creation when no status was ever observed", "", "2026-07-24T10:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			legacy := sampleTask()
			legacy.PaneStartedAt = ""
			legacy.StatusChangedAt = tc.statusChangedAt
			writeLegacyTask(t, home, legacy)

			db, err := Open(home)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()

			got, found, err := db.ReadTask(legacy.ID)
			if err != nil || !found {
				t.Fatalf("ReadTask = %v, %v", found, err)
			}
			if got.PaneStartedAt != tc.want {
				t.Fatalf("PaneStartedAt = %q, want %q", got.PaneStartedAt, tc.want)
			}
		})
	}
}

// Running the migration twice is what actually happens: every hand command opens
// the store. The second open must find nothing to do and change nothing.
func TestMigrationIsIdempotent(t *testing.T) {
	home := t.TempDir()
	writeLegacyTask(t, home, sampleTask())

	first, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	updated := sampleTask()
	updated.PR = "https://github.com/o/nsr/pull/99"
	if err := first.WriteTask(updated); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()

	got, found, err := second.ReadTask(updated.ID)
	if err != nil || !found {
		t.Fatalf("ReadTask = %v, %v", found, err)
	}
	if got.PR != updated.PR {
		t.Fatalf("a re-run overwrote live state with the archived file: PR = %q", got.PR)
	}
	tasks, err := second.ListTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("ListTasks = %d tasks, want 1", len(tasks))
	}
}

// A legacy file the migration cannot read is a loud failure, not a skipped task:
// silently continuing would present a partial fleet as the whole one.
func TestOpenRefusesAnUnreadableLegacyFile(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(Dir(home), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(Dir(home), "broken.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	db, err := Open(home)
	if err == nil {
		_ = db.Close()
		t.Fatal("Open accepted an unparseable legacy task file")
	}
	if !strings.Contains(err.Error(), "broken.json") {
		t.Fatalf("error does not name the file: %v", err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("refusing consumed the file it could not read: %v", statErr)
	}
}

// Every hand command opens the store, so first contact with a legacy home is routinely
// several at once. The import spans a readdir, an insert and an archive rename, none of them
// serialized, so concurrent opens must still import each task once and archive every file.
func TestConcurrentOpensImportALegacyHomeExactlyOnce(t *testing.T) {
	home := t.TempDir()
	const legacyTasks = 5
	for i := range legacyTasks {
		task := sampleTask()
		task.ID = fmt.Sprintf("task-%d", i)
		writeLegacyTask(t, home, task)
	}

	const openers = 8
	errs := make([]error, openers)
	var wg sync.WaitGroup
	for i := range openers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db, err := Open(home)
			if err != nil {
				errs[i] = err
				return
			}
			errs[i] = db.Close()
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent open %d: %v", i, err)
		}
	}

	db, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	tasks, err := db.ListTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != legacyTasks {
		t.Fatalf("ListTasks = %d tasks, want %d imported exactly once", len(tasks), legacyTasks)
	}
	for _, task := range tasks {
		if _, err := os.Stat(filepath.Join(Dir(home), task.ID+".json")); !os.IsNotExist(err) {
			t.Fatalf("stat state/%s.json: %v, want every imported file moved aside", task.ID, err)
		}
		if _, err := os.Stat(filepath.Join(LegacyDir(home), task.ID+".json")); err != nil {
			t.Fatalf("stat archived %s.json: %v, want it kept for the operator", task.ID, err)
		}
	}
}

// The parse -> insert -> archive sequence only holds together while one
// importer runs it, so an import that meets the lock held has to wait for it
// rather than run its own copy alongside.
func TestLegacyImportWaitsForTheMigrationLock(t *testing.T) {
	home := t.TempDir()
	writeLegacyTask(t, home, sampleTask())

	unlock, err := Lock(home, MigrationLock, false)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		db, err := Open(home)
		if err == nil {
			err = db.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		unlock()
		t.Fatalf("Open returned %v while the migration lock was held", err)
	case <-time.After(200 * time.Millisecond):
	}

	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Open never completed after the lock was released")
	}

	if _, err := os.Stat(filepath.Join(LegacyDir(home), sampleTask().ID+".json")); err != nil {
		t.Fatalf("stat archived task file: %v, want the import to have finished", err)
	}
}

func TestMigratedMarker(t *testing.T) {
	db, _ := openTemp(t)
	done, err := db.Migrated("projects.md")
	if err != nil || done {
		t.Fatalf("Migrated before marking = %v, %v", done, err)
	}
	if err := db.MarkMigrated("projects.md"); err != nil {
		t.Fatal(err)
	}
	done, err = db.Migrated("projects.md")
	if err != nil || !done {
		t.Fatalf("Migrated after marking = %v, %v", done, err)
	}
}

func TestPathsLiveUnderTheStateDirectory(t *testing.T) {
	home := "/fleet"
	if got := Path(home); got != filepath.Join(home, "state", "hand.db") {
		t.Errorf("Path = %q", got)
	}
	if got := IndexPath(home); got != filepath.Join(home, "state", "index.db") {
		t.Errorf("IndexPath = %q", got)
	}
	if got := LegacyDir(home); got != filepath.Join(home, "state", "migrated") {
		t.Errorf("LegacyDir = %q", got)
	}
}

// A fleet home can sit anywhere an operator put it, and the pragmas require a
// file: URI, where `%`, `#` and `?` are syntax rather than filename.
func TestOpenHandlesAHomePathWithURISyntaxInIt(t *testing.T) {
	home := filepath.Join(t.TempDir(), "fleet 100%#a?b")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := Open(home)
	if err != nil {
		t.Fatalf("open %q: %v", home, err)
	}
	defer func() { _ = db.Close() }()

	if err := db.WriteTask(sampleTask()); err != nil {
		t.Fatal(err)
	}
	got, ok, err := db.ReadTask(sampleTask().ID)
	if err != nil || !ok {
		t.Fatalf("ReadTask = %v, %v", ok, err)
	}
	if got.Project != sampleTask().Project {
		t.Fatalf("got %+v, want the task written back", got)
	}
	if _, err := os.Stat(Path(home)); err != nil {
		t.Fatalf("stat %s: %v, want the database inside the home", Path(home), err)
	}
}

func TestOpenReadOnlyReadsCurrentRowsAndRefusesWrites(t *testing.T) {
	db, home := openTemp(t)
	if err := db.WriteTask(sampleTask()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(Path(home))
	if err != nil {
		t.Fatal(err)
	}

	db, err = OpenReadOnly(home)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := db.ListTasks()
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != sampleTask().ID {
		t.Fatalf("ListTasks = %+v, want the current SQLite row", tasks)
	}
	if err := db.WriteTask(Task{ID: "must-not-write"}); err == nil {
		t.Fatal("WriteTask through OpenReadOnly succeeded")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(Path(home))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("OpenReadOnly changed the database bytes")
	}
}
