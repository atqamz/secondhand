package agentsmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func makeWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state", "hand.db"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRefreshSkipsSilentlyWhenNotAFleetHome(t *testing.T) {
	dir := t.TempDir()
	refreshed, err := Refresh(dir)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed {
		t.Fatal("got refreshed=true, want false outside a fleet home")
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("got AGENTS.md written outside a fleet home, err=%v", err)
	}
}

func TestRefreshWritesAgentsMdAndClaudeSymlinkWhenMissing(t *testing.T) {
	dir := makeWorkspace(t)

	refreshed, err := Refresh(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !refreshed {
		t.Fatal("got refreshed=false, want true")
	}

	got, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), beginMarker) || !strings.Contains(string(got), endMarker) {
		t.Fatalf("got %q, want generated markers present", got)
	}
	if !strings.Contains(string(got), "## Secondhand supervisor bootstrap") ||
		!strings.Contains(string(got), "hand session start") ||
		!strings.Contains(string(got), "HAND_ROLE=worker") {
		t.Fatalf("got %q, want the compact supervisor bootstrap", got)
	}
	if strings.Contains(string(got), "## Workflow") || strings.Contains(string(got), "## Rules") {
		t.Fatalf("got %q, want no durable operating manual in the managed block", got)
	}

	link, err := os.Readlink(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if link != "AGENTS.md" {
		t.Fatalf("got CLAUDE.md -> %q, want AGENTS.md", link)
	}
}

func TestSupervisorInstructionsCoverDurableOperatingContract(t *testing.T) {
	got := strings.Join(SupervisorInstructions(), "\n")
	for _, want := range []string{
		"data/operator.md",
		"data/projects.md",
		"data/backlog.md",
		"data/<id>/brief.md",
		"state/<id>.status",
		"working:",
		"paused:",
		"blocked:",
		"needs-decision:",
		"done:",
		"failed:",
		"re-arm",
		"Never merge without explicit authorization",
		"hand deliver",
		"Never edit files under `projects/`",
		"full and absolute, never relative",
		"hand hold set",
		"hand hold clear",
		"data/done-archive.md",
		"data/note-archive.md",
		"data/learnings.md",
		"Only a `hand send` message carries an operator decision",
		"TOON",
		"Branch on `kind`",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("got supervisor instructions %q, want durable rule %q", got, want)
		}
	}
}

func TestSupervisorInstructionsReturnsClone(t *testing.T) {
	first := SupervisorInstructions()
	first[0] = "changed by caller"
	if got := SupervisorInstructions()[0]; got == "changed by caller" {
		t.Fatal("SupervisorInstructions exposed mutable package state")
	}
}

// This is the requirement most likely to regress silently: a refresh must
// never wipe out rules or sections the user appended by hand.
func TestRefreshPreservesUserAddedContentAcrossRefresh(t *testing.T) {
	dir := makeWorkspace(t)
	path := filepath.Join(dir, "AGENTS.md")

	userPreamble := "# House rules\n\nRead this before the generated block.\n\n"
	userContent := "\n- a project-specific rule the user wrote by hand\n\n## Maintaining this file\n\nKeep this file tidy.\n"
	stale := userPreamble + beginMarker + "\n# Secondhand\n\nAn out-of-date template.\n" + endMarker + userContent
	if err := os.WriteFile(path, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	refreshed, err := Refresh(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !refreshed {
		t.Fatal("got refreshed=false, want true when the generated block was stale")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), userPreamble) {
		t.Fatalf("got %q, want user content before the markers preserved verbatim", got)
	}
	if !strings.HasSuffix(string(got), userContent) {
		t.Fatalf("got %q, want user content after the markers preserved verbatim", got)
	}
	if !strings.Contains(string(got), "## Secondhand supervisor bootstrap") {
		t.Fatalf("got %q, want the current generated bootstrap", got)
	}
	if strings.Contains(string(got), "An out-of-date template.") {
		t.Fatalf("got %q, want the stale generated block replaced", got)
	}
}

func TestRefreshAppendsManagedBlockToUnmarkedAgentsMd(t *testing.T) {
	for name, original := range map[string]string{
		"trailing newline":    "# Project rules\n\nKeep this byte-for-byte.\n",
		"no trailing newline": "# Project rules\n\nKeep this byte-for-byte.",
	} {
		t.Run(name, func(t *testing.T) {
			dir := makeWorkspace(t)
			path := filepath.Join(dir, "AGENTS.md")
			if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
				t.Fatal(err)
			}

			changed, err := Refresh(dir)
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !changed || !strings.HasPrefix(string(got), original) || !strings.Contains(string(got), beginMarker) {
				t.Fatalf("got %q, want unchanged prefix plus managed block", got)
			}
		})
	}
}

func TestRefreshRefusesDuplicateReversedOrUnpairedMarkersWithoutWrites(t *testing.T) {
	for name, content := range map[string]string{
		"missing end": beginMarker + "\nmissing end\n",
		"end only":    endMarker + "\n",
		"reversed":    endMarker + "\n" + beginMarker + "\n",
		"duplicate":   generatedBlock() + generatedBlock(),
	} {
		t.Run(name, func(t *testing.T) {
			dir := makeWorkspace(t)
			path := filepath.Join(dir, "AGENTS.md")
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Refresh(dir); err == nil {
				t.Fatal("Refresh succeeded, want malformed-marker error")
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != content {
				t.Fatalf("got %q, want unchanged %q", got, content)
			}
			if _, err := os.Lstat(filepath.Join(dir, "CLAUDE.md")); !os.IsNotExist(err) {
				t.Fatalf("got CLAUDE.md change after malformed input, err=%v", err)
			}
		})
	}
}

// An already-current AGENTS.md must not be rewritten at all: an identical-bytes write
// still swaps the inode, resets the mode, and turns a symlink into a regular file.
func TestRefreshLeavesUpToDateFileOnDiskUntouched(t *testing.T) {
	dir := makeWorkspace(t)
	path := filepath.Join(dir, "AGENTS.md")

	if _, err := Refresh(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	refreshed, err := Refresh(dir)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed {
		t.Fatal("got refreshed=true, want false when the template is already current")
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("got AGENTS.md replaced, want the existing file left untouched")
	}
	if after.Mode().Perm() != 0o600 {
		t.Fatalf("got mode %v, want the existing 0600 preserved", after.Mode().Perm())
	}
}

// The source checkout is also a dogfood fleet home, so its project-owned rules point at the
// authority while its managed span stays current enough for initialization to be a no-op.
func TestThisRepoAgentsMdIsCurrentForDogfood(t *testing.T) {
	repoCopy, err := os.ReadFile(filepath.Join("..", "..", "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(repoCopy), "generatedBody") {
		t.Fatalf("got repo AGENTS.md %q, want a pointer to generatedBody", repoCopy)
	}
	merged, err := mergeGenerated(string(repoCopy))
	if err != nil {
		t.Fatal(err)
	}
	if merged != string(repoCopy) {
		t.Fatalf("got repo AGENTS.md %q, want its managed block already current", repoCopy)
	}
}

// atqamz/secondhand#87's fix has to reach every worker without a new report-vocabulary word, since the
// watcher's classifier and hand status's renderer were outside that change's scope: the working: prefix
// plus a first-person convention was the only lever available.
func TestGeneratedRulesCoverSelfDecidedCallsInFirstPerson(t *testing.T) {
	instructions := strings.Join(SupervisorInstructions(), "\n")
	if !strings.Contains(instructions, "hand send") || !strings.Contains(instructions, "operator decision") {
		t.Fatalf("got instructions %q, want the hand send invariant", instructions)
	}
	if !strings.Contains(instructions, "working: deciding myself:") {
		t.Fatalf("got instructions %q, want the first-person working: convention", instructions)
	}
}

// atqamz/secondhand#114: a fleet agent following the template had no reason
// to reach for hand hold, since every "waiting on" case routed through
// data/backlog.md and hand send.
func TestGeneratedRulesCoverHolds(t *testing.T) {
	instructions := strings.Join(SupervisorInstructions(), "\n")
	if !strings.Contains(instructions, "hand hold set") || !strings.Contains(instructions, "hand hold clear") {
		t.Fatalf("got instructions %q, want hand hold set and hand hold clear", instructions)
	}
}

// atqamz/secondhand#47: the four files hand init seeds are inert unless the template says
// who reads each one and when. atqamz/secondhand#64: the one direction data/ does not carry
// has to be stated, or the agent invents a hand-written operator channel again.
func TestGeneratedRulesCoverOperatorContextLearningsAndArchives(t *testing.T) {
	instructions := strings.Join(SupervisorInstructions(), "\n")
	for _, want := range []string{
		"data/operator.md",
		"data/learnings.md",
		"data/done-archive.md",
		"data/note-archive.md",
		"written for the operator to read",
		"never rewrite it",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("got instructions %q, want it to name %q", instructions, want)
		}
	}
	if strings.Contains(instructions, "data/inbox.md") {
		t.Fatalf("got instructions %q, want no hand-written operator channel", instructions)
	}
}

func TestRefreshDoesNotOverwriteExistingClaudeSymlink(t *testing.T) {
	dir := makeWorkspace(t)
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("placeholder\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "OTHER.md"), []byte("other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("OTHER.md", filepath.Join(dir, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}

	if _, err := Refresh(dir); err != nil {
		t.Fatal(err)
	}

	link, err := os.Readlink(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if link != "OTHER.md" {
		t.Fatalf("got CLAUDE.md -> %q, want unchanged OTHER.md", link)
	}
}

func hasViolation(violations []Violation, substr string) bool {
	for _, v := range violations {
		if strings.Contains(v.Text, substr) {
			return true
		}
	}
	return false
}

func TestCheckSkipsNonFleetHome(t *testing.T) {
	violations, err := Check(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if violations != nil {
		t.Fatalf("got %v, want nil outside a fleet home", violations)
	}
}

func TestCheckFlagsMissingAgentsFile(t *testing.T) {
	dir := makeWorkspace(t)
	violations, err := Check(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 1 || !hasViolation(violations, "AGENTS.md is missing") {
		t.Fatalf("got %v, want one missing-file violation", violations)
	}
	if violations[0].Severity != SeverityViolation {
		t.Fatalf("got severity %v, want SeverityViolation", violations[0].Severity)
	}
}

func TestCheckCleanRightAfterRefresh(t *testing.T) {
	dir := makeWorkspace(t)
	if _, err := Refresh(dir); err != nil {
		t.Fatal(err)
	}
	violations, err := Check(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) != 0 {
		t.Fatalf("got %v, want no violations right after Refresh", violations)
	}
}

func TestCheckFlagsDateOutsideGeneratedBlock(t *testing.T) {
	dir := makeWorkspace(t)
	if _, err := Refresh(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, filename)
	appendLine(t, path, "Fixed the race condition on 2026-07-29.")

	violations, err := Check(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !hasViolation(violations, "date outside the generated block") {
		t.Fatalf("got %v, want a date violation", violations)
	}
}

func TestCheckIgnoresDateInCodeSpanOrURL(t *testing.T) {
	dir := makeWorkspace(t)
	if _, err := Refresh(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, filename)
	appendLine(t, path, "Example: `2026-07-29` is an RFC3339 date, see https://example.com/releases/2026-07-29 for the tag.")

	violations, err := Check(dir)
	if err != nil {
		t.Fatal(err)
	}
	if hasViolation(violations, "date outside the generated block") {
		t.Fatalf("got %v, want no date violation for a code span or URL", violations)
	}
}

func TestCheckFlagsSelfExpiringPhrasing(t *testing.T) {
	dir := makeWorkspace(t)
	if _, err := Refresh(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, filename)
	appendLine(t, path, "Until #84 lands, do the mtime check by hand.")

	violations, err := Check(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !hasViolation(violations, "self-expiring phrasing") {
		t.Fatalf("got %v, want a self-expiring-phrasing violation", violations)
	}
}

func TestCheckFlagsEmDashAndEmoji(t *testing.T) {
	dir := makeWorkspace(t)
	if _, err := Refresh(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, filename)
	appendLine(t, path, "This rule matters a lot — so don't skip it")
	appendLine(t, path, "Ship it \U0001F680")

	violations, err := Check(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n := countViolations(violations, "banned character"); n != 2 {
		t.Fatalf("got %d banned-character violations in %v, want 2 (em dash and emoji)", n, violations)
	}
}

func TestCheckFlagsBannedCharacterInsideFence(t *testing.T) {
	dir := makeWorkspace(t)
	if _, err := Refresh(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, filename)
	appendLine(t, path, "```\nan example — with an em dash\n```")

	violations, err := Check(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n := countViolations(violations, "banned character"); n != 1 {
		t.Fatalf("got %d banned-character violations in %v, want 1 inside a fenced block", n, violations)
	}
}

func countViolations(violations []Violation, substr string) int {
	n := 0
	for _, v := range violations {
		if strings.Contains(v.Text, substr) {
			n++
		}
	}
	return n
}

func TestCheckFlagsDuplicateReversedAndUnpairedMarkers(t *testing.T) {
	tests := []struct {
		name    string
		content string
		line    int
		finding string
	}{
		{"unpaired start", "preamble\n" + beginMarker + "\nbody\n", 2, "unpaired hand:generated start marker"},
		{"unpaired end", "preamble\n" + endMarker + "\n", 2, "unpaired hand:generated end marker"},
		{"reversed", "preamble\n" + endMarker + "\n" + beginMarker + "\n", 2, "end marker appears before start marker"},
		{"duplicate start", beginMarker + "\n" + beginMarker + "\n" + endMarker + "\n", 2, "duplicate hand:generated start marker"},
		{"duplicate end", beginMarker + "\n" + endMarker + "\n" + endMarker + "\n", 3, "duplicate hand:generated end marker"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := makeWorkspace(t)
			if err := os.WriteFile(filepath.Join(dir, filename), []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}

			violations, err := Check(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, violation := range violations {
				if strings.Contains(violation.Text, tt.finding) {
					if violation.Line != tt.line {
						t.Fatalf("got line %d for %q, want %d", violation.Line, violation.Text, tt.line)
					}
					return
				}
			}
			t.Fatalf("got %v, want finding %q", violations, tt.finding)
		})
	}
}

func TestCheckFlagsGeneratedBlockDrift(t *testing.T) {
	dir := makeWorkspace(t)
	if _, err := Refresh(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, filename)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	drifted := strings.Replace(string(content), "hand session start", "hand session begin", 1)
	if err := os.WriteFile(path, []byte(drifted), 0o644); err != nil {
		t.Fatal(err)
	}

	violations, err := Check(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !hasViolation(violations, "generated block has drifted") {
		t.Fatalf("got %v, want a generated-block-drift violation", violations)
	}
	// A bare "run hand init" would target the operator's working directory,
	// which is a new nested fleet home whenever that is not the home itself.
	if !hasViolation(violations, "run hand init '"+dir+"' to refresh") {
		t.Fatalf("got %v, want the remedy to name the resolved home %q", violations, dir)
	}
}

func TestCheckFlagsMissingGeneratedMarkers(t *testing.T) {
	dir := makeWorkspace(t)
	if err := os.WriteFile(filepath.Join(dir, filename), []byte("# Hand-written, no markers\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	violations, err := Check(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !hasViolation(violations, "no hand:generated markers") {
		t.Fatalf("got %v, want a missing-markers violation: Refresh declines to touch this file, so doctor must fail it", violations)
	}
	if hasViolation(violations, "generated block has drifted") {
		t.Fatalf("got %v, want no drift violation when there is no block to drift", violations)
	}
	for _, v := range violations {
		if strings.Contains(v.Text, "no hand:generated markers") && v.Severity != SeverityViolation {
			t.Fatalf("got severity %v for the missing-markers finding, want SeverityViolation", v.Severity)
		}
	}
}

func TestCheckRemediationCommandsQuoteFleetHomeForPOSIXShell(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "fleet path's `printf injected`;printf injected")
	for _, subdir := range []string{"data", "state"} {
		if err := os.MkdirAll(filepath.Join(dir, subdir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "state", "hand.db"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "hand"), []byte("#!/bin/sh\nprintf '%s\\n' \"$#\" \"$1\" \"$2\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	assertRecovery := func(finding string) {
		t.Helper()
		violations, err := Check(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, violation := range violations {
			if !strings.Contains(violation.Text, finding) {
				continue
			}
			start := strings.Index(violation.Text, "run ")
			end := strings.LastIndex(violation.Text, " to ")
			if start < 0 || end <= start {
				t.Fatalf("finding = %q, want an executable recovery command", violation.Text)
			}
			recovery := violation.Text[start+len("run ") : end]
			got, runErr := exec.Command("sh", "-c", recovery).CombinedOutput()
			if runErr != nil {
				t.Fatalf("run recovery %q: %v: %s", recovery, runErr, got)
			}
			want := "2\ninit\n" + dir + "\n"
			if string(got) != want {
				t.Fatalf("recovery argv = %q, want %q", got, want)
			}
			return
		}
		t.Fatalf("violations = %v, want finding %q", violations, finding)
	}

	assertRecovery("AGENTS.md is missing")
	if err := os.WriteFile(filepath.Join(dir, filename), []byte("# Hand-written, no markers\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertRecovery("no hand:generated markers")
	if _, err := Refresh(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, filename)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	drifted := strings.Replace(string(content), "hand session start", "hand session begin", 1)
	if err := os.WriteFile(path, []byte(drifted), 0o644); err != nil {
		t.Fatal(err)
	}
	assertRecovery("generated block has drifted")
}

func TestCheckFlagsUnterminatedFence(t *testing.T) {
	dir := makeWorkspace(t)
	if _, err := Refresh(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, filename)
	appendLine(t, path, "```")
	appendLine(t, path, "Fixed the race condition on 2026-07-29.")

	violations, err := Check(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !hasViolation(violations, "unterminated code fence") {
		t.Fatalf("got %v, want an unterminated-fence violation instead of a silently truncated scan", violations)
	}
}

func TestCheckIgnoresAwaitingWithNoIssueReference(t *testing.T) {
	dir := makeWorkspace(t)
	if _, err := Refresh(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, filename)
	appendLine(t, path, "Never merge a PR awaiting review.")

	violations, err := Check(dir)
	if err != nil {
		t.Fatal(err)
	}
	if hasViolation(violations, "self-expiring phrasing") {
		t.Fatalf("got %v, want no violation: a bare awaiting with no issue to expire against is durable prose", violations)
	}

	appendLine(t, path, "Skip the mtime check, awaiting #84.")
	violations, err = Check(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !hasViolation(violations, "self-expiring phrasing") {
		t.Fatalf("got %v, want an awaiting-#N violation", violations)
	}
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n" + line + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
