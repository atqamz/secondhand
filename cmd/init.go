package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/atqamz/hand/internal/agentsmd"
	"github.com/atqamz/hand/internal/axi"
	"github.com/atqamz/hand/internal/home"
	"github.com/atqamz/hand/internal/integration"
	"github.com/atqamz/hand/internal/project"
	"github.com/atqamz/hand/internal/registry"
	"github.com/atqamz/hand/internal/routing"
	"github.com/atqamz/hand/internal/sessionhook"
	"github.com/atqamz/hand/internal/skill"
	"github.com/atqamz/hand/internal/state"
	"github.com/atqamz/hand/internal/toolchain"
	"github.com/spf13/cobra"
)

var skillFields = []axi.Column[skill.DestinationResult]{
	{Name: "dir", Value: func(r skill.DestinationResult) string { return r.Dir }},
	{Name: "outcome", Value: func(r skill.DestinationResult) string { return string(r.Outcome) }},
}

const backlogSkeleton = `# Backlog

## Queue

## In Progress

## Done
`

const projectsSkeleton = `# Projects

`

const operatorSkeleton = `# Operator

Standing constraints and preferences. They outrank the agent's own judgment.

## Identity

## Authority

## Hard constraints

## Preferences
`

const learningsSkeleton = "# Learnings\n"

const doneArchiveSkeleton = "# Done archive\n"

const noteArchiveSkeleton = "# Note archive\n"

// Bootstrap only: it asks nothing, reads no stdin, and writes no worker default. What the fleet should
// dispatch is settled by the operator in the first supervising session (`hand config`), because a value
// invented at bootstrap time is indistinguishable from one the operator chose.
func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init [path]",
		Short: "Create or refresh a fleet home; asks no questions",
		Args:  usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			home, err := resolveInitHome(cwd, args)
			if err != nil {
				return err
			}
			if err := preflightInitTarget(home); err != nil {
				return err
			}

			if err := initLayout(home); err != nil {
				return err
			}
			if err := initMarker(home); err != nil {
				return err
			}
			fleetID, err := state.FleetID(home)
			if err != nil {
				return fmt.Errorf("read Fleet identity after initialization: %w", err)
			}
			registryOutcome, registryErr := registerFleet(home, fleetID)
			migrated, err := migrateWorkerSettings(home)
			if err != nil {
				return err
			}
			refresh, err := agentsmd.Refresh(home)
			if err != nil {
				return err
			}
			skillResults, err := skill.Refresh(home)
			if err != nil {
				return err
			}
			exe, err := os.Executable()
			if err != nil {
				return fmt.Errorf("resolve the hand executable: %w", err)
			}
			hookRemoved, err := sessionhook.Remove(home, exe)
			if err != nil {
				return err
			}

			if err := warnHandHomeMismatch(cmd.ErrOrStderr(), home); err != nil {
				return err
			}
			runtimeStatus, err := doctorRuntimeStatus()
			if err != nil {
				return err
			}

			var doc axi.Doc
			doc.Field("result", "initialized")
			doc.Field("home", home)
			doc.Field("fleet_id", fleetID)
			doc.Field("registry", registryOutcome)
			doc.Field("agents_md", writtenOrUnchanged(refresh.Changed))
			doc.Field("agents_md_legacy_archive", noneIfEmpty(refresh.ArchivedPath))
			axi.Table(&doc, "skill", skillResults, skillFields)
			doc.Int("skill_conflicts", skillConflictCount(skillResults))
			doc.Field("session_hook", removedOrUnchanged(hookRemoved))
			doc.Bool("runtime_ready", runtimeStatus.Ready)
			doc.Field("runtime_target", runtimeStatus.Target)
			doc.Field("runtime_id", valueOrNone(runtimeStatus.RuntimeID))
			doc.Field("runtime_reason", valueOrNone(runtimeStatus.Reason))
			doc.List("migrated", migrated)
			cfg, err := currentWorkerConfig(home)
			if err != nil {
				return err
			}
			appendWorkerConfig(&doc, cfg)
			doc.List("missing_integrations", missingIntegrations())
			effectiveSettingsHelp := "Start a supervising session in this home; no supported worker harness is configured or detected, so it reports the unanswered harness choice"
			switch cfg.settings[0].state {
			case stateConfigured:
				effectiveSettingsHelp = "Start a supervising session in this home; it uses the configured worker harness and any explicit model or effort overrides, with native defaults where unset"
			case stateDetected:
				effectiveSettingsHelp = "Start a supervising session in this home; it detects the current harness and uses its native model and effort defaults"
			}
			help := []string{
				effectiveSettingsHelp,
				"Run `hand config set <key> <value>` only to persist an explicit worker override",
				"Read AGENTS.md in this home for how a supervising agent is meant to drive it",
				"Run `hand project add <source>` or `hand project create <name>` to register the first project",
				"AGENTS.md and its CLAUDE.md reference carry the startup integration across harnesses",
			}
			if refresh.ArchivedPath != "" {
				help = append(help, fmt.Sprintf("This home's previous AGENTS.md content was archived verbatim at %s; review it and relocate anything still useful yourself", refresh.ArchivedPath))
			}
			if n := skillConflictCount(skillResults); n > 0 {
				help = append(help, fmt.Sprintf("%d bundled-skill destination(s) already hold a foreign file at the managed entry path and were left untouched; move each aside, then run hand init again to install the skill there", n))
			}
			doc.Help(help...)
			if err := doc.Render(cmd.OutOrStdout()); err != nil {
				return err
			}
			if registryErr != nil {
				return fmt.Errorf("fleet initialized at %s with fleet_id %s, but registry discovery update failed: %w", home, fleetID, registryErr)
			}
			return nil
		},
	}
}

func preflightInitTarget(target string) error {
	info, err := os.Stat(target)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect init target %s: %w", target, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("init target %s exists and is not a directory", target)
	}
	known, err := home.IsHome(target)
	if err != nil {
		return fmt.Errorf("inspect init target %s: %w", target, err)
	}
	if known {
		if err := state.ValidateInitTarget(target); err != nil {
			return fmt.Errorf("init target %s is recognized but unsafe to reconcile: %w; refusing before mutation", target, err)
		}
		return nil
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return fmt.Errorf("inspect init target %s: %w", target, err)
	}
	if len(entries) == 0 {
		return nil
	}
	runtimeRoot, err := handOwnedRuntimeRoot()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "AGENTS.md":
			continue
		case ".secondhand":
			if runtimeRoot == filepath.Join(target, entry.Name()) {
				continue
			}
		}
		return fmt.Errorf("init target %s exists, is not empty, and is not a recognized Secondhand fleet; refusing to adopt it", target)
	}
	return nil
}

func handOwnedRuntimeRoot() (string, error) {
	store, err := toolchain.DefaultStore()
	if err != nil {
		return "", fmt.Errorf("inspect private runtime store: %w", err)
	}
	return filepath.Clean(store.Root), nil
}

func registerFleet(home, fleetID string) (string, error) {
	db, err := registry.Open()
	if err != nil {
		return "failed", err
	}
	defer func() { _ = db.Close() }()
	if err := db.Register(home, fleetID, time.Now().UTC()); err != nil {
		return "failed", err
	}
	fleets, err := db.List(home)
	if err != nil {
		return "failed", err
	}
	for _, fleet := range fleets {
		if fleet.ID == fleetID && fleet.State == registry.StateDuplicate {
			return string(fleet.State), nil
		}
	}
	return "registered", nil
}

// Reported rather than resolved: a missing tool is a diagnostic the first session explains in context,
// and turning bootstrap into a prerequisite wizard is what this command exists not to be.
func missingIntegrations() []string {
	var missing []string
	statuses, err := integration.DefaultStore().List()
	if err != nil {
		return []string{"unavailable: " + err.Error()}
	}
	for _, status := range statuses {
		if status.State == integration.StateMissing {
			missing = append(missing, status.Capability.ID)
		}
	}
	return missing
}

func writtenOrUnchanged(changed bool) string {
	if changed {
		return "written"
	}
	return "unchanged"
}

func skillConflictCount(results []skill.DestinationResult) int {
	n := 0
	for _, r := range results {
		if r.Outcome == skill.OutcomeConflict {
			n++
		}
	}
	return n
}

func noneIfEmpty(path string) string {
	if path == "" {
		return "none"
	}
	return path
}

func removedOrUnchanged(changed bool) string {
	if changed {
		return "removed"
	}
	return "unchanged"
}

func initLayout(home string) error {
	if err := initDirs(home); err != nil {
		return err
	}
	if err := initSkeletonFiles(home); err != nil {
		return err
	}
	release, err := routing.Lock(home)
	if err != nil {
		return err
	}
	release()
	return nil
}

func initDirs(home string) error {
	dirs := []string{"state", "data", "projects", "config"}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	return nil
}

// Seeds every file it can and reports every one it could not, because the seeds are independent of each
// other.
func initSkeletonFiles(home string) error {
	// A fixed order: stopping at the first failure named an arbitrary victim, so two runs against the same
	// broken home disagreed about which file was at fault.
	files := []struct {
		rel     string
		content string
	}{
		{"data/backlog.md", backlogSkeleton},
		{"data/projects.md", projectsSkeleton},
		{"data/operator.md", operatorSkeleton},
		{"data/learnings.md", learningsSkeleton},
		{"data/done-archive.md", doneArchiveSkeleton},
		{"data/note-archive.md", noteArchiveSkeleton},
	}
	var errs []error
	for _, f := range files {
		path := filepath.Join(home, f.rel)
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("stat %s: %w", f.rel, err))
			continue
		}
		if err := os.WriteFile(path, []byte(f.content), 0o644); err != nil {
			errs = append(errs, fmt.Errorf("write %s: %w", f.rel, err))
		}
	}
	return errors.Join(errs...)
}

// Creates or upgrades machine state up front so init is the explicit boundary for schema,
// legacy task, and legacy project migrations. The composed operation is idempotent.
func initMarker(home string) error {
	if err := project.Migrate(home); err != nil {
		return fmt.Errorf("create state/hand.db: %w", err)
	}
	return nil
}

func resolveInitHome(cwd string, args []string) (string, error) {
	if len(args) > 1 {
		return "", &ExitError{Err: fmt.Errorf("init accepts at most one target path"), Code: 2}
	}
	if len(args) == 0 {
		return cwd, nil
	}
	home := args[0]
	if strings.TrimSpace(home) == "" {
		return "", &ExitError{Err: fmt.Errorf("init target path cannot be empty"), Code: 2}
	}
	if !filepath.IsAbs(home) {
		home = filepath.Join(cwd, home)
	}
	return filepath.Clean(home), nil
}

// Reports the one asymmetry in home handling: init creates the home its argument or working directory
// names, while every other command resolves HAND_HOME first, so an operator who exported HAND_HOME and
// initialized somewhere else would otherwise get a home nothing ever uses.
func warnHandHomeMismatch(w io.Writer, home string) error {
	handHome := os.Getenv("HAND_HOME")
	if handHome == "" {
		return nil
	}
	display := handHome
	if abs, err := filepath.Abs(handHome); err == nil {
		if abs == home {
			return nil
		}
		display = abs
	}
	_, err := fmt.Fprintf(w, "warning: HAND_HOME is set to %s, so every other hand command will use that home, not %s\n", display, home)
	return err
}
