package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/atqamz/secondhand/internal/axi"
	"github.com/atqamz/secondhand/internal/home"
	"github.com/atqamz/secondhand/internal/selfupdate"
	"github.com/spf13/cobra"
)

func newRootCmd(version string) *cobra.Command {
	root := &cobra.Command{
		Use:     "hand",
		Short:   "Talk to one agent. Ship with a crew.",
		Version: version,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if fleetHome, err := home.Resolve(); err == nil {
				startupOverview := cmd.Name() == "hand" || cmd.CommandPath() == "hand session start"
				if cmd.Name() != "init" && !startupOverview {
					if _, err := migrateWorkerSettings(fleetHome); err != nil {
						return err
					}
				}
				if cmd.Name() != "update" && !startupOverview {
					if notice := selfupdate.CheckNotice(fleetHome, selfupdate.Repo, version); notice != "" {
						_, _ = fmt.Fprintln(cmd.ErrOrStderr(), notice)
					}
				}
			}
			return nil
		},
		// Cobra's own error/usage printing is disabled; Execute renders exactly
		// one error document on stderr and picks the exit code.
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRootOverview(cmd, version)
		},
	}
	root.SetVersionTemplate("{{.Version}}\n")
	root.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return &ExitError{Err: err, Code: 2}
	})
	root.AddCommand(newInitCmd())
	root.AddCommand(newConfigCmd())
	root.AddCommand(newProjectCmd())
	root.AddCommand(newSpawnCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newSessionCmd(version))
	root.AddCommand(newSendCmd())
	root.AddCommand(newHoldCmd())
	root.AddCommand(newDeliverCmd())
	root.AddCommand(newTeardownCmd())
	root.AddCommand(newWatchCmd())
	root.AddCommand(newMergeCmd())
	root.AddCommand(newPRCmd())
	root.AddCommand(newPromoteCmd())
	root.AddCommand(newNotifyCmd())
	root.AddCommand(newSearchCmd())
	root.AddCommand(newDoctorCmd())
	root.AddCommand(newUpdateCmd(version))
	// ExecuteC would add the completion group later, too late for the guard below.
	root.InitDefaultCompletionCmd()
	guardSubcommandGroups(root)
	return root
}

// Makes every subcommand-only group below c reject an unknown subcommand with exit code 2.
func guardSubcommandGroups(c *cobra.Command) {
	// Root itself is left alone: cobra's Find() already reports its unknown commands, and giving it a
	// non-nil Args would suppress that.
	for _, sub := range c.Commands() {
		if sub.HasSubCommands() && !sub.Runnable() {
			// A group with no RunE trips cobra's Runnable() check, which short-circuits to a help dump and a
			// zero exit before the group's Args validator ever runs, so the group needs both.
			sub.Args = usageArgs(cobra.NoArgs)
			sub.RunE = func(cmd *cobra.Command, args []string) error { return cmd.Help() }
		}
		guardSubcommandGroups(sub)
	}
}

// The bare command answers with the fleet it manages rather than a help dump:
// what a caller with nothing to go on needs first is the state, and `hand
// --help` is still one word away for the reference.
func runRootOverview(cmd *cobra.Command, version string) error {
	fleetHome, err := home.Resolve()
	if err == nil {
		return renderSessionOverview(cmd, version, fleetHome)
	}

	exe, err := os.Executable()
	if err != nil {
		exe = "unknown"
	}

	var doc axi.Doc
	doc.Field("tool", "hand")
	doc.Field("purpose", "manages a fleet of coding agents - one worker per task in its own worktree and herdr pane")
	doc.Field("version", version)
	doc.Field("exec", tildePath(exe))

	doc.Field("home", "none")
	doc.Help("Run `hand init` in the directory that should become the fleet home, or point HAND_HOME at one that already exists",
		"Run `hand --help` for the command reference")
	return doc.Render(cmd.OutOrStdout())
}

func tildePath(path string) string {
	dir, err := os.UserHomeDir()
	if err != nil || dir == "" || !strings.HasPrefix(path, dir+string(os.PathSeparator)) {
		return path
	}
	return "~" + strings.TrimPrefix(path, dir)
}

func usageArgs(validate cobra.PositionalArgs) cobra.PositionalArgs {
	return func(c *cobra.Command, args []string) error {
		if err := validate(c, args); err != nil {
			return &ExitError{Err: err, Code: 2}
		}
		return nil
	}
}

// Tags a rejected input value as exit code 2 only when it came from the command line. The same value read
// from a config/ default is a general error (code 1): nothing the invocation said was wrong.
func usageValue(fromFlag bool, err error) error {
	if fromFlag {
		return &ExitError{Err: err, Code: 2}
	}
	return err
}

func Execute(version string) {
	root := newRootCmd(version)
	found, err := root.ExecuteC()
	if err == nil {
		return
	}

	code := 1
	var exitErr *ExitError
	switch {
	case errors.As(err, &exitErr):
		code = exitErr.Code
	case found == root:
		// cobra's own dispatch failed before reaching any subcommand's Args
		// check (e.g. an unknown command name) - untagged, but still a usage error.
		code = 2
	}
	_ = renderError(os.Stderr, err, code, found.CommandPath())
	os.Exit(code)
}

// The error document goes to stderr, where AXI puts it on stdout, because
// `hand watch` owns stdout as an event stream a reader consumes line by line.
func renderError(w io.Writer, err error, code int, path string) error {
	var doc axi.Doc
	doc.Field("error", err.Error())
	doc.Field("kind", errorKind(code))
	doc.Int("exit", code)
	doc.Help(errorHelp(code, path)...)
	return doc.Render(w)
}

// These names let callers branch without memorizing exit numbers.
var errorKinds = map[int]string{
	1: "general",
	2: "usage",
	3: "precondition",
	4: "no-event",
	5: "arm-failed",
	6: "send-undelivered",
}

func errorKind(code int) string {
	if kind, ok := errorKinds[code]; ok {
		return kind
	}
	return "general"
}

func errorHelp(code int, path string) []string {
	switch code {
	case 2:
		return []string{"Run `" + path + " --help` for the arguments and flags this command accepts"}
	case 3:
		return []string{"Nothing changed: this refuses until the state it names is fixed, then the same command runs again"}
	case 4:
		return []string{"The wait ended with no transition: run it again with a longer `--timeout`, or read `hand status` for where the fleet stands"}
	case 5:
		return []string{"A named task's pane failed its arm-time probe: run `hand status <id>` to read what that worker reports"}
	case 6:
		return []string{"The message never reached the pane: send it again, with a longer `--wait` if the composer stays busy"}
	}
	return nil
}

// ExitError carries a non-default exit code, distinct from the general-error code
// cobra otherwise produces for a RunE error.
type ExitError struct {
	Err error
	// 2 for a usage error (bad arg count, unknown flag, unknown subcommand, invalid argument or flag
	// value) and 3 for a precondition failure like red CI or uncommitted changes.
	Code int
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }
