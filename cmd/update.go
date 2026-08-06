package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/atqamz/secondhand/internal/agentsmd"
	"github.com/atqamz/secondhand/internal/axi"
	"github.com/atqamz/secondhand/internal/home"
	"github.com/atqamz/secondhand/internal/selfupdate"
	"github.com/atqamz/secondhand/internal/sessionhook"
	"github.com/spf13/cobra"
)

func newUpdateCmd(version string) *cobra.Command {
	var checkOnly bool

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Self-update the hand binary from GitHub Releases",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			latest, err := selfupdate.LatestTag(selfupdate.Repo)
			if err != nil {
				return err
			}
			newer, err := selfupdate.IsNewer(latest, version)
			if err != nil {
				return err
			}

			if !newer || checkOnly {
				var doc axi.Doc
				doc.Field("current", version)
				doc.Field("latest", latest)
				doc.Bool("update_available", newer)
				doc.Bool("updated", false)
				doc.Field("agents_md", "not-applicable")
				doc.Field("session_hook", "not-applicable")
				doc.List("notes", nil)
				if newer {
					doc.Help("Run `hand update` to install " + latest + ", which also refreshes this home's AGENTS.md template")
				}
				return doc.Render(cmd.OutOrStdout())
			}

			if err := selfupdate.Apply(selfupdate.Repo, latest); err != nil {
				return err
			}

			// The binary is already replaced by this point, so a failed AGENTS.md refresh or skeleton seed is
			// reported as a warning rather than an error: exiting nonzero here reads as "the update failed" and
			// invites a pointless re-run.
			var refreshed, hookRemoved bool
			var seedErr, hookErr error
			fleetHome, refreshErr := home.Resolve()
			switch {
			case refreshErr == nil:
				refreshed, refreshErr = agentsmd.Refresh(fleetHome)
				// The refreshed template directs the agent at data files an older home never had, so the command
				// that installs it also leaves those files in place - directories included, since a home resolves
				// as one on its state/hand.db marker alone.
				seedErr = initLayout(fleetHome)
				// Generated instructions supersede the Claude-only hook, so an
				// update retires Secondhand's command without touching others.
				var exe string
				if exe, hookErr = os.Executable(); hookErr == nil {
					hookRemoved, hookErr = sessionhook.Remove(fleetHome, exe)
				}
			case errors.Is(refreshErr, home.ErrNotFound):
				refreshErr = nil
			}

			if refreshErr != nil {
				if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "warning: refresh AGENTS.md: %v\n", refreshErr); err != nil {
					return err
				}
			}
			if hookErr != nil {
				if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "warning: retire the session hook: %v\n", hookErr); err != nil {
					return err
				}
			}
			if seedErr != nil {
				if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "warning: seed data skeletons: %v\n", seedErr); err != nil {
					return err
				}
			}
			notes, _ := selfupdate.ReleaseNotes(selfupdate.Repo, latest)

			var doc axi.Doc
			doc.Field("current", version)
			doc.Field("latest", latest)
			doc.Bool("update_available", true)
			doc.Bool("updated", true)
			doc.Field("agents_md", refreshOutcome(fleetHome, refreshed, refreshErr))
			doc.Field("session_hook", retirementOutcome(fleetHome, hookRemoved, hookErr))
			doc.List("notes", releaseNoteLines(notes))
			doc.Help("Run `hand doctor` to check this home's AGENTS.md against the template " + latest + " installed")
			return doc.Render(cmd.OutOrStdout())
		},
	}

	cmd.Flags().BoolVar(&checkOnly, "check", false, "check whether an update is available without installing")
	return cmd
}

// The binary is replaced whatever this says, so every outcome is a value of
// one field rather than a line that appears or does not.
func refreshOutcome(fleetHome string, changed bool, err error) string {
	switch {
	case err != nil:
		return "failed"
	case fleetHome == "":
		return "no-fleet-home"
	case changed:
		return "refreshed"
	default:
		return "unchanged"
	}
}

func retirementOutcome(fleetHome string, changed bool, err error) string {
	switch {
	case err != nil:
		return "failed"
	case fleetHome == "":
		return "no-fleet-home"
	case changed:
		return "removed"
	default:
		return "unchanged"
	}
}

func releaseNoteLines(notes string) []string {
	var lines []string
	for _, line := range strings.Split(notes, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}
