// Package harness constructs the per-harness command used to launch a worker agent.
package harness

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/atqamz/secondhand/internal/agentsmd"
)

const (
	Claude     = "claude"
	Codex      = "codex"
	Grok       = "grok"
	Pi         = "pi"
	OpenCode   = "opencode"
	RoleEnv    = "HAND_ROLE"
	HomeEnv    = "HAND_HOME"
	WorkerRole = "worker"
)

// The one list of supported harnesses. Anything that offers a choice of harness derives it from here
// rather than repeating the names, so a harness added below is offered everywhere at once.
var names = []string{Claude, Codex, Grok, Pi, OpenCode}

func Names() []string {
	return slices.Clone(names)
}

func IsSupported(name string) bool {
	return slices.Contains(names, name)
}

// One interactive dialog a harness may show before it starts reading the brief; Match is checked
// against the pane's recent scrollback text. Exactly one of Keys and Refuse is set: Keys answer the
// dialog unattended, a non-empty Refuse leaves it deliberately for a human and its text says why.
type FirstRunPrompt struct {
	Name   string
	Match  *regexp.Regexp
	Keys   []string
	Refuse string
}

// A harness's verified pane signatures. Known are the dialogs whose exact wording is catalogued,
// and Unrecognized is a generic fallback for "some dialog is still on screen" that no Known entry
// matches.
type FirstRunPrompts struct {
	// The harness's own startup paint, a secondary signal that a pane herdr already reports a running
	// agent on has finished starting. A harness with no Ready signature is still confirmed, just by
	// waiting out the settle window.
	Ready        *regexp.Regexp
	Known        []FirstRunPrompt
	Unrecognized *regexp.Regexp
}

// Verified signatures per harness. Claude and codex have been observed on real first runs; every
// other harness gets the zero value until one is, leaving its launch confirmed on agent presence
// alone even if it parks on a dialog.
var firstRunPrompts = map[string]FirstRunPrompts{
	// Interactive claude gates on first-run dialogs --print skipped, and a fresh worktree path means
	// the trust one appears on every spawn, not just a fresh host.
	// cmd/launch.go's confirmLaunch clears them per spawn, not leaving them for an operator to notice.
	Claude: {
		// claude's own startup paint: the splash banner, or either composer footer hint once the REPL
		// is up (the bypass-mode line replaces the shortcuts hint when the footer is wide enough).
		// None can come from the echoed launch command, so a match means claude drew a frame itself.
		Ready: regexp.MustCompile(`Welcome\s+to\s+Claude\s+Code|\?\s+for\s+shortcuts|bypass\s+permissions\s+on`),
		Known: []FirstRunPrompt{
			{
				Name:  "workspace trust",
				Match: regexp.MustCompile(`Yes,\s+I\s+trust\s+this\s+folder`),
				Keys:  []string{"Enter"},
			},
			{
				// Defaults focus to "No, exit" - a blind Enter declines it, so this needs Down
				// first to reach "Yes, I accept" before confirming.
				Name:  "bypass permissions",
				Match: regexp.MustCompile(`Bypass\s+Permissions\s+mode`),
				Keys:  []string{"Down", "Enter"},
			},
			{
				// Nothing to do with the checked-out repo: claude's security dialog for managed
				// settings this host's org policy applies to every run. Accepting grants arbitrary code
				// execution and prompt interception - a host trust decision hand will not make for you.
				Name:   "managed settings",
				Match:  regexp.MustCompile(`Managed\s+settings\s+require\s+approval|Yes,\s+I\s+trust\s+these\s+settings`),
				Refuse: "this host has managed settings claude requires approval for, which hand will not accept for you; run claude yourself on this host once and accept the managed-settings prompt, then respawn",
			},
		},
		// Every dialog above ends in this footer, wrapped or not; a harness update that
		// reshuffles their wording still trips this fallback instead of confirmLaunch mistaking
		// the dialog for a started worker.
		Unrecognized: regexp.MustCompile(`Enter\s+to\s+confirm`),
	},
	Codex: {
		Known: []FirstRunPrompt{
			{
				Name:   "directory trust",
				Match:  regexp.MustCompile(`Do\s+you\s+trust\s+the\s+contents\s+of\s+this\s+directory\?`),
				Refuse: "trusting this directory enables project-local config, hooks, and exec policies; hand will not accept that security decision for you; run codex yourself in this checkout once and choose whether to trust it, then respawn",
			},
		},
		Unrecognized: regexp.MustCompile(`Press\s+enter\s+to\s+continue`),
	},
}

// FirstRunPromptsFor returns name's verified first-run signatures, or the zero value if name has
// none - a known, accepted gap rather than a bug: the catalogue is what makes an unattended launch
// safe, so it matters for every harness added here, not only claude.
func FirstRunPromptsFor(name string) FirstRunPrompts {
	return firstRunPrompts[name]
}

// The harnesses whose panes herdr has been observed labeling with an agent. Codex CLI 0.146.0 was
// launched through hand, reported as codex while resident, and cleared after /quit; the false
// entries still rely only on herdr's shipped detection manifests.
var agentDetectionVerified = map[string]bool{
	Claude:   true,
	Codex:    true,
	OpenCode: true,
}

// AgentDetectionVerified reports whether herdr's agent labeling has actually been exercised
// against name. A launch that never sees an agent means something different for the two cases,
// and the failure has to say which.
func AgentDetectionVerified(name string) bool {
	return agentDetectionVerified[name]
}

type Options struct {
	Worktree            string
	Brief               string
	FleetHome           string
	Model               string
	Effort              string
	BriefHasFrontMatter bool
}

var modelCapable = map[string]bool{
	Claude:   true,
	Codex:    true,
	OpenCode: true,
}

var effortCapable = map[string]bool{
	Claude: true,
	Codex:  true,
}

var promptCapable = map[string]bool{
	Claude:   true,
	Codex:    true,
	OpenCode: true,
}

// False means the caller must warn instead of silently dropping the model.
func SupportsModel(name string) bool {
	return modelCapable[name]
}

// False means the caller must warn instead of silently dropping the effort.
func SupportsEffort(name string) bool {
	return effortCapable[name]
}

// False means the builder hands the brief over as a file and has no prompt to append to, so
// briefPrompt's operator-decision rule and front-matter disclaimer never reach the worker.
// Carrying them properly needs flags verified against a real --help (atqamz/secondhand#36).
func CarriesPrompt(name string) bool {
	return promptCapable[name]
}

// Build constructs the shell command that cds into the worktree and launches the harness against
// the brief. Every launch is interactive, never one-shot: hand send steers a running pane, hand
// watch classifies its lifecycle, and a no-mistakes pipeline drives many turns a one-shot cannot.
func Build(name string, opts Options) (string, error) {
	var launch string
	// Flags for claude, codex, and opencode are verified against the installed CLI's own --help, and
	// this file is the source of truth for those three.
	switch name {
	case Claude:
		launch = buildClaude(opts)
	case Codex:
		launch = buildCodex(opts)
	case Grok:
		launch = buildGrok(opts)
	case Pi:
		launch = buildPi(opts)
	case OpenCode:
		launch = buildOpenCode(opts)
	default:
		return "", fmt.Errorf("harness %q not recognized", name)
	}
	env := ""
	if opts.FleetHome != "" {
		env = RoleEnv + "=" + WorkerRole + " " + HomeEnv + "=" + shellQuote(opts.FleetHome) + " "
	}
	return fmt.Sprintf("cd %s && %s%s", shellQuote(opts.Worktree), env, launch), nil
}

// Launches claude interactively - no --print - so the pane stays resident for hand send and hand
// watch across a multi-turn no-mistakes pipeline. Verified via `claude --help`: --model, --effort
// and --dangerously-skip-permissions all apply outside --print.
func buildClaude(o Options) string {
	// --dangerously-skip-permissions, or an unattended worker stalls on a permission prompt.
	// CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=false suppresses the dim predicted-next-prompt ghost text,
	// which a pane-watching supervisor would otherwise read as typed input under an idle worker.
	args := []string{"CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=false", "claude", "--dangerously-skip-permissions"}
	if o.Model != "" {
		args = append(args, "--model", shellQuote(o.Model))
	}
	if o.Effort != "" {
		args = append(args, "--effort", shellQuote(o.Effort))
	}
	args = append(args, shellQuote(briefPrompt(o)))
	return strings.Join(args, " ")
}

// Launches Codex CLI 0.146.0 interactively with its positional prompt. Its help and config schema
// expose the flags below; paste-burst buffering otherwise absorbs hand send's immediate Enter, and
// auto effort means inherit Codex's default rather than pass a literal value.
func buildCodex(o Options) string {
	args := []string{"codex", "--dangerously-bypass-approvals-and-sandbox", "-c", shellQuote("disable_paste_burst=true")}
	if o.Model != "" {
		args = append(args, "--model", shellQuote(o.Model))
	}
	if o.Effort != "" && o.Effort != "auto" {
		args = append(args, "-c", shellQuote(fmt.Sprintf(`model_reasoning_effort="%s"`, o.Effort)))
	}
	args = append(args, shellQuote(briefPrompt(o)))
	return strings.Join(args, " ")
}

func buildGrok(o Options) string {
	return fmt.Sprintf("grok --trust --file %s", shellQuote(o.Brief))
}

func buildPi(o Options) string {
	return fmt.Sprintf("pi %s", shellQuote(o.Brief))
}

// Uses the bare `opencode` command (verified via `opencode --help`), which opens an interactive TUI,
// rather than `opencode run` - that one is explicitly headless and exits after a single reply.
func buildOpenCode(o Options) string {
	// OPENCODE_CONFIG_CONTENT grants blanket tool permission so an unattended worker does not stall
	// on a permission prompt.
	args := []string{"OPENCODE_CONFIG_CONTENT=" + shellQuote(`{"permission":{"*":"allow"}}`), "opencode"}
	if o.Model != "" {
		args = append(args, "--model", shellQuote(o.Model))
	}
	// The bare command has no --file flag and no effort or variant flag, so the brief path rides in
	// the --prompt text and Options.Effort is dropped here rather than passed.
	args = append(args, "--prompt", shellQuote(briefPrompt(o)))
	return strings.Join(args, " ")
}

// Shared so the wording cannot drift between harnesses. It ends with agentsmd.OperatorDecisionRule
// because a worker runs in a worktree that is never under the fleet home, so the home's AGENTS.md
// never reaches it and the launch prompt is the only channel that rule has.
func briefPrompt(o Options) string {
	prompt := fmt.Sprintf("Read the brief at %s and carry out the task it describes.", o.Brief)
	if o.BriefHasFrontMatter {
		prompt += " Any model or effort keys in its leading '---' block are dispatch metadata, not task content."
	}
	return prompt + " " + agentsmd.OperatorDecisionRule
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
