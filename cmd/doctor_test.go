package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atqamz/secondhand/internal/agentsmd"
)

// A clean file is the one answer silence used to be indistinguishable from a
// checker that never ran, so it states its zero rather than printing nothing.
func TestDoctorCleanFleetHomeStatesItsZeroCount(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	if _, err := agentsmd.Refresh(home); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newDoctorCmd()
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("got error %v, want nil for a clean AGENTS.md", err)
	}
	want := "file: " + filepath.Join(home, "AGENTS.md") + "\n" +
		"count: 0\n" +
		"violations: 0\n" +
		"findings[0]{line,severity,finding}:\n"
	if out.String() != want {
		t.Fatalf("stdout = %q, want %q", out.String(), want)
	}
}

func TestDoctorReportsViolationsAndExitsNonZero(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	if _, err := agentsmd.Refresh(home); err != nil {
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
	cmd := newDoctorCmd()
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	err = cmd.Execute()
	if err == nil {
		t.Fatal("got nil error, want a non-nil error for a perishable-content hit")
	}
	want := "file: " + filepath.Join(home, "AGENTS.md") + "\n"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("stdout = %q, want the findings anchored at the resolved home's absolute path %q", out.String(), want)
	}
	if !strings.Contains(out.String(), "violations: 1\n") || !strings.Contains(out.String(), ",violation,") {
		t.Fatalf("stdout = %q, want one finding counted and marked at violation severity", out.String())
	}
}

func TestDoctorTreatsMissingManagedMarkersAsViolation(t *testing.T) {
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	if err := os.WriteFile(filepath.Join(home, "AGENTS.md"),
		[]byte("# Hand-authored, no generated markers\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newDoctorCmd()
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err == nil {
		t.Fatal("got nil error, want missing managed markers to fail doctor")
	}
	if !strings.Contains(out.String(), "count: 1\n") || !strings.Contains(out.String(), "violations: 1\n") {
		t.Fatalf("stdout = %q, want the missing markers counted as a violation", out.String())
	}
	if !strings.Contains(out.String(), "  none,violation,") {
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
	if err := os.WriteFile(path, []byte("# Hand-authored, no generated markers\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newDoctorCmd()
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err == nil {
		t.Fatal("got nil error, want removed managed markers to fail doctor")
	}
	if !strings.Contains(out.String(), "no hand:generated markers") {
		t.Fatalf("stdout = %q, want the missing-markers violation reported", out.String())
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
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd := newDoctorCmd()
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err == nil {
		t.Fatal("got nil error, want a deleted AGENTS.md to fail doctor")
	}
	if !strings.Contains(out.String(), "violations: 1\n") || !strings.Contains(out.String(), "AGENTS.md is missing") {
		t.Fatalf("stdout = %q, want one missing-file violation", out.String())
	}
}

func TestDoctorReportsMalformedMarkersWithLineNumbers(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"unpaired", "# Rules\n<!-- hand:generated:start -->\n", "  2,violation,\"unpaired hand:generated start marker\""},
		{"duplicate", "<!-- hand:generated:start -->\n<!-- hand:generated:start -->\n<!-- hand:generated:end -->\n", "  2,violation,\"duplicate hand:generated start marker\""},
		{"reversed", "<!-- hand:generated:end -->\n<!-- hand:generated:start -->\n", "  1,violation,\"hand:generated end marker appears before start marker\""},
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
			cmd := newDoctorCmd()
			cmd.SetOut(&out)
			cmd.SetArgs(nil)
			if err := cmd.Execute(); err == nil {
				t.Fatal("got nil error, want malformed markers to fail doctor")
			}
			if !strings.Contains(out.String(), tt.want) {
				t.Fatalf("stdout = %q, want %q", out.String(), tt.want)
			}
		})
	}
}

func TestDoctorOutsideFleetHomeIsPrecondition(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HAND_HOME", "")

	cmd := newDoctorCmd()
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
