// Package agentsmd generates and refreshes the AGENTS.md workflow/rules
// template that hand init writes into a fleet home, and checks an existing one
// for perishable content and generated-block drift.
package agentsmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/atqamz/secondhand/internal/atomicfile"
	"github.com/atqamz/secondhand/internal/home"
	"github.com/atqamz/secondhand/internal/shellquote"
)

const (
	filename    = "AGENTS.md"
	symlinkName = "CLAUDE.md"

	beginMarker = "<!-- hand:generated:start -->"
	endMarker   = "<!-- hand:generated:end -->"
)

// OperatorDecisionRule is the one supervisor rule a worker needs wherever it runs, and a
// worktree is never under the fleet home, so it never loads the home's AGENTS.md.
// internal/harness puts this string in the launch prompt and the session contract includes it.
const OperatorDecisionRule = "Only a `hand send` message carries an operator decision. " +
	"Answering your own harness's question dialog is you deciding, not the operator - " +
	"never record that answer as \"operator said\" or \"operator chose\". " +
	"You may decide anything reversible yourself and proceed; say so in first person - " +
	"`working: deciding myself: <the call> because <reason>` - " +
	"and reserve `needs-decision:` for what you cannot take back."

const generatedBody = `## Secondhand supervisor bootstrap

Before responding or acting in a supervising session, run ` + "`hand session start`" + `.
Do not run supervisor bootstrap when ` + "`HAND_ROLE=worker`" + `.
`

var supervisorInstructions = []string{
	"Read `data/operator.md` before anything else. Its constraints outrank your own judgment.",
	"Match the request to a project in `data/projects.md`.",
	"Edit `data/backlog.md` to record the task with a unique ID.",
	"Write a brief at `data/<id>/brief.md`, including the absolute path to `state/<id>.status` and the report vocabulary the worker should append to it.",
	"`hand status <id>` shows a worker's reported state. Workers report with `working:`, `paused:`, `blocked:`, `needs-decision:`, `done:`, or `failed:`.",
	"Run `hand watch --until-event` as a background task to monitor the fleet. It exits on the first fleet event and that exit is what reaches you, so re-arm it every time you act on one.",
	"Never merge without explicit authorization.",
	"Run `hand teardown <id>` after work is landed. Work that is handed off but whose landing is someone else's call is recorded with `hand deliver <id> --reason <text>` first.",
	"Never edit files under `projects/`. Workers do that in worktrees.",
	"Never force-teardown without explicit authorization. `--force` is for work nobody delivered; `hand deliver` is the answer for work that is delivered and not landed.",
	"Name a path in a brief, a status report, or an operator message: full and absolute, never relative.",
	"Waiting on the operator or on another task: `hand hold set <id> --kind operator --reason <text>` or `--kind blocked --reason <text> --blocked-on <id>`, and `hand hold clear <id>` once resolved.",
	"`data/operator.md` is the operator's file, not yours. Read it, never rewrite it: one-way ownership is what lets its constraints outrank your judgment.",
	"`data/backlog.md` is your task queue. Edit it directly.",
	"Roll finished backlog entries into `data/done-archive.md`, and dropped or superseded ones into `data/note-archive.md` with the reason they were dropped. Roll off rather than delete.",
	"`data/learnings.md` holds dated, evidence-backed operational facts. Read it when a task touches something it covers, add to it when a discovery cost real time, and curate it - rewrite and prune rather than append forever.",
	OperatorDecisionRule,
	"Every command prints TOON on stdout: `key: value` lines, `name[N]{f1,f2}:` blocks with one comma-joined row per line, and a `help[N]:` list. A count of `0` and an empty block are an answer, not a failure.",
	"A failure always writes one document to stderr: `error`, `kind`, and `exit`. Branch on `kind` rather than matching the message text. A command that already produced output keeps it on stdout.",
	"Nothing under `data/` is written for the operator to read. Report to them in the session.",
}

// Returns an isolated copy of the durable operating contract.
func SupervisorInstructions() []string {
	return append([]string(nil), supervisorInstructions...)
}

// Refresh writes or refreshes dir/AGENTS.md and its CLAUDE.md symlink, reporting whether
// the template content changed (false, nil when dir is not a fleet home, which is not an
// error). Only the span between the markers is replaced, so user-added content survives.
func Refresh(dir string) (bool, error) {
	isHome, err := home.IsHome(dir)
	if err != nil {
		return false, err
	}
	if !isHome {
		return false, nil
	}

	path := filepath.Join(dir, filename)
	existing, err := os.ReadFile(path)
	var target string
	switch {
	case os.IsNotExist(err):
		existing = nil
		target = generatedBlock()
	case err != nil:
		return false, fmt.Errorf("read %s: %w", filename, err)
	default:
		target, err = mergeGenerated(string(existing))
		if err != nil {
			return false, err
		}
	}

	refreshed := false
	if target != string(existing) {
		if err := atomicfile.Write(path, ".agents.md-", []byte(target), 0o644); err != nil {
			return false, fmt.Errorf("write %s: %w", filename, err)
		}
		refreshed = true
	}

	symlinkPath := filepath.Join(dir, symlinkName)
	if _, err := os.Lstat(symlinkPath); os.IsNotExist(err) {
		if err := os.Symlink(filename, symlinkPath); err != nil {
			return false, fmt.Errorf("create %s symlink: %w", symlinkName, err)
		}
	} else if err != nil {
		return false, fmt.Errorf("check %s: %w", symlinkName, err)
	}

	return refreshed, nil
}

func generatedBlock() string {
	return beginMarker + "\n" + generatedBody + endMarker + "\n"
}

// A valid marked span is replaced, while an unmarked file gets a safely separated append.
// Ambiguous marker ownership is rejected before Refresh performs any write.
func mergeGenerated(content string) (string, error) {
	startCount := strings.Count(content, beginMarker)
	endCount := strings.Count(content, endMarker)
	switch {
	case startCount == 0 && endCount == 0:
		separator := ""
		if content != "" && !strings.HasSuffix(content, "\n") {
			separator = "\n"
		}
		if content != "" {
			separator += "\n"
		}
		return content + separator + generatedBlock(), nil
	case startCount != 1 || endCount != 1:
		return "", fmt.Errorf("AGENTS.md has malformed or duplicate hand:generated markers")
	}
	start, end, ok := generatedBlockSpan(content)
	if !ok {
		return "", fmt.Errorf("AGENTS.md has malformed hand:generated markers")
	}
	return content[:start] + strings.TrimSuffix(generatedBlock(), "\n") + content[end:], nil
}

// The returned byte range includes both markers and exists only for one ordered pair.
func generatedBlockSpan(content string) (start, end int, ok bool) {
	if strings.Count(content, beginMarker) != 1 || strings.Count(content, endMarker) != 1 {
		return 0, 0, false
	}
	start = strings.Index(content, beginMarker)
	if start == -1 {
		return 0, 0, false
	}
	relEnd := strings.Index(content[start:], endMarker)
	if relEnd == -1 {
		return 0, 0, false
	}
	return start, start + relEnd + len(endMarker), true
}

func markerOffsets(content, marker string) []int {
	var offsets []int
	for searchFrom := 0; ; {
		rel := strings.Index(content[searchFrom:], marker)
		if rel == -1 {
			return offsets
		}
		offset := searchFrom + rel
		offsets = append(offsets, offset)
		searchFrom = offset + len(marker)
	}
}

func lineAtOffset(content string, offset int) int {
	return strings.Count(content[:offset], "\n") + 1
}

var (
	// The two shapes of perishable content that belong in the fleet home's own notes
	// rather than AGENTS.md: a dated fact is an incident, and phrasing that names its
	// own expiry is not an invariant. hand does not own that notes convention.
	dateRe         = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}\b`)
	selfExpiringRe = regexp.MustCompile(`(?i)\b(?:until|once)\s+#\d+\s+lands\b|\bawaiting\s+#\d+\b`)

	// Stripped from a line before it is tested above: a date in a quoted example or a
	// URL is not an incident, and flagging it anyway is the false positive that gets a
	// checker ignored (atqamz/secondhand#90).
	inlineCodeRe = regexp.MustCompile("`[^`]*`")
	urlRe        = regexp.MustCompile(`https?://\S+`)
)

// Severity distinguishes a Violation that fails hand doctor from one that is
// informational: real and worth a human's attention, but not something the checker can
// resolve into a pass/fail verdict on its own.
type Severity int

const (
	SeverityViolation Severity = iota
	SeverityInfo
)

// Violation is one perishable-content, malformed-file or generated-block hit Check found,
// at SeverityViolation unless Severity says otherwise. Line is 1-based, or 0 when the hit
// is not about a single line (a drifted or absent generated block).
type Violation struct {
	Line     int
	Text     string
	Severity Severity
}

// Check reports perishable content, malformed fences, and generated-block drift or absence without
// fixing any of it. A nil result with no error means the directory is not a fleet home.
func Check(dir string) ([]Violation, error) {
	isHome, err := home.IsHome(dir)
	if err != nil {
		return nil, err
	}
	if !isHome {
		return nil, nil
	}

	data, err := os.ReadFile(filepath.Join(dir, filename))
	if os.IsNotExist(err) {
		return []Violation{{Text: fmt.Sprintf("AGENTS.md is missing: run hand init %s to restore the current generated block", shellquote.Quote(dir))}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filename, err)
	}
	content := string(data)
	startOffsets := markerOffsets(content, beginMarker)
	endOffsets := markerOffsets(content, endMarker)
	blockStart, blockEnd, hasBlock := generatedBlockSpan(content)

	var violations []Violation
	switch {
	case len(startOffsets) == 0 && len(endOffsets) == 0:
		violations = append(violations, Violation{
			Text: fmt.Sprintf("no hand:generated markers: run hand init %s to append the current generated block", shellquote.Quote(dir)),
		})
	case len(startOffsets) == 0:
		for _, offset := range endOffsets {
			violations = append(violations, Violation{Line: lineAtOffset(content, offset), Text: "unpaired hand:generated end marker"})
		}
	case len(endOffsets) == 0:
		for _, offset := range startOffsets {
			violations = append(violations, Violation{Line: lineAtOffset(content, offset), Text: "unpaired hand:generated start marker"})
		}
	default:
		for _, offset := range startOffsets[1:] {
			violations = append(violations, Violation{Line: lineAtOffset(content, offset), Text: "duplicate hand:generated start marker"})
		}
		for _, offset := range endOffsets[1:] {
			violations = append(violations, Violation{Line: lineAtOffset(content, offset), Text: "duplicate hand:generated end marker"})
		}
		if endOffsets[0] < startOffsets[0] {
			violations = append(violations, Violation{Line: lineAtOffset(content, endOffsets[0]), Text: "hand:generated end marker appears before start marker"})
		}
	}

	inFence := false
	fenceOpenedAt := 0
	offset := 0
	for i, line := range strings.Split(content, "\n") {
		lineNo := i + 1
		insideBlock := hasBlock && offset >= blockStart && offset < blockEnd
		offset += len(line) + 1

		if r, found := firstBannedRune(line); found {
			violations = append(violations, Violation{Line: lineNo, Text: fmt.Sprintf("banned character %q: no em dash or emoji, house rule everywhere in this file", r)})
		}

		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			if inFence {
				fenceOpenedAt = lineNo
			}
			continue
		}
		if inFence || insideBlock {
			continue
		}

		stripped := urlRe.ReplaceAllString(inlineCodeRe.ReplaceAllString(line, ""), "")
		if dateRe.MatchString(stripped) {
			violations = append(violations, Violation{Line: lineNo, Text: "date outside the generated block: a dated fact is an incident, belongs in the home's own notes, not the generated block"})
		}
		if selfExpiringRe.MatchString(stripped) {
			violations = append(violations, Violation{Line: lineNo, Text: "self-expiring phrasing outside the generated block: not an invariant, belongs in the home's own notes, not the generated block"})
		}
	}

	// An unterminated fence silences every date and self-expiring check after
	// it, so it has to be reported rather than left to read as a clean file.
	if inFence {
		violations = append(violations, Violation{Line: fenceOpenedAt, Text: "unterminated code fence: every date and self-expiring check after this line was skipped"})
	}

	if hasBlock && content[blockStart:blockEnd] != strings.TrimSuffix(generatedBlock(), "\n") {
		violations = append(violations, Violation{Text: fmt.Sprintf("generated block has drifted from generatedBody: run hand init %s to refresh", shellquote.Quote(dir))})
	}

	return violations, nil
}

func firstBannedRune(line string) (rune, bool) {
	for _, r := range line {
		if r == '—' || isEmojiRune(r) {
			return r, true
		}
	}
	return 0, false
}

// Covers the Unicode blocks an accidental emoji actually comes from, not a formal emoji
// property table: pictographs, symbols/dingbats, regional-indicator flag letters, and the
// variation-selector/ZWJ modifiers that ride along with them.
func isEmojiRune(r rune) bool {
	switch {
	case r >= 0x1F300 && r <= 0x1FAFF:
		return true
	case r >= 0x2600 && r <= 0x27BF:
		return true
	case r >= 0x2B00 && r <= 0x2BFF:
		return true
	case r >= 0x1F1E6 && r <= 0x1F1FF:
		return true
	case r == 0xFE0F || r == 0x200D:
		return true
	}
	return false
}
