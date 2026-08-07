package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/atqamz/secondhand/internal/axi"
	"github.com/atqamz/secondhand/internal/harness"
	"github.com/atqamz/secondhand/internal/herdr"
	"github.com/atqamz/secondhand/internal/home"
	"github.com/atqamz/secondhand/internal/project"
	"github.com/atqamz/secondhand/internal/state"
	"github.com/atqamz/secondhand/internal/worktree"
	"github.com/spf13/cobra"
)

func newPromoteCmd() *cobra.Command {
	var harnessName string
	var model string
	var effort string
	var skipGateCheck bool

	cmd := &cobra.Command{
		Use:   "promote <id>",
		Short: "Promote a completed scout task into a ship task",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			id := args[0]
			home, err := home.Resolve()
			if err != nil {
				return asPrecondition(err)
			}

			release, err := state.Lock(home, "task:"+id)
			if err != nil {
				return fmt.Errorf("lock task %q: %w", id, err)
			}
			defer release()

			t, err := state.Read(home, id)
			if err != nil {
				return asPrecondition(err)
			}
			if t.Kind != state.KindScout {
				return &ExitError{Err: fmt.Errorf("task %q is not a scout", id), Code: 3}
			}

			reportRel := filepath.Join("data", id, "report.md")
			if _, err := os.Stat(filepath.Join(home, reportRel)); err != nil {
				return &ExitError{Err: fmt.Errorf("scout report not found at %s", reportRel), Code: 3}
			}

			client := herdr.NewClient()
			if s := herdr.Status(paneAgentStatus(client, t.Herdr.PaneID)); !s.NotBusy() && s != herdr.StatusUnknown {
				return &ExitError{Err: fmt.Errorf("task %q is not a completed scout (agent state: %s)", id, s), Code: 3}
			}

			briefRel := filepath.Join("data", id, "brief.md")
			briefAbs := filepath.Join(home, briefRel)
			if _, err := os.Stat(briefAbs); err != nil {
				return &ExitError{Err: fmt.Errorf("brief not found at %s", briefRel), Code: 3}
			}

			harnessFromFlag := harnessName != ""
			if !harnessFromFlag {
				cfg, err := currentWorkerConfig(home)
				if err != nil {
					return err
				}
				harnessName = cfg.harness
				if harnessName == "" {
					return &ExitError{Err: fmt.Errorf("current supervisor harness is unknown and no worker harness override is configured; run hand config set harness <name>"), Code: 3}
				}
			}
			if !harness.IsSupported(harnessName) {
				return usageValue(harnessFromFlag, fmt.Errorf("harness %q not recognized", harnessName))
			}
			proj, exists, err := project.Find(home, t.Project)
			if err != nil {
				return err
			}
			if !exists {
				return &ExitError{Err: fmt.Errorf("project %q not registered", t.Project), Code: 3}
			}

			clonePath := filepath.Join(home, "projects", proj.Name)
			if err := gatePreflight(cmd, proj, clonePath, skipGateCheck); err != nil {
				return err
			}

			var briefHasFrontMatter bool
			model, effort, briefHasFrontMatter, err = resolveTier(cmd, home, briefAbs, harnessName, model, effort)
			if err != nil {
				return err
			}

			releaseProject, err := state.Lock(home, "project:"+proj.Name)
			if err != nil {
				return fmt.Errorf("lock project %q: %w", proj.Name, err)
			}
			defer releaseProject()

			oldWorktree := t.Worktree
			oldWorkspaceID := t.Herdr.WorkspaceID
			oldTabID := t.Herdr.TabID

			lease, err := worktree.Get(clonePath, "hand:"+id)
			if err != nil {
				return fmt.Errorf("acquire treehouse worktree: %w", err)
			}
			wt := lease.Path
			releaseWorktree, err := state.Lock(home, "worktree:"+wt)
			if err != nil {
				return reportSpawnCleanup(fmt.Errorf("lock worktree %q: %w", wt, err), worktree.Return(wt, true))
			}
			defer releaseWorktree()

			if conflict, err := worktree.CheckCollision(home, lease, id); err != nil {
				return reportSpawnCleanup(err, worktree.Return(wt, true))
			} else if conflict != "" {
				return reportSpawnCleanup(&ExitError{Err: fmt.Errorf("worktree collision: %s already holds %s", conflict, wt), Code: 3}, worktree.Return(wt, true))
			}

			ws, tab, pane, rollback, err := acquireTaskWorkspace(client, wt, id, proj.Name)
			if err != nil {
				return reportSpawnCleanup(err, worktree.Return(wt, true))
			}

			// Same rollback contract as hand spawn: until state.Write records the promotion,
			// this call owns the new workspace or tab and must undo it on any failure; after
			// that the promoted task owns them and later warnings must not tear them down.
			promoted := false
			defer func() {
				if promoted {
					return
				}
				if closeErr := rollback(); closeErr != nil {
					err = reportSpawnCleanup(err, closeErr)
				}
			}()

			launchCmd, err := harness.Build(harnessName, harness.Options{
				Worktree:            wt,
				Brief:               briefAbs,
				FleetHome:           home,
				Model:               model,
				Effort:              effort,
				BriefHasFrontMatter: briefHasFrontMatter,
			})
			if err != nil {
				return reportSpawnCleanup(err, worktree.Return(wt, true))
			}

			if err := client.PaneRun(pane.PaneID, launchCmd); err != nil {
				return reportSpawnCleanup(fmt.Errorf("send launch command failed: %w", err), worktree.Return(wt, true))
			}

			if err := confirmLaunch(client, pane.PaneID, harnessName); err != nil {
				return reportSpawnCleanup(fmt.Errorf("confirm worker started: %w", err), worktree.Return(wt, true))
			}

			t.Kind = state.KindShip
			t.Harness = harnessName
			t.Model = model
			t.Effort = effort
			t.Worktree = wt
			t.LeaseID = lease.ID
			t.Herdr = state.Herdr{
				Session:     "default",
				WorkspaceID: ws.WorkspaceID,
				TabID:       tab.TabID,
				PaneID:      pane.PaneID,
			}
			// Everything below is pane-scoped and so describes a pane this task no longer has, none of it
			// evidence about the ship. The report cursor is carried instead: promote never touches
			// state/<id>.status, so the stream is continuous. Cleared here rather than left for a watch that may be off.
			t.DoneVerified = false
			// The delivery described the scout's report, not the ship run starting
			// here: left set, teardown would accept the ship task as terminal on a
			// delivery nobody made for its code.
			t.DeliveredAt = ""
			t.DeliveredReason = ""
			promotedAt := time.Now().UTC().Format(time.RFC3339)
			t.PaneStartedAt = promotedAt
			t.StatusChangedAt = promotedAt
			t.StatusChangedFor = ""
			t.LastReportState = ""
			t.LastReportNote = ""
			// The scout's harness process is gone along with its pane, and the ship's
			// runs against whatever quota exists now. Kept, the schedule would steer
			// the fresh pane on a clock the scout's refusal set.
			t.UsageLimitRetryAt = ""
			t.UsageLimitAttempts = 0
			if err := state.Write(home, t); err != nil {
				return reportSpawnCleanup(fmt.Errorf("write task state: %w", err), worktree.Return(wt, true))
			}
			promoted = true
			if err := state.ClearHoldIfKind(home, id, state.HoldKindLimit); err != nil {
				if _, printErr := fmt.Fprintf(cmd.ErrOrStderr(), "warning: clear usage-limit hold failed: %v\n", err); printErr != nil {
					return printErr
				}
			}

			if err := closeTaskTab(client, oldWorkspaceID, oldTabID); err != nil {
				if _, printErr := fmt.Fprintf(cmd.ErrOrStderr(), "warning: herdr tab close failed: %v\n", err); printErr != nil {
					return printErr
				}
			}
			if err := worktree.Return(oldWorktree, true); err != nil {
				if _, printErr := fmt.Fprintf(cmd.ErrOrStderr(), "warning: return scout worktree failed: %v\n", err); printErr != nil {
					return printErr
				}
			}

			var doc axi.Doc
			doc.Field("id", id)
			doc.Field("result", "promoted")
			doc.Field("kind", string(state.KindShip))
			doc.Field("was", string(state.KindScout))
			doc.Field("project", proj.Name)
			doc.Field("harness", harnessName)
			doc.Field("worktree", wt)
			doc.Help("The scout's worktree and pane are gone; run `hand status "+id+"` to read the ship worker",
				"The scout's delivery no longer counts for this task, so `hand deliver "+id+"` runs again on the code")
			return doc.Render(cmd.OutOrStdout())
		},
	}

	cmd.Flags().StringVar(&harnessName, "harness", "", "harness for the new ship worker (default: config/harness, then the detected supervisor harness)")
	cmd.Flags().StringVar(&model, "model", "", "model override for harnesses that support it")
	cmd.Flags().StringVar(&effort, "effort", "", "effort override for harnesses that support it")
	cmd.Flags().BoolVar(&skipGateCheck, "skip-gate-check", false, "dispatch even if the no-mistakes gate is not initialized, the clone path is missing from disk, or that path is not a git repository")
	return cmd
}
