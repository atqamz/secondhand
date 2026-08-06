package harness

import (
	"strings"
	"testing"

	"github.com/atqamz/secondhand/internal/agentsmd"
)

// The shell-quoted first message a harness launches with. The operator-decision rule is a paragraph
// of prose owned by internal/agentsmd, so exact-match wants build the prompt from it instead of
// restating it here.
func quotedPrompt(brief string) string {
	return shellQuote("Read the brief at " + brief + " and carry out the task it describes. " + agentsmd.OperatorDecisionRule)
}

func TestBuildUnrecognizedHarness(t *testing.T) {
	if _, err := Build("nonexistent", Options{}); err == nil {
		t.Fatal("expected error for unrecognized harness")
	}
}

func TestIsSupported(t *testing.T) {
	for _, name := range []string{Claude, Codex, Grok, Pi, OpenCode} {
		if !IsSupported(name) {
			t.Errorf("IsSupported(%q) = false, want true", name)
		}
	}
	if IsSupported("nonexistent") {
		t.Error("IsSupported(nonexistent) = true, want false")
	}
}

func TestBuildAlwaysCdsIntoWorktree(t *testing.T) {
	for _, name := range []string{Claude, Codex, Grok, Pi, OpenCode} {
		got, err := Build(name, Options{Worktree: "/tmp/wt", Brief: "/tmp/wt/brief.md"})
		if err != nil {
			t.Fatalf("Build(%q) error: %v", name, err)
		}
		if !strings.HasPrefix(got, "cd '/tmp/wt' && ") {
			t.Errorf("Build(%q) = %q, want cd prefix", name, got)
		}
	}
}

// A worker needs its role and fleet home before every harness-specific setting, so child processes
// can reliably decline supervisor-only commands while still locating the fleet state.
func TestBuildCarriesWorkerRoleAndFleetHome(t *testing.T) {
	for _, name := range Names() {
		got, err := Build(name, Options{
			Worktree:  "/tmp/wt",
			Brief:     "/tmp/brief.md",
			FleetHome: "/tmp/fleet home",
		})
		if err != nil {
			t.Fatalf("Build(%q): %v", name, err)
		}
		want := "HAND_ROLE=worker HAND_HOME='/tmp/fleet home'"
		if !strings.Contains(got, want) {
			t.Fatalf("Build(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestBuildClaude(t *testing.T) {
	got, err := Build(Claude, Options{Worktree: "/tmp/wt", Brief: "/tmp/data/fix-login/brief.md"})
	if err != nil {
		t.Fatal(err)
	}
	want := "cd '/tmp/wt' && CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=false claude --dangerously-skip-permissions " + quotedPrompt("/tmp/data/fix-login/brief.md")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildClaudeWithModelAndEffort(t *testing.T) {
	got, err := Build(Claude, Options{Worktree: "/tmp/wt", Brief: "/tmp/brief.md", Model: "sonnet", Effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	want := "cd '/tmp/wt' && CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION=false claude --dangerously-skip-permissions --model 'sonnet' --effort 'low' " + quotedPrompt("/tmp/brief.md")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Guards against a silent regression to --print, which would strand hand send and hand watch with
// no running pane to steer.
func TestBuildClaudeNeverHeadless(t *testing.T) {
	got, err := Build(Claude, Options{Worktree: "/tmp/wt", Brief: "/tmp/brief.md"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "--print") {
		t.Fatalf("got %q, want no --print (headless) flag", got)
	}
	if !strings.Contains(got, "--dangerously-skip-permissions") {
		t.Fatalf("got %q, want --dangerously-skip-permissions so an unattended worker never stalls on a permission prompt", got)
	}
}

func TestBuildCodex(t *testing.T) {
	got, err := Build(Codex, Options{Worktree: "/tmp/wt", Brief: "/tmp/brief.md"})
	if err != nil {
		t.Fatal(err)
	}
	want := "cd '/tmp/wt' && codex --dangerously-bypass-approvals-and-sandbox -c 'disable_paste_burst=true' " + quotedPrompt("/tmp/brief.md")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildCodexWithModelAndEffort(t *testing.T) {
	got, err := Build(Codex, Options{Worktree: "/tmp/wt", Brief: "/tmp/brief.md", Model: "gpt-5.6-codex", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	want := "cd '/tmp/wt' && codex --dangerously-bypass-approvals-and-sandbox -c 'disable_paste_burst=true' --model 'gpt-5.6-codex' -c 'model_reasoning_effort=\"high\"' " + quotedPrompt("/tmp/brief.md")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildCodexOmitsAutoEffort(t *testing.T) {
	got, err := Build(Codex, Options{Worktree: "/tmp/wt", Brief: "/tmp/brief.md", Effort: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	want := "cd '/tmp/wt' && codex --dangerously-bypass-approvals-and-sandbox -c 'disable_paste_burst=true' " + quotedPrompt("/tmp/brief.md")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if !SupportsEffort(Codex) {
		t.Fatal("SupportsEffort(Codex) = false, want true for explicit non-auto efforts")
	}
}

func TestBuildGrok(t *testing.T) {
	got, err := Build(Grok, Options{Worktree: "/tmp/wt", Brief: "/tmp/brief.md"})
	if err != nil {
		t.Fatal(err)
	}
	want := "cd '/tmp/wt' && grok --trust --file '/tmp/brief.md'"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildPi(t *testing.T) {
	got, err := Build(Pi, Options{Worktree: "/tmp/wt", Brief: "/tmp/brief.md"})
	if err != nil {
		t.Fatal(err)
	}
	want := "cd '/tmp/wt' && pi '/tmp/brief.md'"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildOpenCode(t *testing.T) {
	got, err := Build(OpenCode, Options{Worktree: "/tmp/wt", Brief: "/tmp/brief.md"})
	if err != nil {
		t.Fatal(err)
	}
	want := "cd '/tmp/wt' && OPENCODE_CONFIG_CONTENT='{\"permission\":{\"*\":\"allow\"}}' opencode --prompt " + quotedPrompt("/tmp/brief.md")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildClaudeFrontMatterDisclaimer(t *testing.T) {
	got, err := Build(Claude, Options{Worktree: "/tmp/wt", Brief: "/tmp/brief.md", BriefHasFrontMatter: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "dispatch metadata") {
		t.Fatalf("got %q, want the front matter disclaimed", got)
	}
}

func TestBuildClaudeNoFrontMatterUnchanged(t *testing.T) {
	got, err := Build(Claude, Options{Worktree: "/tmp/wt", Brief: "/tmp/brief.md"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "dispatch metadata") {
		t.Fatalf("got %q, want no disclaimer for a brief with no front matter", got)
	}
}

func TestBuildOpenCodeWithModel(t *testing.T) {
	got, err := Build(OpenCode, Options{Worktree: "/tmp/wt", Brief: "/tmp/brief.md", Model: "opus"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "--model 'opus'") {
		t.Fatalf("got %q, want --model flag", got)
	}
}

// Guards against a silent regression to `opencode run`, which exits after one reply and leaves no
// pane to steer.
func TestBuildOpenCodeNeverHeadless(t *testing.T) {
	got, err := Build(OpenCode, Options{Worktree: "/tmp/wt", Brief: "/tmp/brief.md"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "opencode run") {
		t.Fatalf("got %q, want no headless \"opencode run\" invocation", got)
	}
	if !strings.Contains(got, `OPENCODE_CONFIG_CONTENT`) {
		t.Fatalf("got %q, want OPENCODE_CONFIG_CONTENT so an unattended worker never stalls on a permission prompt", got)
	}
}

func TestBuildOpenCodeFrontMatterDisclaimer(t *testing.T) {
	got, err := Build(OpenCode, Options{Worktree: "/tmp/wt", Brief: "/tmp/brief.md", BriefHasFrontMatter: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "dispatch metadata") {
		t.Fatalf("got %q, want the front matter disclaimed", got)
	}
}

func TestBuildCodexFrontMatterDisclaimer(t *testing.T) {
	got, err := Build(Codex, Options{Worktree: "/tmp/wt", Brief: "/tmp/brief.md", BriefHasFrontMatter: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "dispatch metadata") {
		t.Fatalf("got %q, want the front matter disclaimed", got)
	}
}

// Pins the only channel that rule has: a worker's worktree is never under the fleet home, so the
// AGENTS.md copy of it never reaches the worker.
func TestBuildCarriesOperatorDecisionRule(t *testing.T) {
	quoted := shellQuote(agentsmd.OperatorDecisionRule)
	escaped := quoted[1 : len(quoted)-1]
	for _, name := range []string{Claude, Codex, OpenCode} {
		got, err := Build(name, Options{Worktree: "/tmp/wt", Brief: "/tmp/brief.md"})
		if err != nil {
			t.Fatalf("Build(%q) error: %v", name, err)
		}
		if !strings.Contains(got, escaped) {
			t.Errorf("Build(%q) = %q, want the operator-decision rule %q in the launch prompt", name, got, escaped)
		}
	}
}

// Pinned against the builders rather than restating the map: a harness reporting true while its
// command carries no --model is exactly the silent drop being fixed here.
func TestSupportsModel(t *testing.T) {
	for _, name := range []string{Claude, Codex, Grok, Pi, OpenCode} {
		got, err := Build(name, Options{Worktree: "/tmp/wt", Brief: "/tmp/brief.md", Model: "some-model"})
		if err != nil {
			t.Fatalf("Build(%q) error: %v", name, err)
		}
		if emits := strings.Contains(got, "--model 'some-model'"); emits != SupportsModel(name) {
			t.Errorf("SupportsModel(%q) = %v but Build(%q) = %q", name, SupportsModel(name), name, got)
		}
	}
	if SupportsModel("nonexistent") {
		t.Error("SupportsModel(nonexistent) = true, want false")
	}
}

// Pinned against the builders for the same reason as TestSupportsModel: a harness reporting true
// while its command carries no --effort is the silent drop this predicate exists to prevent.
func TestSupportsEffort(t *testing.T) {
	for _, name := range []string{Claude, Codex, Grok, Pi, OpenCode} {
		got, err := Build(name, Options{Worktree: "/tmp/wt", Brief: "/tmp/brief.md", Effort: "some-effort"})
		if err != nil {
			t.Fatalf("Build(%q) error: %v", name, err)
		}
		emits := strings.Contains(got, "--effort 'some-effort'") || strings.Contains(got, `model_reasoning_effort="some-effort"`)
		if emits != SupportsEffort(name) {
			t.Errorf("SupportsEffort(%q) = %v but Build(%q) = %q", name, SupportsEffort(name), name, got)
		}
	}
	if SupportsEffort("nonexistent") {
		t.Error("SupportsEffort(nonexistent) = true, want false")
	}
}

// Pinned against the builders for the same reason as TestSupportsModel: a harness reporting true
// while its command carries no prompt text drops the operator-decision rule in silence.
func TestCarriesPrompt(t *testing.T) {
	quoted := shellQuote(agentsmd.OperatorDecisionRule)
	escaped := quoted[1 : len(quoted)-1]
	for _, name := range []string{Claude, Codex, Grok, Pi, OpenCode} {
		got, err := Build(name, Options{Worktree: "/tmp/wt", Brief: "/tmp/brief.md"})
		if err != nil {
			t.Fatalf("Build(%q) error: %v", name, err)
		}
		if carries := strings.Contains(got, escaped); carries != CarriesPrompt(name) {
			t.Errorf("CarriesPrompt(%q) = %v but Build(%q) = %q", name, CarriesPrompt(name), name, got)
		}
	}
	if CarriesPrompt("nonexistent") {
		t.Error("CarriesPrompt(nonexistent) = true, want false")
	}
}

func TestShellQuoteEscapesSingleQuotes(t *testing.T) {
	got, err := Build(Pi, Options{Worktree: "/tmp/wt", Brief: "/tmp/it's/brief.md"})
	if err != nil {
		t.Fatal(err)
	}
	want := `cd '/tmp/wt' && pi '/tmp/it'\''s/brief.md'`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Pins the shape confirmLaunch depends on: a startup signature for each frame claude settles into,
// answerable dialogs carrying keys, and the managed-settings dialog catalogued as
// recognized-but-refused so it fails fast instead of looking uncatalogued.
func TestFirstRunPromptsClaude(t *testing.T) {
	prompts := FirstRunPromptsFor(Claude)
	if prompts.Ready == nil || prompts.Unrecognized == nil {
		t.Fatalf("got %+v, want readiness and unrecognized signatures", prompts)
	}
	for _, frame := range []string{"Welcome to Claude Code", "? for shortcuts", "bypass permissions on (shift+tab to cycle)"} {
		if !prompts.Ready.MatchString(frame) {
			t.Errorf("readiness signature does not match claude startup frame %q", frame)
		}
	}
	if prompts.Ready.MatchString("cd '/tmp/wt' && claude --dangerously-skip-permissions 'Read the brief'") {
		t.Fatal("readiness signature matches the echoed launch command, so a pane that never started reads as ready")
	}

	byName := map[string]FirstRunPrompt{}
	for _, prompt := range prompts.Known {
		if (len(prompt.Keys) == 0) == (prompt.Refuse == "") {
			t.Fatalf("prompt %q must set exactly one of Keys and Refuse, got %+v", prompt.Name, prompt)
		}
		byName[prompt.Name] = prompt
	}

	bypass := byName["bypass permissions"]
	if !bypass.Match.MatchString("WARNING: Bypass Permissions mode") {
		t.Fatal("bypass permissions signature does not match the dialog")
	}
	if strings.Join(bypass.Keys, ",") != "Down,Enter" {
		t.Fatalf("bypass permissions keys = %v, want Down before Enter (a bare Enter lands on \"No, exit\" and quits claude)", bypass.Keys)
	}
	if got := byName["workspace trust"]; strings.Join(got.Keys, ",") != "Enter" {
		t.Fatalf("workspace trust keys = %v, want Enter", got.Keys)
	}
	managed := byName["managed settings"]
	if managed.Refuse == "" {
		t.Fatal("managed settings must be refused, not answered on the operator's behalf")
	}
	if !managed.Match.MatchString("Managed settings require approval") || !managed.Match.MatchString("Yes, I trust these settings") {
		t.Fatal("managed settings signature does not match the dialog")
	}
}

// Pins the harnesses actually run in a real pane and observed being labeled by herdr; the rest must
// stay false until each is exercised the same way.
func TestAgentDetectionVerified(t *testing.T) {
	for _, name := range []string{Claude, Codex, OpenCode} {
		if !AgentDetectionVerified(name) {
			t.Errorf("AgentDetectionVerified(%q) = false, want true", name)
		}
	}
	for _, name := range []string{Grok, Pi, "nonexistent"} {
		if AgentDetectionVerified(name) {
			t.Errorf("AgentDetectionVerified(%q) = true, want false until herdr detection is exercised against it", name)
		}
	}
}

func TestFirstRunPromptsWithoutVerifiedSignatures(t *testing.T) {
	for _, name := range []string{Grok, Pi, OpenCode, "nonexistent"} {
		if got := FirstRunPromptsFor(name); got.Ready != nil || got.Known != nil || got.Unrecognized != nil {
			t.Errorf("FirstRunPromptsFor(%q) = %+v, want no unverified signatures", name, got)
		}
	}
}
