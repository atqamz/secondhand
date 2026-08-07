package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/atqamz/secondhand/internal/axi"
	"github.com/atqamz/secondhand/internal/project"
	"github.com/atqamz/secondhand/internal/state"
	"github.com/atqamz/secondhand/internal/store"
)

// Fakes "pane get" as a query command per internal/herdr/client.go's call() doc: a non-null result
// object on success. It always succeeds; the "herdr unreachable" degrade path is exercised for real
// (no fake, empty PATH) by TestStatusFleetDegradesToUnknownWhenHerdrUnreachable below.
func writeFakeHerdrPaneStatus(t *testing.T, status string) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\nprintf '{\"id\":\"cli:1\",\"result\":{\"pane\":{\"pane_id\":\"wA:pB\",\"agent_status\":\"" + status + "\"}}}'\n"
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// The tasks[] row whose first cell is id, so a test can assert on one row's cells without matching the
// whole document.
func fleetRow(t *testing.T, out, id string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "  "+id+",") {
			return strings.TrimPrefix(line, "  ")
		}
	}
	t.Fatalf("got %q, want a tasks row for %q", out, id)
	return ""
}

// The tokens of a default fleet row's trailing flags cell.
func fleetFlags(t *testing.T, out, id string) []string {
	t.Helper()
	row := fleetRow(t, out, id)
	cell := row[strings.LastIndex(row, ",")+1:]
	if cell == "none" {
		return nil
	}
	return strings.Fields(cell)
}

// Whichever merge token a flags cell carries, or "" for neither.
func mergeFlag(flags []string) string {
	for _, f := range flags {
		if strings.HasPrefix(f, "merged") {
			return f
		}
	}
	return ""
}

// One scalar field of the single-task view, unquoted.
func detailField(t *testing.T, out, name string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		value, ok := strings.CutPrefix(line, name+": ")
		if !ok {
			continue
		}
		if strings.HasPrefix(value, `"`) {
			unquoted, err := strconv.Unquote(value)
			if err != nil {
				t.Fatalf("field %q value %q does not unquote: %v", name, value, err)
			}
			return unquoted
		}
		return value
	}
	t.Fatalf("got %q, want a %q field", out, name)
	return ""
}

func TestStatusFleetListsAllTasks(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "working")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "task-1") || !strings.Contains(out.String(), "working") {
		t.Fatalf("got %q, want task-1 and working", out.String())
	}
}

// The width-padded columns this replaced could run together at exactly the
// field width; a delimited row cannot, and this pins that the delimiter is
// still there for a value long enough to have merged before.
func TestStatusFleetRowCellsStaySeparableAtAnyValueWidth(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "working")

	id := "task-aaaaaaaaaaa"
	if err := state.Write(home, state.Task{ID: id, Project: "myproject12", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--fields", "id,project,state"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got, want := fleetRow(t, out.String(), id), id+",myproject12,working"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestStatusFleetDegradesToUnknownWhenHerdrUnreachable(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	t.Setenv("PATH", t.TempDir())

	if err := state.Write(home, state.Task{ID: "task-1", Herdr: state.Herdr{PaneID: "wA:pB"}}); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "unknown") {
		t.Fatalf("got %q, want unknown", out.String())
	}
}

func TestStatusFleetJSON(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "working")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"id": "task-1"`) || !strings.Contains(out.String(), `"agent_state": "working"`) {
		t.Fatalf("got %q, want tasks array with task-1 and agent_state working", out.String())
	}
	if !strings.Contains(out.String(), `"holds": []`) {
		t.Fatalf("got %q, want an empty holds array", out.String())
	}
	if !strings.HasPrefix(strings.TrimSpace(out.String()), "{") {
		t.Fatalf("got %q, want a JSON object wrapping tasks and holds", out.String())
	}
}

func TestStatusSingleTaskDetail(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "idle")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip, Harness: "claude",
		Herdr: state.Herdr{Session: "default", TabID: "wA:tB", PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"task-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := detailField(t, out.String(), "id"); got != "task-1" {
		t.Fatalf("id = %q, want task-1", got)
	}
	if got := detailField(t, out.String(), "state"); got != "idle" {
		t.Fatalf("state = %q, want idle", got)
	}
}

func TestStatusMergeStateCombinationsRenderDistinguishably(t *testing.T) {
	cases := []struct {
		name             string
		merged           bool
		prMergedObserved bool
		wantMerge        string
	}{
		{"neither", false, false, ""},
		{"handMerged", true, false, "merged"},
		{"observedOnly", false, true, "merged-external"},
		{"both", true, true, "merged"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			t.Chdir(home)
			mkFleetDirs(t, home)
			writeFakeHerdrPaneStatus(t, "idle")

			if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
				Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z",
				PR: "https://github.com/a/b/pull/1", MergeExecuted: c.merged, MergeAnnounced: c.prMergedObserved}); err != nil {
				t.Fatal(err)
			}

			cmd := newStatusCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs([]string{"task-1"})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			got := out.String()
			if pr := detailField(t, got, "pr"); pr != "https://github.com/a/b/pull/1" {
				t.Fatalf("pr = %q, want the PR url", pr)
			}
			if merge := mergeFlag(strings.Fields(detailField(t, got, "flags"))); merge != c.wantMerge {
				t.Fatalf("merge flag = %q, want %q", merge, c.wantMerge)
			}
		})
	}
}

// hand status's half of atqamz/secondhand#69: a task whose PR a no-mistakes gate opened directly
// (bypassing hand pr) still shows the PR, once status looks it up by branch.
func TestStatusSingleTaskDetectsGateOpenedPR(t *testing.T) {
	home, worktree := setupTeardownHome(t)
	setupTeardownGateProject(t, home, worktree, "task-1-branch")
	writeFakeHerdrPaneStatus(t, "idle")
	writeFakeGHPRListAndView(t, ghFakePR{Number: 9, URL: "https://github.com/owner/repo/pull/9", State: "OPEN"})

	if err := state.Write(home, state.Task{ID: "task-1", Kind: state.KindShip, Worktree: worktree, Project: "myproj",
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"task-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "https://github.com/owner/repo/pull/9") {
		t.Fatalf("got %q, want the detected PR shown", out.String())
	}

	got, err := state.Read(home, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.PR != "https://github.com/owner/repo/pull/9" {
		t.Fatalf("task.PR = %q, want status to have recorded the detected PR", got.PR)
	}
}

// A scout task's deliverable is data/<id>/report.md, never a PR, so status skips the branch lookup for
// it exactly as checkLandedWork does - the gh fake here would answer with a PR, and recording it would
// pin one onto a task whose completion detail never uses it.
func TestStatusSkipsPRDetectionForScoutTasks(t *testing.T) {
	home, worktree := setupTeardownHome(t)
	setupTeardownGateProject(t, home, worktree, "task-1-branch")
	writeFakeHerdrPaneStatus(t, "idle")
	writeFakeGHPRListAndView(t, ghFakePR{Number: 9, URL: "https://github.com/owner/repo/pull/9", State: "OPEN"})

	if err := state.Write(home, state.Task{ID: "task-1", Kind: state.KindScout, Worktree: worktree, Project: "myproj",
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"task-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	got, err := state.Read(home, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.PR != "" {
		t.Fatalf("task.PR = %q, want a scout task left without a PR", got.PR)
	}
}

func TestStatusFleetOverviewRendersMergeMarker(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "idle")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z",
		PR: "https://github.com/a/b/pull/1", MergeAnnounced: true}); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := mergeFlag(fleetFlags(t, out.String(), "task-1")); got != "merged-external" {
		t.Fatalf("merge flag = %q, want the fleet overview to carry the merge marker", got)
	}
}

func TestStatusFleetOverviewTaskWithNoPRIsUnaffected(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "idle")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "merged") {
		t.Fatalf("got %q, want no merge marker without a PR", out.String())
	}
}

func TestStatusSingleTaskJSON(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "blocked")

	if err := state.Write(home, state.Task{ID: "task-1", Herdr: state.Herdr{PaneID: "wA:pB"}}); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"task-1", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"agent_state": "blocked"`) {
		t.Fatalf("got %q, want agent_state blocked", out.String())
	}
}

func TestStatusSingleTaskMissing(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)

	cmd := newStatusCmd()
	cmd.SetArgs([]string{"missing-task"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("got err %v, want not found", err)
	}
}

func TestStatusFleetFlagsIdleWithoutTerminalReportAsUnreported(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "idle")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	row := fleetRow(t, out.String(), "task-1")
	if !strings.HasPrefix(row, "task-1,idle,none,") || !strings.HasSuffix(row, ",unreported") {
		t.Fatalf("got %q, want idle flagged unreported", row)
	}
}

func TestStatusFleetFlagsIdleWithTerminalReportInsteadOfUnreported(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "idle")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state.ReportPath(home, "task-1"), []byte("needs-decision: waiting on review\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	row := fleetRow(t, out.String(), "task-1")
	if !strings.HasPrefix(row, "task-1,idle,needs-decision,") {
		t.Fatalf("got %q, want idle flagged with the reported state", row)
	}
	if strings.Contains(row, "unreported") {
		t.Fatalf("got %q, want no unreported flag once a terminal report explains the idle", row)
	}
}

// A worker that appends free text after a real report has still reported, so the suffix comes from the
// last line that classified - the same answer hand watch reaches about the same quiet pane. The
// Reported field still shows the raw last line, free text included.
func TestStatusFleetKeepsTheReportedFlagAfterATrailingMalformedLine(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "idle")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state.ReportPath(home, "task-1"),
		[]byte("needs-decision: waiting on review\nstill here, no answer yet\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if row := fleetRow(t, out.String(), "task-1"); !strings.HasPrefix(row, "task-1,idle,needs-decision,") {
		t.Fatalf("got %q, want the last classified report kept behind the free text", row)
	}
}

func TestStatusFleetDoesNotFlagWorkingTasks(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "working")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	row := fleetRow(t, out.String(), "task-1")
	if !strings.HasPrefix(row, "task-1,working,none,") || !strings.HasSuffix(row, ",none") {
		t.Fatalf("got %q, want a working task carrying no report and no flags", row)
	}
}

// A worker that appends `paused:` and leaves its harness running used to render
// as a bare `working`: the column showed the pane and hid the only party that
// said why. One of the two live symptoms atqamz/secondhand#89 names.
func TestStatusFleetCarriesAPausedReportThroughABusyPane(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "working")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state.ReportPath(home, "task-1"), []byte("paused: waiting on the nightly build\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if row := fleetRow(t, out.String(), "task-1"); !strings.HasPrefix(row, "task-1,working,paused,") {
		t.Fatalf("got %q, want the paused report carried through the busy pane", row)
	}
}

// The second live symptom in atqamz/secondhand#89: an hours-old figure sitting
// next to a status file touched minutes earlier, which reads as a stalled
// worker that is in fact reporting.
func TestStatusFleetDwellTimeFollowsTheReportFileNotTheTaskAge(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "working")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr:     state.Herdr{PaneID: "wA:pB"},
		CreatedAt: time.Now().Add(-5 * time.Hour).UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	report := state.ReportPath(home, "task-1")
	if err := os.WriteFile(report, []byte("working: still going\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	touched := time.Now().Add(-3 * time.Minute)
	if err := os.Chtimes(report, touched, touched); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--fields", "id,age,last_report"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got, want := fleetRow(t, out.String(), "task-1"), "task-1,5h ago,3m ago"; got != want {
		t.Fatalf("got %q, want %q: age measures the task, last_report the report file", got, want)
	}
}

// The same distinction in the detail view, where a stale figure is worse: it is
// the view an operator opens once the fleet table has already worried them.
func TestStatusSingleTaskDwellTimeFollowsTheReportFileNotTheTaskAge(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "working")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr:     state.Herdr{PaneID: "wA:pB"},
		CreatedAt: time.Now().Add(-5 * time.Hour).UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	report := state.ReportPath(home, "task-1")
	if err := os.WriteFile(report, []byte("working: still going\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	touched := time.Now().Add(-3 * time.Minute)
	if err := os.Chtimes(report, touched, touched); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"task-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if age := detailField(t, got, "age"); age != "5h ago" {
		t.Fatalf("age = %q, want it still measuring the task", age)
	}
	if last := detailField(t, got, "last_report"); last != "3m ago" {
		t.Fatalf("last_report = %q, want it measuring the report file", last)
	}
}

// A task whose worker has never written a report has no dwell time to show, and
// the column must say so rather than fall back to the task's age - the fallback
// is what made an untouched report look freshly written.
func TestStatusReportsNoDwellTimeWithoutAReportFile(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "working")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr:     state.Herdr{PaneID: "wA:pB"},
		CreatedAt: time.Now().Add(-5 * time.Hour).UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"task-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := detailField(t, out.String(), "last_report"); got != "none" {
		t.Fatalf("last_report = %q, want no dwell time invented for a task that never reported", got)
	}
}

func TestStatusSingleTaskShowsReportedStateAndHistory(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "idle")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	reportBody := "working: started\nneeds-decision: waiting on review\n"
	if err := os.WriteFile(state.ReportPath(home, "task-1"), []byte(reportBody), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"task-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if report := detailField(t, got, "report"); report != "needs-decision: waiting on review" {
		t.Fatalf("report = %q, want the last reported line", report)
	}
	if reported := detailField(t, got, "reported"); reported != "needs-decision" {
		t.Fatalf("reported = %q, want the classified state on its own field", reported)
	}
	if !strings.Contains(got, "report_history[1]:\n  - working: started\n") {
		t.Fatalf("got %q, want the earlier report line in the history block", got)
	}
}

// Holds the detail view to the same graceful degradation the fleet view already has: a report file
// that exists but can't be read names the fault and still prints the rest, rather than failing the
// whole command and showing nothing. A directory in its place is a real EISDIR, not a mocked error.
func TestStatusSingleTaskDegradesOnAnUnreadableReport(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "idle")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(state.ReportPath(home, "task-1"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"task-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("got %v, want the read fault degraded rather than failing the command", err)
	}
	got := out.String()
	if reported := detailField(t, got, "reported"); reported != reportUnreadable {
		t.Fatalf("reported = %q, want the fault named as the reported state", reported)
	}
	if report := detailField(t, got, "report"); !strings.HasPrefix(report, "report "+reportUnreadable+": ") {
		t.Fatalf("report = %q, want the read fault named", report)
	}
	if detailField(t, got, "id") != "task-1" || detailField(t, got, "state") != "idle" {
		t.Fatalf("got %q, want the rest of the detail view still printed", got)
	}
}

// atqamz/secondhand#65: a worker's report prose has run 2.7-4.3 KB for a
// single task, and hand status rendered it in full - the point of this test.
func TestTruncateReportLineKeepsVocabularyPrefixIntactUnderAnAdversarialBudget(t *testing.T) {
	line := state.ParseReportLine("done: " + strings.Repeat("x", 500))
	got := truncateReportLine(line, 3, "task-1") // smaller than the "done: " prefix itself
	if !strings.HasPrefix(got, "done: ") {
		t.Fatalf("got %q, want the done: prefix preserved even when the budget can't fit it", got)
	}
}

func TestTruncateReportLineMarksTruncationVisibly(t *testing.T) {
	line := state.ParseReportLine("working: " + strings.Repeat("x", 500))
	got := truncateReportLine(line, 50, "task-1")
	if strings.Contains(got, strings.Repeat("x", 500)) {
		t.Fatalf("got %q, want the note cut short", got)
	}
	if !strings.Contains(got, "(truncated, 509 chars total - use hand status task-1 --full to see complete text)") {
		t.Fatalf("got %q, want the cut to name its full size and the command that recovers it", got)
	}
}

func TestTruncateReportLineLeavesShortLinesUntouched(t *testing.T) {
	line := state.ParseReportLine("done: short")
	if got := truncateReportLine(line, reportSummaryBudget, "task-1"); got != "done: short" {
		t.Fatalf("got %q, want a report already under budget left exactly as reported", got)
	}
}

func TestTruncateReportLineDoesNotSplitAMultibyteRune(t *testing.T) {
	line := state.ParseReportLine("done: " + strings.Repeat("héllo", 100))
	got := truncateReportLine(line, 50, "task-1")
	if !utf8.ValidString(got) {
		t.Fatalf("got %q, want valid utf8 even when the cut lands inside a multi-byte character", got)
	}
}

// atqamz/secondhand#65's core complaint: hand status <id> printed the latest
// report twice, once under Reported: and again as the last entry of Report
// history. This asserts the fix at the command level, not just the helper.
func TestStatusSingleTaskTruncatesALongReportedLineAndPointsAtTheFile(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "idle")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	longNote := strings.Repeat("x", 500)
	if err := os.WriteFile(state.ReportPath(home, "task-1"), []byte("done: "+longNote+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"task-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	report := detailField(t, got, "report")
	if !strings.HasPrefix(report, "done: ") {
		t.Fatalf("report = %q, want the done: prefix intact", report)
	}
	if strings.Contains(report, longNote) {
		t.Fatalf("report = %q, want the long note truncated rather than printed in full", report)
	}
	// The truncation hint is the whole recovery path: without the total size and
	// the command that reaches the rest, a cut field reads as a short one.
	if !strings.Contains(report, "(truncated, 506 chars total - use hand status task-1 --full to see complete text)") {
		t.Fatalf("report = %q, want the cut to name its size and its recovery command", report)
	}
	if !strings.Contains(got, "  - Run `hand status task-1 --full` for the untruncated report and history\n") {
		t.Fatalf("got %q, want the recovery command in the help block too", got)
	}
	if file := detailField(t, got, "report_file"); file != state.ReportPath(home, "task-1") {
		t.Fatalf("report_file = %q, want the absolute path to the full report", file)
	}
}

// Every scalar the detail view emits has to be one key: value line, and every
// key has to be a name --fields accepts - otherwise a caller cannot ask for
// again what it was just shown.
func TestStatusSingleTaskEmitsOnlyAddressableFields(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "idle")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Harness: "claude", Model: "sonnet", Worktree: filepath.Join(home, "wt"),
		Herdr:     state.Herdr{Session: "default", TabID: "wA:tB", PaneID: "wA:pB"},
		CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state.ReportPath(home, "task-1"), []byte("done: landed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"task-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	known := map[string]bool{}
	for _, name := range axi.Names(taskFields) {
		known[name] = true
	}
	var got []string
	for _, line := range strings.Split(out.String(), "\n") {
		if line == "" || strings.HasPrefix(line, "  ") {
			continue
		}
		key, _, ok := strings.Cut(line, ": ")
		if !ok {
			if strings.HasSuffix(line, "]:") {
				continue
			}
			t.Fatalf("got %q, want every scalar line shaped key: value", line)
		}
		if !known[key] {
			t.Fatalf("got field %q, want a name --fields accepts", key)
		}
		got = append(got, key)
	}
	if strings.Join(got, ",") != strings.Join(detailDefaultFields, ",") {
		t.Fatalf("got fields %v, want the declared detail defaults %v", got, detailDefaultFields)
	}
}

func TestStatusSingleTaskHistoryDoesNotRepeatTheReportedEntry(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "idle")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	reportBody := "working: started\nneeds-decision: waiting on review\n"
	if err := os.WriteFile(state.ReportPath(home, "task-1"), []byte(reportBody), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"task-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Count(got, "needs-decision: waiting on review") != 1 {
		t.Fatalf("got %q, want the latest report shown exactly once, not repeated in history", got)
	}
	if !strings.Contains(got, "working: started") {
		t.Fatalf("got %q, want the earlier report kept in history", got)
	}
}

// --full is the literal opt-out: the report field and the history entry both
// carry the whole line, and the history keeps the entry the summary already
// shows.
func TestStatusSingleTaskFullFlagShowsEveryReportUntruncated(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "idle")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	longNote := strings.Repeat("x", 500)
	reportBody := "working: started\ndone: " + longNote + "\n"
	if err := os.WriteFile(state.ReportPath(home, "task-1"), []byte(reportBody), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"task-1", "--full"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Count(got, "done: "+longNote) != 2 {
		t.Fatalf("got %q, want --full to show the full untruncated entry in both the report field and the history", got)
	}
	if strings.Contains(got, "truncated,") {
		t.Fatalf("got %q, want --full to cut nothing", got)
	}
}

func TestStatusSingleTaskJSONKeepsTheFullReportUntruncated(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "idle")

	if err := state.Write(home, state.Task{ID: "task-1", Herdr: state.Herdr{PaneID: "wA:pB"}}); err != nil {
		t.Fatal(err)
	}
	longNote := strings.Repeat("x", 500)
	if err := os.WriteFile(state.ReportPath(home, "task-1"), []byte("done: "+longNote+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"task-1", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), longNote) {
		t.Fatalf("got %q, want the JSON report note left fully untruncated - a machine consumer wants the whole field", out.String())
	}
}

func TestStatusSingleTaskJSONIncludesReportedAndHistory(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "idle")

	if err := state.Write(home, state.Task{ID: "task-1", Herdr: state.Herdr{PaneID: "wA:pB"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state.ReportPath(home, "task-1"), []byte("blocked: waiting on secrets\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"task-1", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"state": "blocked"`) || !strings.Contains(out.String(), `"note": "waiting on secrets"`) {
		t.Fatalf("got %q, want reported state and note in JSON", out.String())
	}
	if !strings.Contains(out.String(), `"report_history"`) {
		t.Fatalf("got %q, want report_history in JSON", out.String())
	}
}

func TestStatusFleetShowsHeldBlock(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)

	if err := state.SetHold(home, state.Hold{ID: "fix-login", Kind: state.HoldKindOperator,
		Reason: "needs a call", SetAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "held: 1\n") {
		t.Fatalf("got %q, want the held aggregate counting the hold", got)
	}
	if !strings.Contains(got, "holds[1]{id,kind,detail,age}:\n  fix-login,operator,needs a call,") {
		t.Fatalf("got %q, want the hold's id, kind, and reason", got)
	}
}

// A hold outliving its task is the case the standalone hold table exists for;
// this pins that hand status still surfaces it once the task row is gone.
func TestStatusFleetShowsHeldBlockWithNoTaskRowBehindIt(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)

	if err := state.SetHold(home, state.Hold{ID: "torn-down-task", Kind: state.HoldKindOperator,
		Reason: "question never answered", SetAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "torn-down-task") {
		t.Fatalf("got %q, want the hold shown despite no task row", out.String())
	}
}

// Nothing held is a fact worth stating: an omitted block reads the same as a
// command that failed to look.
func TestStatusFleetStatesZeroHoldsRatherThanOmittingTheBlock(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "working")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "held: 0\n") || !strings.Contains(out.String(), "holds[0]{id,kind,detail,age}:\n") {
		t.Fatalf("got %q, want a zero count and an empty holds block", out.String())
	}
}

func TestStatusFleetFlagsInconsistentHold(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)

	db, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetHold(store.Hold{ID: "weird", Kind: "not-a-real-kind", Reason: "who knows"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `inconsistent: unrecognized kind \"not-a-real-kind\"`) {
		t.Fatalf("got %q, want the inconsistent hold flagged rather than silently rendered", out.String())
	}
}

// hand watch writes limit holds, so hand status has to render one as the ordinary fact it is. Left out of
// holdInconsistency it would come out flagged inconsistent, turning every routine usage limit into a
// report that something outside hand corrupted the database.
func TestStatusFleetRendersAMachineSetLimitHold(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)

	if err := state.SetHold(home, state.Hold{
		ID: "fix-login", Kind: state.HoldKindLimit,
		Reason: "harness stopped on a usage limit; 1 attempt made, next try 2026-08-04T15:01:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "inconsistent") {
		t.Fatalf("got %q, want a limit hold rendered as a real kind", out.String())
	}
	if !strings.Contains(out.String(), "limit") || !strings.Contains(out.String(), "next try 2026-08-04T15:01:00Z") {
		t.Fatalf("got %q, want the limit hold's kind and reason in the held block", out.String())
	}
}

func TestStatusFleetJSONFlagsInconsistentHold(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)

	db, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetHold(store.Hold{ID: "weird", Kind: state.HoldKindBlocked, Reason: "who knows"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"inconsistent": "blocked hold has no blocked_on"`) {
		t.Fatalf("got %q, want the inconsistency named in JSON", out.String())
	}
}

// A store fault reading holds must fail the whole command rather than degrade
// to an empty holds list, which would read as "nothing is waiting" - exactly
// the false all-clear this feature exists to avoid.
func TestStatusFleetPropagatesAnUnreadableHoldStore(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	if err := os.Remove(store.Path(home)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(store.Path(home), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err == nil {
		t.Fatal("got nil error, want the unreadable hold store to fail the command")
	}
}

func TestStatusSingleTaskShowsHeldLine(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "idle")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetHold(home, state.Hold{ID: "task-1", Kind: state.HoldKindBlocked,
		Reason: "waiting on migration", BlockedOn: "task-2", SetAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"task-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := detailField(t, out.String(), "held"); got != "waiting on task-2: waiting on migration" {
		t.Fatalf("held = %q, want it naming what the task waits on", got)
	}
}

func TestStatusSingleTaskStatesNoHoldRatherThanOmittingTheField(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "idle")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"task-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := detailField(t, out.String(), "held"); got != "none" {
		t.Fatalf("held = %q, want an explicit none without a hold", got)
	}
}

// Delivered work is terminal but not merged, so both views have to name that
// state: an operator who cannot see it would go looking for a merge that is
// never coming.
func TestStatusShowsDeliveredWorkWithoutClaimingAMerge(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "idle")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z",
		PR:          "https://github.com/kunchenguid/no-mistakes/pull/597",
		DeliveredAt: "2026-08-03T00:00:00Z", DeliveredReason: "offered upstream, maintainer decides"}); err != nil {
		t.Fatal(err)
	}

	single := newStatusCmd()
	var out bytes.Buffer
	single.SetOut(&out)
	single.SetArgs([]string{"task-1"})
	if err := single.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := detailField(t, out.String(), "delivered"); got != "offered upstream, maintainer decides (2026-08-03T00:00:00Z)" {
		t.Fatalf("delivered = %q, want it carrying the reason", got)
	}
	flags := strings.Fields(detailField(t, out.String(), "flags"))
	if !slices.Contains(flags, "delivered") || mergeFlag(flags) != "" {
		t.Fatalf("flags = %v, want delivered with no merge claim", flags)
	}

	fleet := newStatusCmd()
	var fleetOut bytes.Buffer
	fleet.SetOut(&fleetOut)
	if err := fleet.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := fleetFlags(t, fleetOut.String(), "task-1"); !slices.Contains(got, "delivered") {
		t.Fatalf("flags = %v, want a delivered marker in the fleet view", got)
	}

	asJSON := newStatusCmd()
	var jsonOut bytes.Buffer
	asJSON.SetOut(&jsonOut)
	asJSON.SetArgs([]string{"task-1", "--json"})
	if err := asJSON.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jsonOut.String(), `"delivered_reason": "offered upstream, maintainer decides"`) {
		t.Fatalf("got %q, want the delivered fields in JSON", jsonOut.String())
	}
	if !strings.Contains(jsonOut.String(), `"merged": false`) {
		t.Fatalf("got %q, want merged still false", jsonOut.String())
	}
}

func TestStatusSingleTaskJSONIncludesHeld(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "idle")

	if err := state.Write(home, state.Task{ID: "task-1", Herdr: state.Herdr{PaneID: "wA:pB"}}); err != nil {
		t.Fatal(err)
	}
	if err := state.SetHold(home, state.Hold{ID: "task-1", Kind: state.HoldKindOperator,
		Reason: "needs a call", SetAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"task-1", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"held"`) || !strings.Contains(out.String(), `"needs a call"`) {
		t.Fatalf("got %q, want the held hold in JSON", out.String())
	}
}

// The same false-all-clear risk as the fleet view, one task at a time.
func TestStatusSingleTaskPropagatesAnUnreadableHoldStore(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "idle")

	if err := state.Write(home, state.Task{ID: "task-1", Herdr: state.Herdr{PaneID: "wA:pB"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(store.Path(home)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(store.Path(home), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"task-1"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("got nil error, want the unreadable hold store to fail the command")
	}
}

func TestStatusFleetEmptyIsAPositiveStatement(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	want := "count: 0\n" +
		"attention: 0\n" +
		"held: 0\n" +
		"tasks[0]{id,state,reported,age,flags}:\n" +
		"holds[0]{id,kind,detail,age}:\n" +
		"help[2]:\n" +
		"  - Run `hand project list` to see which projects are registered\n" +
		"  - Run `hand spawn <id> <project>` to start a worker\n"
	if got := out.String(); got != want {
		t.Fatalf("got %q, want an explicit zero count, both schema headers, and where to go next:\n%q", got, want)
	}
}

func TestAppendFleetStateKeepsTheStatusBlocksExact(t *testing.T) {
	views := []taskView{{task: state.Task{ID: "task-1"}, agentState: "working", unacked: true}}
	cols := []axi.Column[taskView]{{Name: "id", Value: func(v taskView) string { return v.task.ID }}}
	var doc axi.Doc
	if attention := appendFleetState(&doc, views, nil, cols); attention != 1 {
		t.Fatalf("attention = %d, want 1", attention)
	}
	want := "count: 1\n" +
		"attention: 1\n" +
		"held: 0\n" +
		"tasks[1]{id}:\n" +
		"  task-1\n" +
		"holds[0]{id,kind,detail,age}:\n"
	if got := doc.String(); got != want {
		t.Fatalf("fleet state = %q, want the status blocks unchanged %q", got, want)
	}
}

// --fields narrows what is emitted, and the schema header has to narrow with
// it: a header promising columns the rows do not carry is worse than no header.
func TestStatusFleetFieldsNarrowsTheSchemaHeaderWithTheRows(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "working")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--fields", "state,id"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "tasks[1]{state,id}:\n  working,task-1\n") {
		t.Fatalf("got %q, want the header and the row narrowed to the requested fields, in that order", out.String())
	}
}

func TestStatusFieldsRejectsAnUnknownFieldAsAUsageError(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)

	cmd := newStatusCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--fields", "id,nope"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("got nil error, want an unknown field rejected rather than silently dropped")
	}
	if code := exitCodeFor(t, err); code != 2 {
		t.Fatalf("exit code = %d, want 2 for a usage error", code)
	}
	if !strings.Contains(err.Error(), `"nope"`) || !strings.Contains(err.Error(), "id, project") {
		t.Fatalf("got %v, want the bad field named alongside the ones that exist", err)
	}
}

// The name is checked before the home is resolved, so a flag typo never pays
// for a fleet scan, a registry warning, or a no-mistakes subprocess per done
// ship task before it is told what it got wrong.
func TestStatusFieldsIsRejectedBeforeTheHomeIsResolved(t *testing.T) {
	t.Setenv("HAND_HOME", "")
	t.Chdir(t.TempDir())

	cmd := newStatusCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--fields", "nope"})
	err := cmd.Execute()
	if code := exitCodeFor(t, err); code != 2 {
		t.Fatalf("err = %v, exit code = %d, want the usage error rather than the unresolvable home's 3", err, code)
	}
}

// --fields narrows the TOON schema; accepting it next to --json and then
// ignoring it would hand back the full object the caller asked to narrow.
func TestStatusFieldsWithJSONIsAUsageError(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)

	cmd := newStatusCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--fields", "id", "--json"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("got nil error, want --fields and --json rejected together")
	}
	if code := exitCodeFor(t, err); code != 2 {
		t.Fatalf("exit code = %d, want 2 for a usage error", code)
	}
}

func TestStatusFleetEmptyJSONCarriesACount(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"task_count": 0`) {
		t.Fatalf("got %q, want an explicit task_count of 0", out.String())
	}
}

// An empty fleet must never read as "nothing to see" when a hold is still open on a torn-down
// task's id - the exact false-all-clear atqamz/secondhand#63 guards against.
func TestStatusFleetEmptyStillShowsHeldBlock(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	if err := state.SetHold(home, state.Hold{ID: "gone-task", Kind: state.HoldKindOperator,
		Reason: "needs a call", SetAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "count: 0\n") || !strings.Contains(got, "held: 1\n") ||
		!strings.Contains(got, "  gone-task,operator,needs a call,") {
		t.Fatalf("got %q, want both the no-tasks count and the held row", got)
	}
}

// Registers a no-mistakes-mode project and creates its clone directory, so gateRunIssue's
// os.Stat(clonePath) check and its no-mistakes invocation both have something to find.
func registerNoMistakesProject(t *testing.T, home, name string) {
	t.Helper()
	if err := project.Add(home, project.Project{Name: name, URL: "https://example.com/" + name + ".git", Mode: project.ModeNoMistakes}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "projects", name), 0o755); err != nil {
		t.Fatal(err)
	}
}

// Writes a single done report line, so LastReportedState reads the task as reported done.
func writeDoneReport(t *testing.T, home, id, note string) {
	t.Helper()
	if err := os.WriteFile(state.ReportPath(home, id), []byte("done: "+note+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

const gateRunTestPR = "https://github.com/atqamz/secondhand/pull/120"

func TestStatusFleetFlagsShippedPRWithNoGateRun(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	registerNoMistakesProject(t, home, "gated")
	t.Setenv("PATH", fakeNoMistakesPath(t, "  completed    other-branch   758d72bf  2026-08-03 04:29  https://github.com/atqamz/secondhand/pull/999\n"))

	if err := state.Write(home, state.Task{ID: "task-1", Project: "gated", Kind: state.KindShip,
		PR: gateRunTestPR, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	writeDoneReport(t, home, "task-1", "PR "+gateRunTestPR+" checks green")

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := fleetFlags(t, out.String(), "task-1"); !slices.Contains(got, "gate-no-run-found") {
		t.Fatalf("flags = %v, want a gate marker naming the shipped PR never ran through the gate", got)
	}
}

func TestStatusFleetJSONFlagsShippedPRWithNoGateRun(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	registerNoMistakesProject(t, home, "gated")
	t.Setenv("PATH", fakeNoMistakesPath(t, "  completed    other-branch   758d72bf  2026-08-03 04:29  https://github.com/atqamz/secondhand/pull/999\n"))

	if err := state.Write(home, state.Task{ID: "task-1", Project: "gated", Kind: state.KindShip,
		PR: gateRunTestPR, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	writeDoneReport(t, home, "task-1", "PR "+gateRunTestPR+" checks green")

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"gate_run_issue": "no run found"`) {
		t.Fatalf("got %q, want gate_run_issue naming no run found", out.String())
	}
}

func TestStatusFleetNoGateMarkerWhenRunFound(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	registerNoMistakesProject(t, home, "gated")
	t.Setenv("PATH", fakeNoMistakesPath(t, "  completed    97-gate-visibility   758d72bf  2026-08-03 04:29  "+gateRunTestPR+"\n"))

	if err := state.Write(home, state.Task{ID: "task-1", Project: "gated", Kind: state.KindShip,
		PR: gateRunTestPR, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	writeDoneReport(t, home, "task-1", "PR "+gateRunTestPR+" checks green")

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "gate-") {
		t.Fatalf("got %q, want no gate marker once a completed run recorded this PR", out.String())
	}
}

func TestStatusFleetGateRunUnreachableWhenNoMistakesBinaryMissing(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	registerNoMistakesProject(t, home, "gated")
	t.Setenv("PATH", t.TempDir())

	if err := state.Write(home, state.Task{ID: "task-1", Project: "gated", Kind: state.KindShip,
		PR: gateRunTestPR, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	writeDoneReport(t, home, "task-1", "PR "+gateRunTestPR+" checks green")

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := fleetFlags(t, out.String(), "task-1"); !slices.Contains(got, "gate-unreachable") {
		t.Fatalf("flags = %v, want the gate check named unreachable rather than the stronger no-run-found claim", got)
	}
}

// A scout task with a PR-like field unset and no report at all: the check has nothing to say about a
// task that never shipped, so it must stay silent rather than misreport it as an ungated ship.
func TestStatusFleetSkipsGateCheckWhenItDoesNotApply(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	registerNoMistakesProject(t, home, "gated")
	t.Setenv("PATH", t.TempDir())

	if err := state.Write(home, state.Task{ID: "task-1", Project: "gated", Kind: state.KindScout,
		CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "gate-") {
		t.Fatalf("got %q, want no gate marker for a scout task with no shipped PR", out.String())
	}
}

func TestStatusSingleTaskFlagsShippedPRWithNoGateRun(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	registerNoMistakesProject(t, home, "gated")
	t.Setenv("PATH", fakeNoMistakesPath(t, "  completed    other-branch   758d72bf  2026-08-03 04:29  https://github.com/atqamz/secondhand/pull/999\n"))

	if err := state.Write(home, state.Task{ID: "task-1", Project: "gated", Kind: state.KindShip,
		PR: gateRunTestPR, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	writeDoneReport(t, home, "task-1", "PR "+gateRunTestPR+" checks green")

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"task-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := detailField(t, out.String(), "gate"); got != "no run found" {
		t.Fatalf("gate = %q, want it naming that the shipped PR never ran through the gate", got)
	}
}

func TestStatusSingleTaskJSONFlagsShippedPRWithNoGateRun(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	registerNoMistakesProject(t, home, "gated")
	t.Setenv("PATH", fakeNoMistakesPath(t, "  completed    other-branch   758d72bf  2026-08-03 04:29  https://github.com/atqamz/secondhand/pull/999\n"))

	if err := state.Write(home, state.Task{ID: "task-1", Project: "gated", Kind: state.KindShip,
		PR: gateRunTestPR, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	writeDoneReport(t, home, "task-1", "PR "+gateRunTestPR+" checks green")

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"task-1", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"gate_run_issue": "no run found"`) {
		t.Fatalf("got %q, want gate_run_issue naming no run found", out.String())
	}
}

func TestStatusSingleTaskNoGateLineWhenRunFound(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	registerNoMistakesProject(t, home, "gated")
	t.Setenv("PATH", fakeNoMistakesPath(t, "  completed    97-gate-visibility   758d72bf  2026-08-03 04:29  "+gateRunTestPR+"\n"))

	if err := state.Write(home, state.Task{ID: "task-1", Project: "gated", Kind: state.KindShip,
		PR: gateRunTestPR, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	writeDoneReport(t, home, "task-1", "PR "+gateRunTestPR+" checks green")

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"task-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := detailField(t, out.String(), "gate"); got != "none" {
		t.Fatalf("gate = %q, want none once a completed run recorded this PR", got)
	}
}

// fakeNoMistakesPath plus an append to countFile per invocation, so a test can assert how many
// no-mistakes processes one render actually spawned.
func countingNoMistakesPath(t *testing.T, stdout, countFile string) string {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\necho x >> " + countFile + "\ncat <<'EOF'\n" + stdout + "\nEOF\n"
	if err := os.WriteFile(filepath.Join(bin, "no-mistakes"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin + string(os.PathListSeparator) + os.Getenv("PATH")
}

// Pins the per-clone caching: without it every done ship task on one project spawns its own
// `no-mistakes runs` and re-parses identical output, on the command CLAUDE.md makes the first step of
// every session.
func TestStatusFleetAsksNoMistakesOncePerProject(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	registerNoMistakesProject(t, home, "gated")
	countFile := filepath.Join(t.TempDir(), "calls")
	t.Setenv("PATH", countingNoMistakesPath(t, "  completed    other-branch   758d72bf  2026-08-03 04:29  https://github.com/atqamz/secondhand/pull/999\n", countFile))

	for _, id := range []string{"task-1", "task-2", "task-3"} {
		if err := state.Write(home, state.Task{ID: id, Project: "gated", Kind: state.KindShip,
			PR: gateRunTestPR, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
			t.Fatal(err)
		}
		writeDoneReport(t, home, id, "PR "+gateRunTestPR+" checks green")
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), " gate-no-run-found\n") != 3 {
		t.Fatalf("got %q, want all three ungated tasks marked", out.String())
	}
	calls, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(strings.Fields(string(calls))); got != 1 {
		t.Fatalf("no-mistakes ran %d times for one project, want 1", got)
	}
}

// Writes a data/projects.md line the registry parser rejects, so project.List and project.Find both
// fail on this home.
func writeBrokenRegistry(t *testing.T, home string) {
	t.Helper()
	if err := os.WriteFile(project.RegistryPath(home), []byte("- broken line with no url or mode\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// An unreadable registry silently drops every gate flag fleet-wide, which renders an
// ungated PR as clean - so the overview still prints, but says on stderr that it did.
func TestStatusFleetNamesAnUnreadableRegistryOnStderr(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeBrokenRegistry(t, home)
	if err := state.Write(home, state.Task{ID: "task-1", Project: "gated", Kind: state.KindShip,
		PR: gateRunTestPR, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	writeDoneReport(t, home, "task-1", "PR "+gateRunTestPR+" checks green")

	cmd := newStatusCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("got %v, want a registry fault to degrade the gate check rather than fail the overview", err)
	}
	if !strings.Contains(out.String(), "task-1") {
		t.Fatalf("got %q, want the task table to still print", out.String())
	}
	if !strings.Contains(errOut.String(), "warning:") || !strings.Contains(errOut.String(), "registry") {
		t.Fatalf("got stderr %q, want a warning naming the unreadable project registry", errOut.String())
	}
}

// A scout task on a home whose registry does not parse: the gate-run check has nothing to say about
// it, so the detail view must not fail over a lookup it never needed.
func TestStatusSingleTaskReadsRegistryOnlyWhenTheGateCheckApplies(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeBrokenRegistry(t, home)
	if err := state.Write(home, state.Task{ID: "scout-1", Project: "gated", Kind: state.KindScout,
		CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"scout-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("got %v, want the detail view to print without reading the registry", err)
	}
	if got := detailField(t, out.String(), "id"); got != "scout-1" {
		t.Fatalf("id = %q, want the detail view rendered", got)
	}
}

// The counterpart: when the check does apply, the same unreadable registry fails the command rather
// than silently degrading to no marker - this id's own project is the one fact the check is about.
func TestStatusSingleTaskPropagatesAnUnreadableRegistryWhenTheGateCheckApplies(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeBrokenRegistry(t, home)
	if err := state.Write(home, state.Task{ID: "task-1", Project: "gated", Kind: state.KindShip,
		PR: gateRunTestPR, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	writeDoneReport(t, home, "task-1", "PR "+gateRunTestPR+" checks green")

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"task-1"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("got nil error, want the unreadable registry to fail the check it is needed for")
	}
}

// The uninitialized-gate half of the unreachable bucket: no-mistakes still holds that repo's completed
// runs, so reading its refusal as an empty run list would report a genuinely gated PR as never gated.
func TestStatusGateRunUnreachableWhenGateNotInitialized(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	registerNoMistakesProject(t, home, "gated")
	t.Setenv("PATH", fakeNoMistakesPathExit(t, "repo not initialized (run 'no-mistakes init' first)", 1))

	if err := state.Write(home, state.Task{ID: "task-1", Project: "gated", Kind: state.KindShip,
		PR: gateRunTestPR, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	writeDoneReport(t, home, "task-1", "PR "+gateRunTestPR+" checks green")

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := fleetFlags(t, out.String(), "task-1"); !slices.Contains(got, "gate-unreachable") {
		t.Fatalf("flags = %v, want an uninitialized gate read as unreachable, never as no run found", got)
	}
}

// The whole point of atqamz/secondhand#70: a worker that finished while nobody was
// attached has to be visible in the next hand status, not only in the stream of the
// watcher that was not running.
func TestStatusFleetFlagsATerminalReportNoWatcherConsumed(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "idle")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	writeDoneReport(t, home, "task-1", "PR up")

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if row := fleetRow(t, out.String(), "task-1"); !strings.HasPrefix(row, "task-1,idle,done,") || !strings.HasSuffix(row, ",unacknowledged") {
		t.Fatalf("got %q, want the done report flagged unacknowledged", row)
	}
}

func TestStatusFleetDoesNotFlagATerminalReportAWatcherConsumed(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "idle")

	report := "done: PR up\n"
	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z",
		ReportOffset: int64(len(report))}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state.ReportPath(home, "task-1"), []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	row := fleetRow(t, out.String(), "task-1")
	if !strings.HasPrefix(row, "task-1,idle,done,") {
		t.Fatalf("got %q, want the reported state still shown", row)
	}
	if strings.Contains(row, "unacknowledged") {
		t.Fatalf("got %q, want no flag on a report a watcher already announced", row)
	}
}

func TestStatusFleetJSONFlagsATerminalReportNoWatcherConsumed(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "idle")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	writeDoneReport(t, home, "task-1", "PR up")

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"unacknowledged": true`) {
		t.Fatalf("got %q, want unacknowledged true", out.String())
	}
}

func TestStatusSingleTaskFlagsATerminalReportNoWatcherConsumed(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "idle")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	writeDoneReport(t, home, "task-1", "PR up")

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"task-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "done: PR up (unacknowledged)") {
		t.Fatalf("got %q, want the detail view to flag the unread completion too", out.String())
	}
}

// Free text appended after a real report is expected traffic that must never erase it, and enough of
// it used to push the terminal line out of the detail view's 5-line history window - the one view
// deriving the flag from that window instead of the whole file, so it alone called it acknowledged.
func TestStatusSingleTaskFlagsATerminalReportBeyondTheHistoryWindow(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "idle")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	report := "done: PR up\nstill tidying\nand more\nand more\nand more\nand more\n"
	if err := os.WriteFile(state.ReportPath(home, "task-1"), []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"task-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "done: PR up (unacknowledged)") {
		t.Fatalf("got %q, want the unread completion flagged however much free text follows it", out.String())
	}
}

// The clause has to qualify the state it describes: the flag comes from the
// last line that classified, so the line it decorates must be that one and not
// whatever the worker appended after it.
func TestStatusSingleTaskFlagsTheClassifiedLineNotTrailingFreeText(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "idle")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state.ReportPath(home, "task-1"), []byte("done: PR up\nstill tidying\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"task-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := detailField(t, out.String(), "report"); got != "done: PR up (unacknowledged)" {
		t.Fatalf("report = %q, want the clause on the terminal report it describes", got)
	}
	if !strings.Contains(out.String(), "report_history[1]:\n  - still tidying\n") {
		t.Fatalf("got %q, want the worker's trailing free text still in the history block", out.String())
	}
}

// Without the flag the Reported line stays the worker's literal last line, free
// text included.
func TestStatusSingleTaskShowsTrailingFreeTextWhenAcknowledged(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "idle")

	report := "done: PR up\nstill tidying\n"
	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z",
		ReportOffset: int64(len(report))}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state.ReportPath(home, "task-1"), []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"task-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := detailField(t, out.String(), "report"); got != "still tidying" {
		t.Fatalf("report = %q, want the literal last line when no clause applies", got)
	}
}

func TestStatusSingleTaskJSONOmitsUnacknowledgedWhenAcknowledged(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "idle")

	report := "done: PR up\n"
	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z",
		ReportOffset: int64(len(report))}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state.ReportPath(home, "task-1"), []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"task-1", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "unacknowledged") {
		t.Fatalf("got %q, want the field absent for a consumer that predates it", out.String())
	}
}

// A worker whose append lands between the row's own read and the flag's read is the one way the two
// can disagree, and it cannot be staged through the command itself - so the guard is exercised where
// it lives.
func TestUnacknowledgedAnswersForTheStateTheRowPrints(t *testing.T) {
	home := t.TempDir()
	mkFleetDirs(t, home)
	if err := os.WriteFile(state.ReportPath(home, "task-1"), []byte("done: PR up\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	task := state.Task{ID: "task-1"}

	cases := []struct {
		name     string
		reported state.ReportLine
		ok       bool
		want     bool
	}{
		{name: "row prints the terminal state", reported: state.ReportLine{State: state.ReportDone}, ok: true, want: true},
		// A --json row saying "unacknowledged" next to a "working" it reported in the same breath is
		// contradictory on the interface the supervisor reads on every check.
		{name: "row prints work that supersedes it", reported: state.ReportLine{State: state.ReportWorking}, ok: true},
		{name: "row prints nothing classified", reported: state.ReportLine{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := unacknowledged(home, task, c.reported, c.ok, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

// The watcher leaves an unterminated line for its next tick, so a done written without a trailing
// newline has been announced to nobody. Flagging it is the whole of atqamz/secondhand#70; skipping it
// would let the same silent completion back in through the newline.
func TestStatusFleetFlagsATerminalReportWithNoTrailingNewline(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	writeFakeHerdrPaneStatus(t, "idle")

	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip,
		Herdr: state.Herdr{PaneID: "wA:pB"}, CreatedAt: "2026-07-24T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state.ReportPath(home, "task-1"), []byte("done: PR up"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if row := fleetRow(t, out.String(), "task-1"); !strings.HasPrefix(row, "task-1,idle,done,") || !strings.HasSuffix(row, ",unacknowledged") {
		t.Fatalf("got %q, want an unterminated done flagged like any other unread completion", row)
	}
}
