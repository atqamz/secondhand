package cmd

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/atqamz/secondhand/internal/harness"
	"github.com/atqamz/secondhand/internal/state"
	"github.com/spf13/cobra"
)

func exitCodeFor(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code
	}
	return 1
}

func TestUsageArgsTagsMismatchAsExitCode2(t *testing.T) {
	validate := usageArgs(cobra.ExactArgs(2))
	err := validate(&cobra.Command{}, []string{"onlyone"})
	if code := exitCodeFor(t, err); code != 2 {
		t.Fatalf("code = %d, want 2 (err = %v)", code, err)
	}
}

func TestUsageArgsPassesThroughValidArgs(t *testing.T) {
	validate := usageArgs(cobra.ExactArgs(2))
	if err := validate(&cobra.Command{}, []string{"a", "b"}); err != nil {
		t.Fatalf("got %v, want nil", err)
	}
}

func TestRootRejectsUnknownCommand(t *testing.T) {
	root := newRootCmd("test")
	root.SetArgs([]string{"bogus-command"})
	root.SetOut(new(strings.Builder))
	root.SetErr(new(strings.Builder))
	found, err := root.ExecuteC()
	if err == nil {
		t.Fatal("want error for unknown command")
	}
	if found != root {
		t.Fatalf("found = %v, want root itself", found.Name())
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("err = %v, want unknown command", err)
	}
}

func TestGroupRejectsUnknownSubcommand(t *testing.T) {
	for _, group := range []string{"project", "completion"} {
		t.Run(group, func(t *testing.T) {
			root := newRootCmd("test")
			root.SetArgs([]string{group, "bogus-subcommand"})
			root.SetOut(new(strings.Builder))
			root.SetErr(new(strings.Builder))
			_, err := root.ExecuteC()
			if code := exitCodeFor(t, err); code != 2 {
				t.Fatalf("code = %d, want 2 (err = %v)", code, err)
			}
			if !strings.Contains(err.Error(), "unknown command") {
				t.Fatalf("err = %v, want unknown command", err)
			}
		})
	}
}

func TestGroupBareInvocationShowsHelpWithoutError(t *testing.T) {
	root := newRootCmd("test")
	root.SetArgs([]string{"project"})
	var out strings.Builder
	root.SetOut(&out)
	root.SetErr(new(strings.Builder))
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("got %v, want nil (bare group shows help)", err)
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Fatalf("out = %q, want usage text", out.String())
	}
}

func TestRootRejectsBadArgCount(t *testing.T) {
	root := newRootCmd("test")
	root.SetArgs([]string{"spawn", "onlyonearg"})
	root.SetOut(new(strings.Builder))
	root.SetErr(new(strings.Builder))
	_, err := root.ExecuteC()
	if code := exitCodeFor(t, err); code != 2 {
		t.Fatalf("code = %d, want 2 (err = %v)", code, err)
	}
}

func TestRootRejectsUnknownFlag(t *testing.T) {
	root := newRootCmd("test")
	root.SetArgs([]string{"spawn", "--bogus", "a", "b"})
	root.SetOut(new(strings.Builder))
	root.SetErr(new(strings.Builder))
	_, err := root.ExecuteC()
	if code := exitCodeFor(t, err); code != 2 {
		t.Fatalf("code = %d, want 2 (err = %v)", code, err)
	}
}

func runBareRoot(t *testing.T) string {
	t.Helper()
	root := newRootCmd("test")
	root.SetArgs([]string{})
	var out strings.Builder
	root.SetOut(&out)
	root.SetErr(new(strings.Builder))
	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("got %v, want nil (the bare command reports, it does not refuse)", err)
	}
	return out.String()
}

func TestBareInvocationLeadsWithTheFleetItManages(t *testing.T) {
	t.Setenv("HAND_HARNESS", harness.Claude)
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)
	if err := state.Write(home, state.Task{ID: "task-1", Project: "myproj", Kind: state.KindShip}); err != nil {
		t.Fatal(err)
	}

	out := runBareRoot(t)
	for _, want := range []string{
		"tool: hand\n",
		"version: test\n",
		"count: 1\n",
		"held: 0\n",
		"  task-1,",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("out = %q, want it to contain %q", out, want)
		}
	}
	if strings.Contains(out, "Usage:") {
		t.Fatalf("out = %q, want the fleet rather than a help dump", out)
	}
}

// The bare command is the session hook, so it is the only place a supervising agent learns that the
// fleet is not configured yet, and the only place the question can be put in front of the operator.
func TestBareInvocationReportsConfigurationStateAndAsksTheOperator(t *testing.T) {
	t.Setenv("HAND_HARNESS", "unknown")
	home := t.TempDir()
	t.Chdir(home)
	mkFleetDirs(t, home)

	out := runBareRoot(t)
	for _, want := range []string{
		"config_missing: 1\n",
		"config[3]{key,state,value}:\n",
		"harness,missing,none",
		"Ask the operator which harness",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("out = %q, want it to contain %q", out, want)
		}
	}

	if _, err := runConfigSet(t, settingHarness, "grok"); err != nil {
		t.Fatal(err)
	}
	out = runBareRoot(t)
	for _, want := range []string{
		"config_missing: 0\n",
		"harness,configured,grok",
		"model,unsupported,none",
		"effort,unsupported,none",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("out = %q, want it to contain %q", out, want)
		}
	}
	if strings.Contains(out, "Ask the operator") {
		t.Fatalf("out = %q, want no configuration question once nothing applicable is missing", out)
	}
}

func TestBareInvocationOutsideAFleetHomeSaysSoAndNamesTheWayIn(t *testing.T) {
	t.Setenv("HAND_HARNESS", harness.Claude)
	t.Chdir(t.TempDir())
	t.Setenv("HAND_HOME", "")

	out := runBareRoot(t)
	if !strings.Contains(out, "home: none\n") {
		t.Fatalf("out = %q, want it to state that there is no fleet home", out)
	}
	if !strings.Contains(out, "`hand init`") {
		t.Fatalf("out = %q, want it to name hand init", out)
	}
	if strings.Contains(out, "count:") {
		t.Fatalf("out = %q, want no fleet blocks with no fleet to report", out)
	}
}

func TestErrorDocumentNamesTheKindBehindEveryExitCode(t *testing.T) {
	for _, tc := range []struct {
		code int
		kind string
	}{
		{1, "general"},
		{2, "usage"},
		{3, "precondition"},
		{4, "no-event"},
		{5, "arm-failed"},
		{6, "send-undelivered"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			var out strings.Builder
			if err := renderError(&out, errors.New("something went wrong"), tc.code, "hand spawn"); err != nil {
				t.Fatal(err)
			}
			want := "error: something went wrong\nkind: " + tc.kind + "\nexit: " + strconv.Itoa(tc.code) + "\n"
			if !strings.HasPrefix(out.String(), want) {
				t.Fatalf("error document = %q, want it to start with %q", out.String(), want)
			}
		})
	}
}

func TestUsageErrorHelpNamesTheCommandThatRefused(t *testing.T) {
	var out strings.Builder
	if err := renderError(&out, errors.New("accepts 2 arg(s), received 1"), 2, "hand hold set"); err != nil {
		t.Fatal(err)
	}
	want := "help[1]:\n  - Run `hand hold set --help` for the arguments and flags this command accepts\n"
	if !strings.HasSuffix(out.String(), want) {
		t.Fatalf("error document = %q, want it to end with %q", out.String(), want)
	}
}

// A general error is the one code with no recovery a caller can be told in
// advance, so it gets no help block rather than a line saying nothing.
func TestGeneralErrorCarriesNoHelpBlock(t *testing.T) {
	var out strings.Builder
	if err := renderError(&out, errors.New("write config/harness: disk full"), 1, "hand init"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "help[") {
		t.Fatalf("error document = %q, want no help block", out.String())
	}
}

func TestMultiLineErrorStaysOneField(t *testing.T) {
	var out strings.Builder
	joined := errors.Join(errors.New("write data/backlog.md: read-only"), errors.New("write data/operator.md: read-only"))
	if err := renderError(&out, joined, 1, "hand init"); err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(out.String(), "\n"); lines != 3 {
		t.Fatalf("error document = %q, want its three fields on three lines", out.String())
	}
	if !strings.Contains(out.String(), `\n`) {
		t.Fatalf("error document = %q, want the embedded newline escaped", out.String())
	}
}
