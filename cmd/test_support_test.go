package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atqamz/hand/internal/faketool"
	"github.com/atqamz/hand/internal/herdr"
	"github.com/atqamz/hand/internal/state"
)

func writeTaskAttempt(t *testing.T, home string, task state.Task, attempt state.Attempt) error {
	t.Helper()
	if err := state.CreateTask(home, task); err != nil {
		t.Fatal(err)
	}
	attempt.TaskID = task.ID
	if _, err := state.CreateAttempt(home, attempt); err != nil {
		t.Fatal(err)
	}
	return nil
}

func readTaskAttempt(t *testing.T, home, id string) state.Attempt {
	t.Helper()
	attempt, err := state.ActiveAttempt(home, id)
	if err != nil {
		t.Fatal(err)
	}
	return attempt
}

func setTestUserHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func scopeHerdrForFleet(t *testing.T, home string, h faketool.Herdr) faketool.Herdr {
	t.Helper()
	fleetID, err := state.FleetID(home)
	if err != nil {
		t.Fatal(err)
	}
	for i := range h.Workspaces {
		project := strings.TrimPrefix(h.Workspaces[i].Label, "hand:")
		if project != h.Workspaces[i].Label && !strings.HasPrefix(project, "f_") {
			h.Workspaces[i].Label = herdr.WorkspaceLabel(fleetID, project)
		}
	}
	return h
}

func TestMain(m *testing.M) {
	testUserHome, err := os.MkdirTemp("", "hand-cmd-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, tc := range []struct {
		name  string
		value string
	}{
		{name: "HAND_ROLE", value: ""},
		{name: "HAND_HOME", value: ""},
		{name: "HAND_HARNESS", value: "unknown"},
	} {
		if err := os.Setenv(tc.name, tc.value); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if err := os.Setenv("HOME", testUserHome); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Setenv("USERPROFILE", testUserHome); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	gitConfig := filepath.Join(testUserHome, ".gitconfig")
	if err := os.WriteFile(gitConfig, []byte("[commit]\n\tgpgsign = false\n[tag]\n\tgpgsign = false\n"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Setenv("GIT_CONFIG_GLOBAL", gitConfig); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	configureSelfUpdateTests()
	code := m.Run()
	_ = os.RemoveAll(testUserHome)
	os.Exit(code)
}

func TestCommandPackageStartsWithNeutralEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
	}{
		{name: "HAND_ROLE", want: ""},
		{name: "HAND_HOME", want: ""},
		{name: "HAND_HARNESS", want: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := os.Getenv(tc.name); got != tc.want {
				t.Fatalf("%s = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}
