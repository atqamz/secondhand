package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

func newSpawnCmd() *cobra.Command {
	var scout bool
	var harnessName string
	var model string
	var effort string
	var skipGateCheck bool

	cmd := &cobra.Command{
		Use:   "spawn <id> <project>",
		Short: "Spawn a worker agent in an isolated worktree",
		Args:  usageArgs(cobra.ExactArgs(2)),
		RunE: func(cmd *cobra.Command, args []string) (err error) {
			id := args[0]
			projectName := args[1]

			home, err := home.Resolve()
			if err != nil {
				return asPrecondition(err)
			}

			proj, exists, err := project.Find(home, projectName)
			if err != nil {
				return err
			}
			if !exists {
				return &ExitError{Err: fmt.Errorf("project %q not registered", projectName), Code: 3}
			}

			clonePath := filepath.Join(home, "projects", proj.Name)
			if err := gatePreflight(cmd, proj, clonePath, skipGateCheck); err != nil {
				return err
			}

			releaseClaim, err := state.Claim(home, id)
			if err != nil {
				return asPrecondition(err)
			}
			defer releaseClaim()

			// A hold outlives the task row it was set on, by design, so a torn-down task's open question stays
			// visible, and reusing the id would silently reattach it to unrelated work. Refused rather than
			// cleared: answering an operator hold is not hand's, and a limit hold means a worker is still here.
			held, hasHold, err := state.ReadHold(home, id)
			if err != nil {
				return asPrecondition(err)
			}
			if hasHold {
				return &ExitError{Err: fmt.Errorf("id %q has an open hold (%s: %s); clear it first: hand hold clear %s", id, held.Kind, held.Reason, id), Code: 3}
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

			client := herdr.NewClient()
			ws, tab, pane, rollback, err := acquireTaskWorkspace(client, wt, id, proj.Name)
			if err != nil {
				return reportSpawnCleanup(err, worktree.Return(wt, true))
			}

			// Disarms the rollback below once state.Write has durably recorded the task: from that point
			// its workspace and tab are owned by the running task, not by this call.
			spawned := false
			defer func() {
				if spawned {
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

			kind := state.KindShip
			if scout {
				kind = state.KindScout
			}

			spawnedAt := time.Now().UTC().Format(time.RFC3339)
			task := state.Task{
				ID:       id,
				Project:  proj.Name,
				Kind:     kind,
				Harness:  harnessName,
				Model:    model,
				Effort:   effort,
				Worktree: wt,
				LeaseID:  lease.ID,
				Brief:    briefRel,
				Herdr: state.Herdr{
					Session:     "default",
					WorkspaceID: ws.WorkspaceID,
					TabID:       tab.TabID,
					PaneID:      pane.PaneID,
				},
				CreatedAt:     spawnedAt,
				PaneStartedAt: spawnedAt,
			}
			if err := state.Write(home, task); err != nil {
				return reportSpawnCleanup(fmt.Errorf("write task state: %w", err), worktree.Return(wt, true))
			}
			spawned = true

			var doc axi.Doc
			doc.Field("id", id)
			doc.Field("result", "spawned")
			doc.Field("project", proj.Name)
			doc.Field("kind", string(kind))
			doc.Field("harness", harnessName)
			doc.Field("worktree", wt)
			doc.Help("Run `hand status "+id+"` to read what this worker reports",
				"Run `hand send "+id+" <message>` to steer it")
			return doc.Render(cmd.OutOrStdout())
		},
	}

	cmd.Flags().BoolVar(&scout, "scout", false, "mark as scout task (deliverable is a report, not a PR)")
	cmd.Flags().StringVar(&harnessName, "harness", "", "agent harness to launch (default: config/harness, then the detected supervisor harness)")
	cmd.Flags().StringVar(&model, "model", "", "model override for harnesses that support it")
	cmd.Flags().StringVar(&effort, "effort", "", "effort level for harnesses that support it")
	cmd.Flags().BoolVar(&skipGateCheck, "skip-gate-check", false, "dispatch even if the no-mistakes gate is not initialized, the clone path is missing from disk, or that path is not a git repository")
	return cmd
}

// Refuses to dispatch into a no-mistakes project whose gate is not initialized, rather than letting
// the worker discover it mid-run with nothing obliged to report it.
func gatePreflight(cmd *cobra.Command, proj project.Project, clonePath string, skipGateCheck bool) error {
	if proj.Mode != project.ModeNoMistakes {
		return nil
	}
	// The escape hatch for a project mid-migration; it still prints so bypassing it stays visible.
	if skipGateCheck {
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "warning: --skip-gate-check bypassing the no-mistakes gate check for project %q\n", proj.Name); err != nil {
			return err
		}
		return nil
	}
	// Asks the no-mistakes binary rather than reading its private state.sqlite, so a stale or missing
	// gate registration (renamed working_path, never-initialized repo) is caught here instead of
	// silently producing an ungated "done".
	gateState, err := project.GateStatus(clonePath)
	if err != nil {
		return fmt.Errorf("check no-mistakes gate for project %q: %w", proj.Name, err)
	}
	if gateState == project.GateNotInitialized {
		return &ExitError{Err: fmt.Errorf("no-mistakes gate not initialized for project %q, run: %s", proj.Name, project.GateInitCommand(clonePath)), Code: 3}
	}
	return nil
}

// The label hand gives, and searches for, a project's shared workspace. A bare project name is not a
// safe search key: herdr derives a workspace's label from its root directory's basename, so any
// workspace a human opens under a directory named after the project carries it (atqamz/secondhand#118).
func herdrWorkspaceLabel(projName string) string {
	// Prefixed the same way worktree.Get's pool name already does ("hand:"+id), so the label is one only
	// hand itself would ever set and a same-labelled workspace hand did not create can never match, no
	// matter how the herdr's workspace list happens to be ordered.
	return "hand:" + projName
}

// The caller must invoke the returned rollback, guarded by its own spawned/promoted flag,
// until state.Write durably records the task.
func acquireTaskWorkspace(client *herdr.Client, wt, id, projName string) (herdr.Workspace, herdr.Tab, herdr.Pane, func() error, error) {
	label := herdrWorkspaceLabel(projName)
	ws, found, err := client.FindWorkspaceByLabel(label)
	createdWorkspace := false
	var rootTab herdr.Tab
	var rootPane herdr.Pane
	if err == nil && !found {
		ws, rootTab, rootPane, err = client.WorkspaceCreate(wt, label)
		createdWorkspace = err == nil
	}
	if err != nil {
		return herdr.Workspace{}, herdr.Tab{}, herdr.Pane{}, nil, fmt.Errorf("herdr workspace lookup/create failed: %w", err)
	}

	tab, pane, err := acquireTaskTab(client, createdWorkspace, ws.WorkspaceID, wt, id, rootTab, rootPane)
	if err != nil {
		// No rollback reaches the caller on this path, so a workspace this call just created has
		// to be undone here or it stays open with nobody left holding its ID.
		err = reportSpawnCleanup(err, rollbackHerdr(client, createdWorkspace, ws.WorkspaceID, ""))
		return herdr.Workspace{}, herdr.Tab{}, herdr.Pane{}, nil, err
	}
	tabID := tab.TabID
	rollback := func() error {
		return rollbackHerdr(client, createdWorkspace, ws.WorkspaceID, tabID)
	}
	return ws, tab, pane, rollback, nil
}

// Returns the tab and pane a spawn-shaped lifecycle should use for the task.
func acquireTaskTab(client *herdr.Client, createdWorkspace bool, workspaceID, wt, id string, rootTab herdr.Tab, rootPane herdr.Pane) (herdr.Tab, herdr.Pane, error) {
	// herdr has no way to create an empty workspace: workspace create always creates a root tab and pane
	// at its cwd too, so a task that just created the workspace reuses that root tab (renamed to id)
	// rather than creating a second one, which would leave the root tab behind as an orphan shell.
	if createdWorkspace {
		if err := client.TabRename(rootTab.TabID, id); err != nil {
			return herdr.Tab{}, herdr.Pane{}, fmt.Errorf("herdr tab rename failed: %w", err)
		}
		return rootTab, rootPane, nil
	}
	// A task landing in an already-existing workspace still creates its own tab.
	tab, pane, err := client.TabCreate(workspaceID, wt, id)
	if err != nil {
		return herdr.Tab{}, herdr.Pane{}, fmt.Errorf("herdr tab create failed: %w", err)
	}
	return tab, pane, nil
}

// Undoes the herdr side of a failed spawn-shaped lifecycle.
func rollbackHerdr(client *herdr.Client, createdWorkspace bool, workspaceID, tabID string) error {
	// A workspace this call created goes away whole, because its only tab is the root tab acquireTaskTab
	// renames into the task's, and a failure before that rename leaves no tabID to close it by.
	if createdWorkspace {
		return client.WorkspaceClose(workspaceID)
	}
	if tabID == "" {
		return nil
	}
	// A pre-existing workspace is shared with other tasks and only loses the tab this call added to it.
	return closeTaskTab(client, workspaceID, tabID)
}

func reportSpawnCleanup(cause error, cleanupErrs ...error) error {
	cleanupErr := errors.Join(cleanupErrs...)
	if cleanupErr == nil {
		return cause
	}
	return fmt.Errorf("%w; cleanup failed: %w", cause, cleanupErr)
}

func configDefault(home, name, fallback string) string {
	data, err := os.ReadFile(filepath.Join(home, "config", name))
	if err != nil {
		return fallback
	}
	return strings.TrimSpace(string(data))
}

func configSeconds(home, name string, fallback time.Duration) (time.Duration, error) {
	raw := configDefault(home, name, "")
	if raw == "" {
		return fallback, nil
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid config/%s %q: %w", name, raw, err)
	}
	return time.Duration(seconds) * time.Second, nil
}
