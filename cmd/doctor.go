package cmd

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/atqamz/hand/internal/agentsmd"
	"github.com/atqamz/hand/internal/axi"
	"github.com/atqamz/hand/internal/harness"
	"github.com/atqamz/hand/internal/home"
	"github.com/atqamz/hand/internal/integration"
	"github.com/atqamz/hand/internal/project"
	"github.com/atqamz/hand/internal/routing"
	"github.com/atqamz/hand/internal/selfupdate"
	"github.com/atqamz/hand/internal/skill"
	"github.com/atqamz/hand/internal/toolchain"
	"github.com/spf13/cobra"
)

// Every fleet needs these regardless of which projects are registered, so bootstrap may install
// them with consent; gh and a coding-agent harness are checked separately below, since the
// former is only required by some project delivery modes and hand never picks the latter.
var foundationalTools = []string{"git", "treehouse", "herdr"}

type doctorSeverity string

const (
	doctorError   doctorSeverity = "error"
	doctorWarning doctorSeverity = "warning"
	doctorInfo    doctorSeverity = "info"
)

type doctorFinding struct {
	Line     int
	Severity doctorSeverity
	Text     string
}

var doctorFields = []axi.Column[doctorFinding]{
	{Name: "line", Value: func(f doctorFinding) string {
		if f.Line == 0 {
			return "none"
		}
		return strconv.Itoa(f.Line)
	}},
	{Name: "severity", Value: func(f doctorFinding) string { return string(f.Severity) }},
	{Name: "finding", Value: func(f doctorFinding) string { return f.Text }},
}

var doctorDefaultFields = []string{"line", "severity", "finding"}

// This is the readiness contract a bootstrapper, a human and a supervising agent all read off
// the same `hand doctor` output instead of any of them inventing a second schema.
type toolReadiness struct {
	Tool      string
	Installed bool
	Required  bool
}

type harnessReadiness struct {
	Name      string
	Installed bool
}

var toolReadinessFields = []axi.Column[toolReadiness]{
	{Name: "tool", Value: func(t toolReadiness) string { return t.Tool }},
	{Name: "installed", Value: func(t toolReadiness) string { return strconv.FormatBool(t.Installed) }},
	{Name: "required", Value: func(t toolReadiness) string { return strconv.FormatBool(t.Required) }},
}

var harnessReadinessFields = []axi.Column[harnessReadiness]{
	{Name: "name", Value: func(h harnessReadiness) string { return h.Name }},
	{Name: "installed", Value: func(h harnessReadiness) string { return strconv.FormatBool(h.Installed) }},
}

func newDoctorCmd(info selfupdate.BuildInfo) *cobra.Command {
	var fields []string
	var failIfNotReady bool

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check fleet health: AGENTS.md, the bundled skill, project gates, routing, and tools",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			fleetHome, err := home.Resolve()
			if err != nil {
				return asPrecondition(err)
			}
			findings, err := doctorFindings(fleetHome)
			if err != nil {
				return err
			}
			cols, err := pickFields(doctorFields, fields, doctorDefaultFields)
			if err != nil {
				return err
			}

			path := filepath.Join(fleetHome, "AGENTS.md")
			failing := 0
			for _, finding := range findings {
				if finding.Severity == doctorError {
					failing++
				}
			}

			projects, err := project.ListReadOnly(fleetHome)
			if err != nil {
				return err
			}
			harnesses := doctorHarnesses()
			runtimeStatus, err := doctorRuntimeStatus()
			if err != nil {
				return err
			}
			tools := doctorManagedTools(runtimeStatus, projects)
			integrations, err := integration.DefaultStore().List()
			if err != nil {
				return err
			}
			blocking := doctorBlockingForRuntime(failing, runtimeStatus.Ready, tools, harnesses)
			next := doctorNext(blocking)

			var doc axi.Doc
			doc.Field("file", path)
			doc.Field("version", info.Version)
			doc.Field("channel", info.Channel)
			doc.Field("commit", selfupdate.DisplayCommit(info.Commit))
			doc.Field("distribution", info.Distribution)
			doc.Int("count", len(findings))
			doc.Int("violations", failing)
			doc.Bool("runtime_ready", runtimeStatus.Ready)
			doc.Field("runtime_target", runtimeStatus.Target)
			doc.Field("runtime_id", valueOrNone(runtimeStatus.RuntimeID))
			doc.Field("runtime_bundle", valueOrNone(runtimeStatus.BundleDir))
			doc.Field("git_version", valueOrNone(runtimeStatus.GitVersion))
			doc.Field("treehouse_version", valueOrNone(runtimeStatus.TreehouseVersion))
			doc.Field("herdr_version", valueOrNone(runtimeStatus.HerdrVersion))
			doc.Field("runtime_reason", valueOrNone(runtimeStatus.Reason))
			axi.Table(&doc, "tools", tools, toolReadinessFields)
			axi.Table(&doc, "harnesses", harnesses, harnessReadinessFields)
			doc.Bool("ready", len(blocking) == 0)
			doc.List("blocking", blocking)
			doc.List("next", next)
			axi.Table(&doc, "integrations", integrations, integrationFields)
			axi.Table(&doc, "findings", findings, cols)
			doc.Help(doctorHelp(len(findings), failing)...)
			if err := doc.Render(cmd.OutOrStdout()); err != nil {
				return err
			}
			if failing > 0 {
				return fmt.Errorf("%s: %d issue(s) found", path, failing)
			}
			if failIfNotReady && len(blocking) > 0 {
				return fmt.Errorf("fleet is not ready: %d blocking condition(s)", len(blocking))
			}
			return nil
		},
	}

	cmd.Flags().StringSliceVar(&fields, "fields", nil, fieldsFlagUsage(doctorFields, doctorDefaultFields))
	cmd.Flags().BoolVar(&failIfNotReady, "fail-if-not-ready", false, "return an error when readiness has blocking conditions")
	return cmd
}

func doctorRuntimeStatus() (toolchain.Status, error) {
	store, err := toolchain.DefaultStore()
	if err != nil {
		return toolchain.Status{}, err
	}
	status, err := store.Status("", "")
	if err != nil {
		return toolchain.Status{}, err
	}
	if legacyDoctorCompatibility() && !status.Ready && onPath("git") && onPath("treehouse") && onPath("herdr") {
		status.Ready = true
		status.Reason = "test-only legacy tool fixture"
	}
	return status, nil
}

func legacyDoctorCompatibility() bool {
	return legacyDoctorCompat
}

func doctorFindings(fleetHome string) ([]doctorFinding, error) {
	findings := make([]doctorFinding, 0)

	agentsViolations, err := agentsmd.Check(fleetHome)
	if err != nil {
		return nil, err
	}
	for _, violation := range agentsViolations {
		severity := doctorError
		if violation.Severity == agentsmd.SeverityInfo {
			severity = doctorInfo
		}
		findings = append(findings, doctorFinding{Line: violation.Line, Severity: severity, Text: violation.Text})
	}

	skillViolations, err := skill.Check(fleetHome)
	if err != nil {
		return nil, err
	}
	for _, violation := range skillViolations {
		severity := doctorError
		if violation.Severity == skill.SeverityInfo {
			severity = doctorInfo
		}
		findings = append(findings, doctorFinding{Severity: severity, Text: violation.Text})
	}
	if legacyDoctorCompatibility() {
		for _, tool := range foundationalTools {
			if !onPath(tool) {
				findings = append(findings, doctorFinding{Severity: doctorWarning, Text: fmt.Sprintf("required tool %q is not on PATH", tool)})
			}
		}
	}

	projects, err := project.ListReadOnly(fleetHome)
	if err != nil {
		return nil, err
	}
	for _, p := range projects {
		if issue := gateIssue(fleetHome, p); issue != "" {
			findings = append(findings, doctorFinding{Severity: doctorError, Text: fmt.Sprintf("project %q no-mistakes gate is %s", p.Name, issue)})
		}
	}

	detection, err := harness.DetectCurrent()
	if err != nil {
		return nil, err
	}
	snapshot, err := routing.LoadExecutionSnapshot(fleetHome, detection.Name, true)
	if err != nil {
		return nil, err
	}
	if len(snapshot.Config.Profiles) == 0 && len(snapshot.Config.Routes) == 0 && onlyMissingRoutes(snapshot.Config.Problems) {
		severity := doctorWarning
		text := legacyRoutingFinding(snapshot.Legacy, "routing falls back to legacy defaults without explicit intent")
		if snapshot.Legacy.ConfiguredHarness != "" {
			severity = doctorInfo
			text = legacyRoutingFinding(snapshot.Legacy, "routing resolves through explicit legacy defaults")
		}
		return append(findings, doctorFinding{Severity: severity, Text: text}), nil
	}
	for _, problem := range snapshot.Config.Problems {
		findings = append(findings, doctorFinding{Severity: doctorWarning, Text: routingProblemFinding(problem)})
		if problem.Kind != "" && problem.ExecutionClass != "" {
			findings = append(findings, doctorFinding{Severity: doctorWarning, Text: routingDecisionProblem(problem)})
		}
	}
	if len(snapshot.Config.Profiles) == 0 && len(snapshot.Config.Routes) == 0 {
		return append(findings, doctorFinding{Severity: doctorWarning, Text: legacyRoutingFinding(snapshot.Legacy, "routing effective fallback after configuration problems")}), nil
	}
	for _, route := range snapshot.Config.Routes {
		profile, found := profileByName(snapshot.Config.Profiles, route.Profile)
		if found {
			findings = append(findings, doctorFinding{Severity: doctorInfo, Text: fmt.Sprintf("routing decision: %s.%s -> profile %q -> %s", route.Kind, route.ExecutionClass, profile.Name, profileDetails(profile))})
		}
	}
	return findings, nil
}

// A local-only fleet never needs gh, while a registered project delivering through direct-pr or
// no-mistakes does.
func ghRequired(projects []project.Project) bool {
	for _, p := range projects {
		if p.Mode == project.ModeDirectPR || p.Mode == project.ModeNoMistakes {
			return true
		}
	}
	return false
}

func doctorTools(projects []project.Project) []toolReadiness {
	tools := make([]toolReadiness, 0, len(foundationalTools)+1)
	for _, tool := range foundationalTools {
		tools = append(tools, toolReadiness{Tool: tool, Installed: onPath(tool), Required: true})
	}
	tools = append(tools, toolReadiness{Tool: "gh", Installed: optionalInstalled("github/gh"), Required: ghRequired(projects)})
	return tools
}

func doctorManagedTools(status toolchain.Status, projects []project.Project) []toolReadiness {
	if legacyDoctorCompatibility() {
		return doctorTools(projects)
	}
	return []toolReadiness{
		{Tool: "git", Installed: status.Ready, Required: true},
		{Tool: "treehouse", Installed: status.Ready, Required: true},
		{Tool: "herdr", Installed: status.Ready, Required: true},
		{Tool: "gh", Installed: optionalInstalled("github/gh"), Required: ghRequired(projects)},
	}
}

func optionalInstalled(id string) bool {
	status, err := integration.DefaultStore().List()
	if err != nil {
		return false
	}
	for _, item := range status {
		if item.Capability.ID == id {
			return item.State == integration.StateInstalled
		}
	}
	return false
}

func doctorBlockingForRuntime(failing int, runtimeReady bool, tools []toolReadiness, harnesses []harnessReadiness) []string {
	if legacyDoctorCompatibility() {
		return doctorBlocking(failing, tools, harnesses)
	}
	blocking := make([]string, 0)
	if failing > 0 {
		blocking = append(blocking, "fleet-health")
	}
	if !runtimeReady {
		blocking = append(blocking, "runtime")
	}
	for _, tool := range tools {
		if tool.Tool != "git" && tool.Tool != "treehouse" && tool.Tool != "herdr" && tool.Required && !tool.Installed {
			blocking = append(blocking, tool.Tool)
		}
	}
	if !anyHarnessInstalled(harnesses) {
		blocking = append(blocking, "harness")
	}
	return blocking
}

// Every supported coding-agent harness is reported, never a preferred one: bootstrap and doctor
// both only ever detect, they do not choose.
func doctorHarnesses() []harnessReadiness {
	names := harness.Names()
	out := make([]harnessReadiness, 0, len(names))
	for _, name := range names {
		out = append(out, harnessReadiness{Name: name, Installed: onPath(name)})
	}
	return out
}

func anyHarnessInstalled(harnesses []harnessReadiness) bool {
	for _, h := range harnesses {
		if h.Installed {
			return true
		}
	}
	return false
}

// The one list a bootstrapper needs to decide readiness without re-deriving the rules above: a
// fleet-health entry stands in for the error findings already detailed in `findings`, a missing
// required tool names itself, and a fleet with no installed harness cannot be driven.
func doctorBlocking(failing int, tools []toolReadiness, harnesses []harnessReadiness) []string {
	blocking := make([]string, 0)
	if failing > 0 {
		blocking = append(blocking, "fleet-health")
	}
	for _, tool := range tools {
		if tool.Required && !tool.Installed {
			blocking = append(blocking, tool.Tool)
		}
	}
	if !anyHarnessInstalled(harnesses) {
		blocking = append(blocking, "harness")
	}
	return blocking
}

// One exact recovery action per blocking entry, in the same order, so a caller never has to
// reconcile two lists that could drift apart.
func doctorNext(blocking []string) []string {
	next := make([]string, 0, len(blocking))
	for _, item := range blocking {
		switch item {
		case "fleet-health":
			next = append(next, "resolve every error finding reported above")
		case "harness":
			next = append(next, "install and authenticate at least one supported coding-agent harness (see `harnesses` above), then run hand doctor")
		case "runtime":
			next = append(next, "run `hand runtime ensure`")
		default:
			next = append(next, fmt.Sprintf("install %s", item))
		}
	}
	return next
}

func onlyMissingRoutes(problems []routing.ConfigProblem) bool {
	for _, problem := range problems {
		if problem.Code != routing.ConfigProblemMissingRoute {
			return false
		}
	}
	return true
}

func legacyRoutingFinding(defaults routing.LegacyDefaults, prefix string) string {
	details := []string{fmt.Sprintf("harness %q", defaults.Harness)}
	if model := defaults.Models[defaults.Harness]; model != "" {
		details = append(details, fmt.Sprintf("model %q", model))
	}
	if effort := defaults.Efforts[defaults.Harness]; effort != "" {
		details = append(details, fmt.Sprintf("effort %q", effort))
	}
	return prefix + ": " + strings.Join(details, ", ")
}

func profileByName(profiles []routing.Profile, name string) (routing.Profile, bool) {
	for _, profile := range profiles {
		if profile.Name == name {
			return profile, true
		}
	}
	return routing.Profile{}, false
}

func profileDetails(profile routing.Profile) string {
	details := []string{fmt.Sprintf("harness %q", profile.Harness)}
	if profile.Model != "" {
		details = append(details, fmt.Sprintf("model %q", profile.Model))
	}
	if profile.Effort != "" {
		details = append(details, fmt.Sprintf("effort %q", profile.Effort))
	}
	return strings.Join(details, ", ")
}

func routingDecisionProblem(problem routing.ConfigProblem) string {
	details := strings.TrimPrefix(problem.Message, "route ")
	return fmt.Sprintf("routing decision: %s.%s -> unavailable (%s)", problem.Kind, problem.ExecutionClass, details)
}

func routingProblemFinding(problem routing.ConfigProblem) string {
	if problem.Kind != "" && problem.ExecutionClass != "" {
		return fmt.Sprintf("routing drift: route %s.%s %s", problem.Kind, problem.ExecutionClass, strings.TrimPrefix(problem.Message, "route "))
	}
	if problem.Profile != "" {
		return fmt.Sprintf("routing drift: profile %q %s", problem.Profile, problem.Message)
	}
	return "routing drift: " + problem.Message
}

// hand doctor fixes nothing, so what it owes a reader is which findings are
// theirs to edit and which one command repairs on its own.
func doctorHelp(count, failing int) []string {
	if count == 0 {
		return nil
	}
	if failing == 0 {
		return []string{"No error findings, so this run passed; inspect warnings and info before the next dispatch"}
	}
	return []string{
		"Resolve every error finding; hand doctor reports and never rewrites",
		"Run `hand init` to restore AGENTS.md or the bundled skill; a foreign file conflict at a skill destination must be moved aside by hand first",
		"Inspect project gates and routing configuration for the remaining fleet health findings",
	}
}
