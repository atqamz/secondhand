package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/atqamz/hand/internal/axi"
	"github.com/atqamz/hand/internal/harness"
	"github.com/atqamz/hand/internal/home"
	"github.com/atqamz/hand/internal/project"
	"github.com/atqamz/hand/internal/registry"
	"github.com/atqamz/hand/internal/selfupdate"
	"github.com/atqamz/hand/internal/store"
	"github.com/spf13/cobra"
)

func newRootCmd(info selfupdate.BuildInfo) *cobra.Command {
	root := &cobra.Command{
		Use:     "hand",
		Short:   "You lead. hand runs the crew.",
		Version: info.Version,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Name() == "build-info" || cmd.Name() == "adopt" {
				return nil
			}
			if cmd.Name() == "fleet" {
				return nil
			}
			if fleetHome, err := home.Resolve(); err == nil {
				startupOverview := cmd.Name() == "hand" || cmd.CommandPath() == "hand session start"
				readOnly := isReadOnlyCommand(cmd)
				if cmd.Name() != "init" && cmd.Name() != "status" && !startupOverview && !readOnly {
					if _, statErr := os.Stat(store.Path(fleetHome)); os.IsNotExist(statErr) {
						if err := project.Migrate(fleetHome); err != nil {
							return err
						}
					} else if statErr != nil {
						return fmt.Errorf("check state/hand.db: %w", statErr)
					}
					if _, err := migrateWorkerSettings(fleetHome); err != nil {
						return err
					}
				}
				if cmd.Name() != "update" && !startupOverview && !readOnly {
					if notice := selfupdate.CheckNoticeForBuild(fleetHome, selfupdate.Repo, info); notice != "" {
						_, _ = fmt.Fprintln(cmd.ErrOrStderr(), notice)
					}
				}
				if shouldGuardFleet(cmd) {
					warnings, err := registry.Preflight(fleetHome, cmd.Name() == "status" || readOnly || startupOverview)
					if err != nil {
						return asPrecondition(err)
					}
					for _, warning := range warnings {
						if _, err := fmt.Fprintln(cmd.ErrOrStderr(), warning); err != nil {
							return err
						}
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
			return runRootOverview(cmd, info.Version)
		},
	}
	root.SetVersionTemplate("{{.Version}}\n")
	root.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return &ExitError{Err: err, Code: 2}
	})
	root.AddCommand(newInitCmd())
	root.AddCommand(newBuildInfoCmd(info))
	root.AddCommand(newAdoptCmd())
	root.AddCommand(newConfigCmd())
	root.AddCommand(newProjectCmd())
	root.AddCommand(newSpawnCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newFleetCmd())
	root.AddCommand(newReconcileCmd())
	root.AddCommand(newSessionCmd(info.Version))
	root.AddCommand(newSendCmd())
	root.AddCommand(newHoldCmd())
	root.AddCommand(newDeliverCmd())
	root.AddCommand(newAckCmd())
	root.AddCommand(newTeardownCmd())
	root.AddCommand(newWatchCmd())
	root.AddCommand(newMergeCmd())
	root.AddCommand(newPRCmd())
	root.AddCommand(newPromoteCmd())
	root.AddCommand(newReopenCmd())
	root.AddCommand(newNotifyCmd())
	root.AddCommand(newSearchCmd())
	root.AddCommand(newDoctorCmd(info))
	root.AddCommand(newRuntimeCmd())
	root.AddCommand(newIntegrationCmd())
	root.AddCommand(newUpdateCmd(info))
	// ExecuteC would add the completion group later, too late for the guard below.
	root.InitDefaultCompletionCmd()
	guardSubcommandGroups(root)
	return root
}

func isReadOnlyCommand(cmd *cobra.Command) bool {
	switch cmd.CommandPath() {
	case "hand", "hand doctor", "hand runtime status":
		return true
	default:
		return false
	}
}

func shouldGuardFleet(cmd *cobra.Command) bool {
	return cmd.Name() != "init" && cmd.Name() != "fleet" && cmd.CommandPath() != "hand" && cmd.CommandPath() != "hand session start"
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
	if os.Getenv(harness.RoleEnv) == harness.WorkerRole {
		return &ExitError{Err: fmt.Errorf("supervisor session bootstrap is unavailable when %s=%s", harness.RoleEnv, harness.WorkerRole), Code: 3}
	}
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

func Execute(version, channel, commit, distribution string) {
	root := newRootCmd(selfupdate.NormalizeBuildInfo(version, channel, commit, distribution))
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
	help := errorHelp(code, path)
	var details interface {
		SendFields() (int64, int64, string, string, bool, bool)
	}
	if errors.As(err, &details) {
		sendID, attemptID, sendState, reason, retrySafe, partial := details.SendFields()
		if sendID != 0 {
			doc.Field("send_id", fmt.Sprintf("%d", sendID))
		}
		if attemptID != 0 {
			doc.Field("attempt", fmt.Sprintf("%d", attemptID))
		}
		if sendState != "" {
			doc.Field("send_state", sendState)
		}
		if reason != "" {
			doc.Field("reason", reason)
		}
		switch {
		case partial:
			help = []string{"The message was not submitted, but text may remain in the composer; do not blindly send the whole message again"}
		case retrySafe:
			help = []string{"No terminal submission occurred; retry is safe when the underlying precondition is ready"}
		case sendState == "uncertain" || sendState == "pending":
			help = []string{"Terminal submission is uncertain; do not blindly retry because the message may already be in the pane"}
		}
	}
	doc.Help(help...)
	return doc.Render(w)
}

// These names let callers branch without memorizing exit numbers.
var errorKinds = map[int]string{
	1: "general",
	2: "usage",
	3: "precondition",
	4: "no-event",
	5: "arm-failed",
	6: "send-not-submitted",
	7: "send-uncertain",
	8: "watch-interrupted",
	9: "watch-replaced",
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
		return []string{"No terminal submission occurred; retry after the underlying precondition is ready"}
	case 7:
		return []string{"Terminal submission is uncertain; do not blindly retry because the message may already be in the pane"}
	case 8:
		return []string{"The watcher stopped because of a generic interruption; it emitted no fleet event, releases ownership as the command exits, and may be re-armed by a supervisor"}
	case 9:
		return []string{"This watcher was explicitly displaced by another Hand watcher; it emitted no fleet event, and the takeover successor acquires ownership after release"}
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
