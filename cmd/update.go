package cmd

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/atqamz/hand/internal/axi"
	"github.com/atqamz/hand/internal/home"
	"github.com/atqamz/hand/internal/selfupdate"
	"github.com/spf13/cobra"
)

func newUpdateCmd(info selfupdate.BuildInfo) *cobra.Command {
	var checkOnly bool
	var requestedChannel string

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Self-update the hand binary from GitHub Releases",
		Args:  usageArgs(cobra.NoArgs),
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if requestedChannel != "" && requestedChannel != selfupdate.ChannelStable && requestedChannel != selfupdate.ChannelEdge {
				return usageValue(true, fmt.Errorf("invalid release channel %q", requestedChannel))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			targetChannel := info.Channel
			if targetChannel == selfupdate.ChannelDev {
				targetChannel = selfupdate.ChannelStable
			}
			if requestedChannel != "" {
				targetChannel = requestedChannel
			}

			target, err := selfupdate.ResolveTarget(selfupdate.Repo, targetChannel)
			if err != nil {
				return err
			}
			newer, err := selfupdate.NeedsUpdate(info, target)
			if err != nil {
				return err
			}

			reconcileHome, reconcileErr := home.Resolve()
			switch {
			case reconcileErr == nil:
				reconcileErr = selfupdate.ReconcileNotice(reconcileHome, target)
			case errors.Is(reconcileErr, home.ErrNotFound):
				reconcileErr = nil
			}
			if reconcileErr != nil {
				if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "warning: reconcile the version notice cache: %v\n", reconcileErr); err != nil {
					return err
				}
			}

			if !newer || checkOnly {
				doc := updateDoc(info, target, newer, false, "not-applicable", nil)
				if newer {
					doc.Help(updateHelp(target, requestedChannel != ""))
				}
				return doc.Render(cmd.OutOrStdout())
			}

			if !selfupdate.CanSelfUpdate(info.Distribution) {
				doc := updateDoc(info, target, true, false, "not-applicable", nil)
				doc.Help(ownershipRefusalHelp(info.Distribution))
				return doc.Render(cmd.OutOrStdout())
			}

			if err := selfupdate.Apply(selfupdate.Repo, target); err != nil {
				return err
			}

			// The binary is already replaced by this point, so a failed handoff is reported as a
			// warning rather than an error: exiting nonzero here reads as "the update failed" and
			// invites a pointless re-run of a step that already succeeded.
			result, homeErr := reconcileFleetHome()
			if homeErr != nil {
				if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "warning: resolve fleet home for reconciliation: %v\n", homeErr); err != nil {
					return err
				}
			}
			notes, _ := selfupdate.ReleaseNotes(selfupdate.Repo, target.Tag)

			doc := updateDoc(info, target, true, true, result.status, releaseNoteLines(notes))
			if len(result.output) > 0 {
				doc.List("fleet_reconcile_output", result.output)
			}
			doc.Help("Run `hand doctor` to check this home's generated surfaces against the template " + target.Version + " installed")
			return doc.Render(cmd.OutOrStdout())
		},
	}

	cmd.Flags().BoolVar(&checkOnly, "check", false, "check whether an update is available without installing")
	cmd.Flags().StringVar(&requestedChannel, "channel", "", "release channel to target (stable or edge)")
	return cmd
}

// Reports what handing reconciliation to the newly installed binary found.
type fleetReconcileResult struct {
	status string
	output []string
}

// Hands fleet-home reconciliation to the binary selfupdate.Apply just installed: the old binary
// only knows how to replace itself, the new one owns what a valid fleet home looks like. The
// returned error is a resolution failure other than "not found", reported as a warning only.
func reconcileFleetHome() (fleetReconcileResult, error) {
	fleetHome, err := home.Resolve()
	switch {
	case err == nil:
	case errors.Is(err, home.ErrNotFound):
		return fleetReconcileResult{status: "no-fleet-home"}, nil
	default:
		return fleetReconcileResult{status: "failed", output: []string{err.Error()}}, err
	}

	// The same override selfupdate.Apply consults, so a test that stages a fake executable
	// exercises the actual binary the handoff execs, not the real running process's path.
	exe, err := selfupdate.ExecutableOverride()
	if err != nil {
		return fleetReconcileResult{status: "failed", output: []string{err.Error()}}, nil
	}
	out, runErr := exec.Command(exe, "init", fleetHome).CombinedOutput()
	lines := nonEmptyLines(string(out))
	if runErr != nil {
		lines = append(lines, runErr.Error())
		return fleetReconcileResult{status: "failed", output: lines}, nil
	}
	return fleetReconcileResult{status: "ok", output: lines}, nil
}

func nonEmptyLines(text string) []string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

func updateDoc(info selfupdate.BuildInfo, target selfupdate.Target, available, updated bool, fleetReconcile string, notes []string) axi.Doc {
	var doc axi.Doc
	doc.Field("current", info.Version)
	doc.Field("current_channel", info.Channel)
	doc.Field("current_commit", selfupdate.DisplayCommit(info.Commit))
	doc.Field("distribution", info.Distribution)
	doc.Field("latest", target.Version)
	doc.Field("latest_channel", target.Channel)
	doc.Field("latest_commit", selfupdate.DisplayCommit(target.Commit))
	doc.Bool("update_available", available)
	doc.Bool("updated", updated)
	doc.Field("fleet_reconcile", fleetReconcile)
	doc.List("notes", notes)
	return doc
}

func updateHelp(target selfupdate.Target, explicit bool) string {
	command := "hand update"
	if explicit {
		command += " --channel " + target.Channel
	}
	return "Run `" + command + "` to install " + target.Version + ", which also reconciles this home's generated fleet surfaces via hand init"
}

// A package manager's install is its own to manage, and a go/source build was never a
// hand release artifact, so self-replacing either would surprise whatever placed it.
func ownershipRefusalHelp(distribution string) string {
	command := selfupdate.UpgradeCommand(distribution)
	switch distribution {
	case selfupdate.DistributionGo, selfupdate.DistributionSource:
		return "hand will not replace a " + distribution + " build; " + command
	default:
		return "hand will not replace a package-manager-owned build; " + command
	}
}

func releaseNoteLines(notes string) []string {
	return nonEmptyLines(notes)
}
