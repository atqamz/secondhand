package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/atqamz/secondhand/internal/agentsmd"
	"github.com/atqamz/secondhand/internal/axi"
	"github.com/atqamz/secondhand/internal/harness"
	"github.com/atqamz/secondhand/internal/herdr"
	"github.com/atqamz/secondhand/internal/home"
	"github.com/atqamz/secondhand/internal/project"
	"github.com/atqamz/secondhand/internal/state"
	"github.com/spf13/cobra"
)

const sessionBacklogLimit = 80

type backlogSummary struct {
	Items  []string
	Queued int
}

var sessionProjectFields = []axi.Column[project.Project]{
	{Name: "name", Value: func(p project.Project) string { return p.Name }},
	{Name: "mode", Value: func(p project.Project) string { return p.Mode }},
	{Name: "url", Value: func(p project.Project) string { return orNone(p.URL) }},
	{Name: "upstream", Value: func(p project.Project) string { return orNone(p.Upstream) }},
}

func newSessionCmd(version string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "session",
		Short: "Manage a supervisor session",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "start",
		Short: "Load the bounded supervisor session context",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSessionStart(cmd, version)
		},
	})
	return cmd
}

func runSessionStart(cmd *cobra.Command, version string) error {
	if os.Getenv(harness.RoleEnv) == harness.WorkerRole {
		return &ExitError{Err: fmt.Errorf("supervisor session bootstrap is unavailable when %s=%s", harness.RoleEnv, harness.WorkerRole), Code: 3}
	}
	fleetHome, err := home.Resolve()
	if err != nil {
		return asPrecondition(err)
	}
	return renderSessionOverview(cmd, version, fleetHome)
}

func renderSessionOverview(cmd *cobra.Command, version, fleetHome string) error {
	operatorPath := filepath.Join(fleetHome, "data", "operator.md")
	operator, err := os.ReadFile(operatorPath)
	if err != nil {
		return sessionContextError(fleetHome, operatorPath, err)
	}
	backlogPath := filepath.Join(fleetHome, "data", "backlog.md")
	backlog, err := readBacklogSummary(backlogPath, sessionBacklogLimit)
	if err != nil {
		return sessionContextError(fleetHome, backlogPath, err)
	}
	projects, err := project.List(fleetHome)
	if err != nil {
		return err
	}
	cfg, err := currentWorkerConfig(fleetHome)
	if err != nil {
		return err
	}
	cols, err := pickFields(taskFields, nil, fleetDefaultFields)
	if err != nil {
		return err
	}
	views, holds, err := fleetViews(cmd, fleetHome, herdr.NewClient())
	if err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		exe = "unknown"
	}
	detected, source := cfg.detection.Name, cfg.detection.Source
	if detected == "" {
		detected = "unknown"
	}
	if source == "" {
		source = "unknown"
	}

	var doc axi.Doc
	doc.Field("session_bootstrap", "complete")
	doc.Field("tool", "hand")
	doc.Field("version", version)
	doc.Field("exec", tildePath(exe))
	doc.Field("home", tildePath(fleetHome))
	doc.Field("supervisor_harness", detected)
	doc.Field("supervisor_harness_source", source)
	appendWorkerConfig(&doc, cfg)
	doc.Field("operator", strings.TrimSuffix(string(operator), "\n"))
	doc.List("instructions", agentsmd.SupervisorInstructions())
	doc.Int("project_count", len(projects))
	axi.Table(&doc, "projects", projects, sessionProjectFields)
	doc.List("backlog", backlog.Items)
	appendFleetState(&doc, views, holds, cols)
	doc.Help(sessionNextAction(cfg, len(projects), backlog, views, holds))
	return doc.Render(cmd.OutOrStdout())
}

func sessionContextError(fleetHome, path string, err error) error {
	return &ExitError{
		Err:  fmt.Errorf("read required session context %s: %w; run `hand init %s` to restore it", path, err, fleetHome),
		Code: 3,
	}
}

func readBacklogSummary(path string, limit int) (backlogSummary, error) {
	f, err := os.Open(path)
	if err != nil {
		return backlogSummary{}, err
	}
	defer func() { _ = f.Close() }()

	var summary backlogSummary
	queued, truncated := false, false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		emit := strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ")
		if strings.HasPrefix(line, "## ") {
			heading := strings.TrimSpace(strings.TrimPrefix(line, "## "))
			queued = strings.EqualFold(heading, "queue") || strings.EqualFold(heading, "queued")
		}
		if (strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ")) && queued {
			summary.Queued++
		}
		if !emit {
			continue
		}
		if len(summary.Items) < limit {
			summary.Items = append(summary.Items, line)
		} else {
			truncated = true
		}
	}
	if err := scanner.Err(); err != nil {
		return backlogSummary{}, err
	}
	if truncated {
		summary.Items = append(summary.Items, "truncated: additional backlog identity lines omitted; read data/backlog.md for complete context")
	}
	return summary, nil
}

func sessionNextAction(cfg workerConfig, projectCount int, backlog backlogSummary, views []taskView, holds []state.Hold) string {
	if cfg.harness == "" {
		return workerConfigHelp(cfg)[0]
	}
	for _, view := range views {
		if view.unacked {
			return fmt.Sprintf("Run `hand status %s` and act on its unacknowledged worker event", view.task.ID)
		}
	}
	if len(holds) > 0 {
		return fmt.Sprintf("Run `hand status %s` and resolve its active hold", holds[0].ID)
	}
	if projectCount == 0 {
		return "Run `hand project add <repo-url>` to register the first project"
	}
	if backlog.Queued > 0 {
		return "Read `data/backlog.md` and prepare the queued task; dispatch it with `hand spawn <id> <project>` when its brief is ready"
	}
	if len(views) > 0 {
		return "Run `hand watch --until-event` as a background task and re-arm it after each event"
	}
	return "The fleet is ready and idle"
}
