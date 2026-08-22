package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atqamz/hand/internal/agentsmd"
	"github.com/atqamz/hand/internal/axi"
	"github.com/atqamz/hand/internal/faketool"
	"github.com/atqamz/hand/internal/harness"
	"github.com/atqamz/hand/internal/project"
	"github.com/atqamz/hand/internal/routing"
	"github.com/atqamz/hand/internal/selfupdate"
	"github.com/atqamz/hand/internal/skill"
)

func TestDoctorFindingsCoverFleetHealth(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, home string)
		want  []doctorFinding
	}{
		{
			name: "clean fleet",
			setup: func(t *testing.T, home string) {
				t.Helper()
				if _, err := agentsmd.Refresh(home); err != nil {
					t.Fatal(err)
				}
				if _, err := skill.Refresh(home); err != nil {
					t.Fatal(err)
				}
				mustConfigSet(t, settingHarness, harness.Claude)
			},
			want: []doctorFinding{{Severity: doctorInfo, Text: `routing resolves through explicit legacy defaults: harness "claude"`}},
		},
		{
			name: "unreachable gate",
			setup: func(t *testing.T, home string) {
				t.Helper()
				if _, err := agentsmd.Refresh(home); err != nil {
					t.Fatal(err)
				}
				if _, err := skill.Refresh(home); err != nil {
					t.Fatal(err)
				}
				if err := project.Add(home, project.Project{Name: "gated", URL: "https://example.com/gated.git", Mode: project.ModeNoMistakes}); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", t.TempDir())
			},
			want: []doctorFinding{{Severity: doctorError, Text: `project "gated" no-mistakes gate is unreachable`}},
		},
		{
			name: "uninitialized gate",
			setup: func(t *testing.T, home string) {
				t.Helper()
				if _, err := agentsmd.Refresh(home); err != nil {
					t.Fatal(err)
				}
				if _, err := skill.Refresh(home); err != nil {
					t.Fatal(err)
				}
				if err := project.Add(home, project.Project{Name: "gated", URL: "https://example.com/gated.git", Mode: project.ModeNoMistakes}); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(home, "projects", "gated"), 0o755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", fakeNoMistakesPath(t, "repo not initialized"))
			},
			want: []doctorFinding{{Severity: doctorError, Text: `project "gated" no-mistakes gate is not initialized`}},
		},
		{
			name: "explicit legacy intent",
			setup: func(t *testing.T, home string) {
				t.Helper()
				if _, err := agentsmd.Refresh(home); err != nil {
					t.Fatal(err)
				}
				if _, err := skill.Refresh(home); err != nil {
					t.Fatal(err)
				}
				mustConfigSet(t, settingHarness, harness.Claude)
			},
			want: []doctorFinding{{Severity: doctorInfo, Text: `routing resolves through explicit legacy defaults: harness "claude"`}},
		},
		{
			name: "unstated legacy fallback",
			setup: func(t *testing.T, home string) {
				t.Helper()
				if _, err := agentsmd.Refresh(home); err != nil {
					t.Fatal(err)
				}
				if _, err := skill.Refresh(home); err != nil {
					t.Fatal(err)
				}
			},
			want: []doctorFinding{{Severity: doctorWarning, Text: `routing falls back to legacy defaults without explicit intent: harness "claude"`}},
		},
		{
			name: "partial routing config",
			setup: func(t *testing.T, home string) {
				t.Helper()
				if _, err := agentsmd.Refresh(home); err != nil {
					t.Fatal(err)
				}
				if _, err := skill.Refresh(home); err != nil {
					t.Fatal(err)
				}
				if err := routing.WriteProfile(home, routing.Profile{Name: "daily", Harness: harness.Claude}); err != nil {
					t.Fatal(err)
				}
				if err := routing.WriteRoute(home, routing.Route{Kind: routing.TaskKindScout, ExecutionClass: routing.ExecutionClassStandard, Profile: "daily"}); err != nil {
					t.Fatal(err)
				}
			},
			want: []doctorFinding{{Severity: doctorWarning, Text: "routing drift: route scout.mechanical is not configured"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Chdir(home)
			mkFleetDirs(t, home)
			t.Setenv("HAND_HARNESS", harness.Claude)
			tt.setup(t, home)

			findings, err := doctorFindings(home)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tt.want {
				if !hasDoctorFinding(findings, want) {
					t.Fatalf("findings = %#v, want %#v", findings, want)
				}
			}
		})
	}
}

func TestDoctorIncludesProjectListGateFinding(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	if _, err := agentsmd.Refresh(home); err != nil {
		t.Fatal(err)
	}
	if _, err := skill.Refresh(home); err != nil {
		t.Fatal(err)
	}
	mustConfigSet(t, settingHarness, harness.Claude)
	if err := project.Add(home, project.Project{Name: "gated", URL: "https://example.com/gated.git", Mode: project.ModeNoMistakes}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())

	findings, err := doctorFindings(home)
	if err != nil {
		t.Fatal(err)
	}
	if !hasDoctorFinding(findings, doctorFinding{Severity: doctorError, Text: `project "gated" no-mistakes gate is unreachable`}) {
		t.Fatalf("findings = %#v, want project list gate finding", findings)
	}
}

func hasDoctorFinding(findings []doctorFinding, want doctorFinding) bool {
	for _, finding := range findings {
		if finding.Severity == want.Severity && finding.Text == want.Text {
			return true
		}
	}
	return false
}

// A clean fleet still reports its effective routing decision rather than making
// an operator infer it from a lack of findings.
func TestDoctorCleanFleetReportsEffectiveRouting(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	if _, err := agentsmd.Refresh(home); err != nil {
		t.Fatal(err)
	}
	if _, err := skill.Refresh(home); err != nil {
		t.Fatal(err)
	}
	mustConfigSet(t, settingHarness, harness.Claude)
	// Every required tool present, so this run's only finding is the routing decision under
	// test - onPath only checks PATH presence, never invokes any of these, so zero-valued
	// fakes are enough.
	bin := faketool.Bin(t)
	faketool.Treehouse{}.Install(t, bin)
	faketool.Herdr{}.Install(t, bin)
	faketool.GH{}.Install(t, bin)

	var out bytes.Buffer
	cmd := newDoctorCmd(stableBuild("v0.1.0"))
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("got error %v, want nil for a clean AGENTS.md", err)
	}
	want := "file: " + axi.Value(filepath.Join(home, "AGENTS.md")) + "\n" +
		"version: v0.1.0\n" +
		"channel: stable\n" +
		"commit: unknown\n" +
		"distribution: \"\"\n" +
		"count: 1\n" +
		"violations: 0\n"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("stdout = %q, want it to contain %q", out.String(), want)
	}
	wantFindings := "findings[1]{line,severity,finding}:\n" +
		"  none,info," + axi.Value(`routing resolves through explicit legacy defaults: harness "claude"`) + "\n" +
		"help[1]:\n" +
		"  - No error findings, so this run passed; inspect warnings and info before the next dispatch\n"
	if !strings.Contains(out.String(), wantFindings) {
		t.Fatalf("stdout = %q, want it to contain %q", out.String(), wantFindings)
	}
}

func TestDoctorReportsConfiguredRoutingDecision(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	if _, err := agentsmd.Refresh(home); err != nil {
		t.Fatal(err)
	}
	if _, err := skill.Refresh(home); err != nil {
		t.Fatal(err)
	}
	if err := routing.WriteProfile(home, routing.Profile{Name: "daily", Harness: harness.Claude, Model: "opus", Effort: "high"}); err != nil {
		t.Fatal(err)
	}
	if err := routing.WriteRoute(home, routing.Route{Kind: routing.TaskKindScout, ExecutionClass: routing.ExecutionClassMechanical, Profile: "daily"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAND_HARNESS", harness.Claude)

	findings, err := doctorFindings(home)
	if err != nil {
		t.Fatal(err)
	}
	want := doctorFinding{Severity: doctorInfo, Text: `routing decision: scout.mechanical -> profile "daily" -> harness "claude", model "opus", effort "high"`}
	if !hasDoctorFinding(findings, want) {
		t.Fatalf("findings = %#v, want %q", findings, want.Text)
	}
}

func TestDoctorReportsUnresolvedConfiguredRoutingDecision(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	if _, err := agentsmd.Refresh(home); err != nil {
		t.Fatal(err)
	}
	if _, err := skill.Refresh(home); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "config", "routes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config", "routes", "ship.deep"), []byte("missing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAND_HARNESS", harness.Claude)

	findings, err := doctorFindings(home)
	if err != nil {
		t.Fatal(err)
	}
	want := doctorFinding{Severity: doctorWarning, Text: `routing decision: ship.deep -> unavailable (profile "missing" does not exist or is invalid)`}
	if !hasDoctorFinding(findings, want) {
		t.Fatalf("findings = %#v, want %q", findings, want.Text)
	}
}

func TestDoctorReportsMalformedRoutingBeforeEffectiveFallback(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	if _, err := agentsmd.Refresh(home); err != nil {
		t.Fatal(err)
	}
	if _, err := skill.Refresh(home); err != nil {
		t.Fatal(err)
	}
	profileDir := filepath.Join(home, "config", "profiles", "broken")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, "current"), []byte("missing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustConfigSet(t, settingHarness, harness.Claude)

	findings, err := doctorFindings(home)
	if err != nil {
		t.Fatal(err)
	}
	profileIndex := -1
	fallbackIndex := -1
	for i, finding := range findings {
		if strings.HasPrefix(finding.Text, "routing drift: profile") {
			profileIndex = i
		}
		if strings.HasPrefix(finding.Text, "routing effective fallback after configuration problems: harness") {
			fallbackIndex = i
		}
	}
	if profileIndex < 0 || fallbackIndex < 0 || profileIndex >= fallbackIndex {
		t.Fatalf("findings = %#v, want malformed profile before fallback", findings)
	}
}

func TestDoctorReportsViolationsAndExitsNonZero(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	if _, err := agentsmd.Refresh(home); err != nil {
		t.Fatal(err)
	}
	if _, err := skill.Refresh(home); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "AGENTS.md")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\nFixed on 2026-07-29.\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newDoctorCmd(stableBuild("v0.1.0"))
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	err = cmd.Execute()
	if err == nil {
		t.Fatal("got nil error, want a non-nil error for a perishable-content hit")
	}
	want := "file: " + axi.Value(filepath.Join(home, "AGENTS.md")) + "\n"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("stdout = %q, want the findings anchored at the resolved home's absolute path %q", out.String(), want)
	}
	if !strings.Contains(out.String(), "violations: 1\n") || !strings.Contains(out.String(), ",error,") {
		t.Fatalf("stdout = %q, want one finding counted and marked at error severity", out.String())
	}
}

func TestDoctorTreatsMissingManagedMarkersAsViolation(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	// Refresh first so CLAUDE.md (a plain file on Windows, checked separately from
	// AGENTS.md's canonical content) is already correct: the overwrite below isolates the one
	// violation under test rather than also tripping the Windows-only CLAUDE.md check.
	if _, err := agentsmd.Refresh(home); err != nil {
		t.Fatal(err)
	}
	if _, err := skill.Refresh(home); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "AGENTS.md"),
		[]byte("# Hand-authored, no generated markers\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newDoctorCmd(stableBuild("v0.1.0"))
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err == nil {
		t.Fatal("got nil error, want missing managed markers to fail doctor")
	}
	if !strings.Contains(out.String(), "violations: 1\n") {
		t.Fatalf("stdout = %q, want the missing markers counted as a violation", out.String())
	}
	if !strings.Contains(out.String(), "  none,error,") {
		t.Fatalf("stdout = %q, want a whole-file finding to carry no line number", out.String())
	}
}

func TestDoctorFailsWhenManagedMarkersAreRemovedAfterInitialization(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	path := filepath.Join(home, "AGENTS.md")
	if _, err := agentsmd.Refresh(home); err != nil {
		t.Fatal(err)
	}
	if _, err := skill.Refresh(home); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# Hand-authored, no generated markers\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newDoctorCmd(stableBuild("v0.1.0"))
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err == nil {
		t.Fatal("got nil error, want removed managed markers to fail doctor")
	}
	if !strings.Contains(out.String(), "AGENTS.md has drifted from the canonical Hand-owned content") {
		t.Fatalf("stdout = %q, want the drift violation reported", out.String())
	}
}

func TestDoctorFailsWhenAgentsFileIsDeletedAfterInitialization(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	path := filepath.Join(home, "AGENTS.md")
	if _, err := agentsmd.Refresh(home); err != nil {
		t.Fatal(err)
	}
	if _, err := skill.Refresh(home); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newDoctorCmd(stableBuild("v0.1.0"))
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err == nil {
		t.Fatal("got nil error, want a deleted AGENTS.md to fail doctor")
	}
	if !strings.Contains(out.String(), "violations: 1\n") || !strings.Contains(out.String(), "AGENTS.md is missing") {
		t.Fatalf("stdout = %q, want one missing-file violation", out.String())
	}
}

// Every drift shape now collapses to one whole-file finding: the canonical AGENTS.md is
// compared byte-for-byte, so doctor reports no partial-drift detail.
func TestDoctorReportsDriftForMalformedOrForeignContentWithNoLineNumber(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"unpaired", "# Rules\n<!-- hand:generated:start -->\n"},
		{"duplicate", "<!-- hand:generated:start -->\n<!-- hand:generated:start -->\n<!-- hand:generated:end -->\n"},
		{"reversed", "<!-- hand:generated:end -->\n<!-- hand:generated:start -->\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Chdir(home)
			mkFleetDirs(t, home)
			if err := os.WriteFile(filepath.Join(home, "AGENTS.md"), []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}

			var out bytes.Buffer
			cmd := newDoctorCmd(stableBuild("v0.1.0"))
			cmd.SetOut(&out)
			cmd.SetArgs(nil)
			if err := cmd.Execute(); err == nil {
				t.Fatal("got nil error, want drifted content to fail doctor")
			}
			if !strings.Contains(out.String(), "  none,error,\"AGENTS.md has drifted from the canonical Hand-owned content") {
				t.Fatalf("stdout = %q, want one whole-file drift finding with no line number", out.String())
			}
		})
	}
}

func TestDoctorOutsideFleetHomeIsPrecondition(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HAND_HOME", "")

	cmd := newDoctorCmd(stableBuild("v0.1.0"))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs(nil)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("got nil error, want a precondition failure outside a fleet home")
	}
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 3 {
		t.Fatalf("got %v, want an ExitError with code 3", err)
	}
}

func TestDoctorReportsBinaryVersionChannelCommitAndDistribution(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	if _, err := agentsmd.Refresh(home); err != nil {
		t.Fatal(err)
	}
	if _, err := skill.Refresh(home); err != nil {
		t.Fatal(err)
	}
	mustConfigSet(t, settingHarness, harness.Claude)

	var out bytes.Buffer
	info := selfupdate.BuildInfo{Version: "v0.6.0", Channel: selfupdate.ChannelEdge, Commit: "abcdef1234567890", Distribution: selfupdate.DistributionBrew}
	cmd := newDoctorCmd(info)
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"version: v0.6.0\n",
		"channel: edge\n",
		"commit: " + selfupdate.DisplayCommit("abcdef1234567890") + "\n",
		"distribution: " + selfupdate.DistributionBrew + "\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("stdout = %q, want it to contain %q", got, want)
		}
	}
}

func TestDoctorFlagsEveryMissingBundledSkillDestination(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	if _, err := agentsmd.Refresh(home); err != nil {
		t.Fatal(err)
	}
	mustConfigSet(t, settingHarness, harness.Claude)
	// No skill.Refresh: every destination is missing.

	findings, err := doctorFindings(home)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range findings {
		if strings.Contains(f.Text, "bundled skill is missing") {
			if f.Severity != doctorError {
				t.Fatalf("got severity %v for a missing-skill finding, want error", f.Severity)
			}
			n++
		}
	}
	if n != len(skill.DestinationDirs(home)) {
		t.Fatalf("got %d missing-skill findings, want one per destination (%d): %#v", n, len(skill.DestinationDirs(home)), findings)
	}
}

func TestDoctorFlagsADriftedBundledSkillFile(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	if _, err := agentsmd.Refresh(home); err != nil {
		t.Fatal(err)
	}
	if _, err := skill.Refresh(home); err != nil {
		t.Fatal(err)
	}
	mustConfigSet(t, settingHarness, harness.Claude)
	dir := skill.DestinationDirs(home)[0]
	path := filepath.Join(dir, "SKILL.md")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(content, []byte("\nstray\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	findings, err := doctorFindings(home)
	if err != nil {
		t.Fatal(err)
	}
	if !hasDoctorFinding(findings, doctorFinding{Severity: doctorError, Text: fmt.Sprintf("bundled skill at %s has drifted from the canonical content: run hand init '%s' to refresh it", dir, home)}) {
		t.Fatalf("findings = %#v, want a drift finding naming %s", findings, dir)
	}
}

func TestDoctorFlagsAForeignFileAtASkillDestinationAsAConflict(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	if _, err := agentsmd.Refresh(home); err != nil {
		t.Fatal(err)
	}
	mustConfigSet(t, settingHarness, harness.Claude)
	dir := skill.DestinationDirs(home)[0]
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# unrelated\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	findings, err := doctorFindings(home)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range findings {
		if strings.Contains(f.Text, "foreign, unmanaged file") {
			found = true
			if f.Severity != doctorError {
				t.Fatalf("got severity %v for a skill conflict finding, want error", f.Severity)
			}
		}
	}
	if !found {
		t.Fatalf("findings = %#v, want a foreign-file conflict finding for %s", findings, dir)
	}
}

func TestDoctorWarnsOnEachMissingRequiredTool(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	if _, err := agentsmd.Refresh(home); err != nil {
		t.Fatal(err)
	}
	if _, err := skill.Refresh(home); err != nil {
		t.Fatal(err)
	}
	mustConfigSet(t, settingHarness, harness.Claude)
	faketool.NoTools(t)

	findings, err := doctorFindings(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range foundationalTools {
		want := doctorFinding{Severity: doctorWarning, Text: fmt.Sprintf("required tool %q is not on PATH", tool)}
		if !hasDoctorFinding(findings, want) {
			t.Fatalf("findings = %#v, want a missing-tool warning for %q", findings, tool)
		}
	}
}

func TestGHRequiredOnlyWhenARegisteredProjectDeliversThroughGitHub(t *testing.T) {
	tests := []struct {
		mode string
		want bool
	}{
		{mode: project.ModeLocalOnly, want: false},
		{mode: project.ModeDirectPR, want: true},
		{mode: project.ModeNoMistakes, want: true},
	}
	for _, tt := range tests {
		got := ghRequired([]project.Project{{Name: "p", Mode: tt.mode}})
		if got != tt.want {
			t.Errorf("ghRequired(mode=%q) = %v, want %v", tt.mode, got, tt.want)
		}
	}
	if ghRequired(nil) {
		t.Error("ghRequired(nil) = true, want false for a fleet with no registered projects")
	}
}

func TestDoctorToolsReportsFoundationalAndContextualRequirement(t *testing.T) {
	faketool.NoTools(t)
	bin := faketool.Bin(t)
	faketool.Command{Name: "git"}.Install(t, bin)
	faketool.Treehouse{}.Install(t, bin)

	got := doctorTools([]project.Project{{Name: "p", Mode: project.ModeDirectPR}})
	want := []toolReadiness{
		{Tool: "git", Installed: true, Required: true},
		{Tool: "treehouse", Installed: true, Required: true},
		{Tool: "herdr", Installed: false, Required: true},
		{Tool: "gh", Installed: false, Required: true},
	}
	if len(got) != len(want) {
		t.Fatalf("doctorTools() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("doctorTools()[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestDoctorHarnessesReportsEverySupportedHarness(t *testing.T) {
	faketool.NoTools(t)
	bin := faketool.Bin(t)
	faketool.Command{Name: harness.Codex}.Install(t, bin)

	got := doctorHarnesses()
	if len(got) != len(harness.Names()) {
		t.Fatalf("doctorHarnesses() = %#v, want one entry per %v", got, harness.Names())
	}
	for _, h := range got {
		want := h.Name == harness.Codex
		if h.Installed != want {
			t.Fatalf("doctorHarnesses() entry %q installed = %v, want %v", h.Name, h.Installed, want)
		}
	}
}

func TestDoctorBlockingAndNextStayInStepAndReadyFollowsBlocking(t *testing.T) {
	tools := []toolReadiness{
		{Tool: "git", Installed: true, Required: true},
		{Tool: "treehouse", Installed: false, Required: true},
		{Tool: "herdr", Installed: true, Required: true},
		{Tool: "gh", Installed: false, Required: false},
	}
	harnesses := []harnessReadiness{{Name: harness.Claude, Installed: false}}

	blocking := doctorBlocking(1, tools, harnesses)
	want := []string{"fleet-health", "treehouse", "harness"}
	if len(blocking) != len(want) {
		t.Fatalf("doctorBlocking() = %v, want %v", blocking, want)
	}
	for i := range want {
		if blocking[i] != want[i] {
			t.Fatalf("doctorBlocking()[%d] = %q, want %q", i, blocking[i], want[i])
		}
	}

	next := doctorNext(blocking)
	if len(next) != len(blocking) {
		t.Fatalf("doctorNext() = %v, want one entry per blocking item %v", next, blocking)
	}
	if next[1] != "install treehouse" {
		t.Fatalf("doctorNext()[1] = %q, want %q", next[1], "install treehouse")
	}

	if doctorBlocking(0, []toolReadiness{{Tool: "git", Installed: true, Required: true}}, []harnessReadiness{{Name: harness.Claude, Installed: true}}) == nil {
		t.Fatal("doctorBlocking() returned a nil slice for a ready fleet, want an empty non-nil slice so the rendered list still states its count")
	}
}

func TestDoctorReportsReadyWhenEveryFoundationalToolAndOneHarnessArePresent(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	if _, err := agentsmd.Refresh(home); err != nil {
		t.Fatal(err)
	}
	if _, err := skill.Refresh(home); err != nil {
		t.Fatal(err)
	}
	mustConfigSet(t, settingHarness, harness.Claude)
	faketool.NoTools(t)
	bin := faketool.Bin(t)
	faketool.Command{Name: "git"}.Install(t, bin)
	faketool.Treehouse{}.Install(t, bin)
	faketool.Herdr{}.Install(t, bin)
	faketool.Command{Name: harness.Claude}.Install(t, bin)

	var out bytes.Buffer
	cmd := newDoctorCmd(stableBuild("v0.1.0"))
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("got error %v, want nil for a ready fleet", err)
	}
	want := "tools[4]{tool,installed,required}:\n" +
		"  git,true,true\n" +
		"  treehouse,true,true\n" +
		"  herdr,true,true\n" +
		"  gh,false,false\n" +
		"harnesses[5]{name,installed}:\n" +
		"  claude,true\n" +
		"  codex,false\n" +
		"  grok,false\n" +
		"  pi,false\n" +
		"  opencode,false\n" +
		"ready: true\n" +
		"blocking[0]:\n" +
		"next[0]:\n"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("stdout = %q, want it to contain %q", out.String(), want)
	}
}

func TestDoctorReportsNotReadyWithBlockingAndNextWhenTreehouseAndEveryHarnessAreMissing(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	if _, err := agentsmd.Refresh(home); err != nil {
		t.Fatal(err)
	}
	if _, err := skill.Refresh(home); err != nil {
		t.Fatal(err)
	}
	mustConfigSet(t, settingHarness, harness.Claude)
	faketool.NoTools(t)
	bin := faketool.Bin(t)
	faketool.Command{Name: "git"}.Install(t, bin)
	faketool.Herdr{}.Install(t, bin)

	var out bytes.Buffer
	cmd := newDoctorCmd(stableBuild("v0.1.0"))
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("got error %v, want nil: a missing foundational tool or harness is a warning, not an error", err)
	}
	want := "ready: false\n" +
		"blocking[2]:\n" +
		"  - treehouse\n" +
		"  - harness\n" +
		"next[2]:\n" +
		"  - install treehouse\n" +
		"  - install and authenticate at least one supported coding-agent harness (see `harnesses` above), then run hand doctor\n"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("stdout = %q, want it to contain %q", out.String(), want)
	}
}

func TestDoctorStrictModeReturnsAnErrorForBlockingReadiness(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	if _, err := agentsmd.Refresh(home); err != nil {
		t.Fatal(err)
	}
	if _, err := skill.Refresh(home); err != nil {
		t.Fatal(err)
	}
	faketool.NoTools(t)

	var out bytes.Buffer
	cmd := newDoctorCmd(stableBuild("v0.1.0"))
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--fail-if-not-ready"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("strict doctor error = %v, want a not-ready error", err)
	}
	if !strings.Contains(out.String(), "ready: false\n") {
		t.Fatalf("strict doctor output = %q, want structured readiness", out.String())
	}
}

func TestDoctorGHNotRequiredForALocalOnlyProjectDoesNotBlockReadiness(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	if _, err := agentsmd.Refresh(home); err != nil {
		t.Fatal(err)
	}
	if _, err := skill.Refresh(home); err != nil {
		t.Fatal(err)
	}
	if err := project.Add(home, project.Project{Name: "local", URL: "https://example.com/local.git", Mode: project.ModeLocalOnly}); err != nil {
		t.Fatal(err)
	}
	mustConfigSet(t, settingHarness, harness.Claude)
	faketool.NoTools(t)
	bin := faketool.Bin(t)
	faketool.Command{Name: "git"}.Install(t, bin)
	faketool.Treehouse{}.Install(t, bin)
	faketool.Herdr{}.Install(t, bin)
	faketool.Command{Name: harness.Claude}.Install(t, bin)

	var out bytes.Buffer
	cmd := newDoctorCmd(stableBuild("v0.1.0"))
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("got error %v, want nil", err)
	}
	if !strings.Contains(out.String(), "gh,false,false\n") {
		t.Fatalf("stdout = %q, want gh reported installed=false, required=false for a local-only project", out.String())
	}
	if !strings.Contains(out.String(), "ready: true\n") {
		t.Fatalf("stdout = %q, want ready: true since gh is not required", out.String())
	}
}
