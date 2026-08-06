package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/atqamz/secondhand/internal/agentsmd"
	"github.com/atqamz/secondhand/internal/axi"
	"github.com/atqamz/secondhand/internal/sessionhook"
	"github.com/atqamz/secondhand/internal/store"
	"github.com/spf13/cobra"
)

var toolCandidates = []string{"treehouse", "herdr", "no-mistakes", "gh"}

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

			if err := initLayout(home); err != nil {
				return err
			}
			if err := initMarker(home); err != nil {
				return err
			}
			migrated, err := migrateWorkerSettings(home)
			if err != nil {
				return err
			}
			refreshed, err := agentsmd.Refresh(home)
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

			var doc axi.Doc
			doc.Field("result", "initialized")
			doc.Field("home", home)
			doc.Field("agents_md", writtenOrUnchanged(refreshed))
			doc.Field("session_hook", removedOrUnchanged(hookRemoved))
			doc.List("migrated", migrated)
			cfg, err := currentWorkerConfig(home)
			if err != nil {
				return err
			}
			appendWorkerConfig(&doc, cfg)
			doc.List("missing_tools", missingTools())
			doc.Help("Start a supervising session in this home; it reports the worker defaults still missing and asks you for each one (`hand config set <key> <value>`)",
				"Read AGENTS.md in this home for how a supervising agent is meant to drive it",
				"Run `hand project add <repo-url>` to register the first project",
				"AGENTS.md and its CLAUDE.md symlink carry the startup integration across harnesses")
			return doc.Render(cmd.OutOrStdout())
		},
	}
}

// Reported rather than resolved: a missing tool is a diagnostic the first session explains in context,
// and turning bootstrap into a prerequisite wizard is what this command exists not to be.
func missingTools() []string {
	var missing []string
	for _, t := range toolCandidates {
		if !onPath(t) {
			missing = append(missing, t)
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
	return initSkeletonFiles(home)
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

// Creates state/hand.db up front so home.IsHome's marker exists as soon as init returns, rather than
// waiting for the first command that happens to touch machine state. store.Open is safe to call
// repeatedly.
func initMarker(home string) error {
	db, err := store.Open(home)
	if err != nil {
		return fmt.Errorf("create state/hand.db: %w", err)
	}
	return db.Close()
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
