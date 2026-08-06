// Package sessionhook retires Secondhand-owned Claude Code SessionStart hooks.
package sessionhook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/atqamz/secondhand/internal/atomicfile"
)

const (
	settingsDir  = ".claude"
	settingsFile = "settings.json"
	event        = "SessionStart"
	toolName     = "hand"
)

// Removes owned hooks from dir/.claude/settings.json and reports whether the
// file changed. A missing settings file is already retired.
func Remove(dir, exe string) (bool, error) {
	path := filepath.Join(dir, settingsDir, settingsFile)
	existing, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", relPath(), err)
	}

	var settings map[string]any
	decoder := json.NewDecoder(bytes.NewReader(existing))
	decoder.UseNumber()
	if err := decoder.Decode(&settings); err != nil {
		return false, fmt.Errorf("parse %s: %w", relPath(), err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return false, fmt.Errorf("parse %s: %w", relPath(), err)
	}
	if settings == nil {
		return false, fmt.Errorf("%s: settings is not an object, refusing to overwrite it", relPath())
	}
	changed, err := remove(settings, exe)
	if err != nil || !changed {
		return false, err
	}

	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return false, err
	}
	encoded = append(encoded, '\n')
	if err := atomicfile.Write(path, ".settings.json-", encoded, 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", relPath(), err)
	}
	return true, nil
}

func relPath() string {
	return filepath.Join(settingsDir, settingsFile)
}

// Edits settings in place while carrying every unrelated matcher, hook, and
// setting through untouched.
func remove(settings map[string]any, exe string) (bool, error) {
	hooks, err := object(settings, "hooks", "hooks")
	if err != nil {
		return false, err
	}
	matchers, err := array(hooks, event, "hooks."+event)
	if err != nil {
		return false, err
	}

	filteredMatchers := make([]any, 0, len(matchers))
	changed := false
	for i, matcher := range matchers {
		entry, ok := matcher.(map[string]any)
		if !ok {
			return false, fmt.Errorf("%s: hooks.%s[%d] is not an object, refusing to overwrite it", relPath(), event, i)
		}
		commands, err := array(entry, "hooks", fmt.Sprintf("hooks.%s[%d].hooks", event, i))
		if err != nil {
			return false, err
		}
		filteredCommands := make([]any, 0, len(commands))
		matcherChanged := false
		for j, hook := range commands {
			command, ok := hook.(map[string]any)
			if !ok {
				return false, fmt.Errorf("%s: hooks.%s[%d].hooks[%d] is not an object, refusing to overwrite it", relPath(), event, i, j)
			}
			rawType, exists := command["type"]
			if !exists {
				filteredCommands = append(filteredCommands, hook)
				continue
			}
			hookType, ok := rawType.(string)
			if !ok {
				return false, fmt.Errorf("%s: hooks.%s[%d].hooks[%d].type is not a string, refusing to overwrite it", relPath(), event, i, j)
			}
			if hookType != "command" {
				filteredCommands = append(filteredCommands, hook)
				continue
			}
			raw, exists := command["command"]
			if !exists {
				filteredCommands = append(filteredCommands, hook)
				continue
			}
			line, ok := raw.(string)
			if !ok {
				return false, fmt.Errorf("%s: hooks.%s[%d].hooks[%d].command is not a string, refusing to overwrite it", relPath(), event, i, j)
			}
			if _, owned := handArgs(line, exe); owned {
				matcherChanged = true
				changed = true
				continue
			}
			filteredCommands = append(filteredCommands, hook)
		}
		if !matcherChanged {
			filteredMatchers = append(filteredMatchers, matcher)
			continue
		}
		if len(filteredCommands) > 0 {
			entry["hooks"] = filteredCommands
			filteredMatchers = append(filteredMatchers, matcher)
		}
	}
	if !changed {
		return false, nil
	}
	if len(filteredMatchers) == 0 {
		delete(hooks, event)
	} else {
		hooks[event] = filteredMatchers
	}
	return true, nil
}

// A key whose value is not the shape hand merges into is the operator's, the
// same way an unparseable file is: it cannot be carried through, and writing
// over it destroys what is there. An absent or null key is neither.
func object(m map[string]any, key, path string) (map[string]any, error) {
	raw, ok := m[key]
	if !ok || raw == nil {
		return map[string]any{}, nil
	}
	value, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s: %s is not an object, refusing to overwrite it", relPath(), path)
	}
	return value, nil
}

func array(m map[string]any, key, path string) ([]any, error) {
	raw, ok := m[key]
	if !ok || raw == nil {
		return nil, nil
	}
	value, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s: %s is not an array, refusing to overwrite it", relPath(), path)
	}
	return value, nil
}

// Splits a hook command into whatever follows the binary it runs. An entry is
// ours when it names this binary or any binary called hand.
func handArgs(line, exe string) (string, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	first := trimmed
	if i := strings.IndexAny(trimmed, " \t"); i >= 0 {
		first = trimmed[:i]
	}
	if first == "" || (first != exe && filepath.Base(first) != toolName) {
		return "", false
	}
	return strings.TrimPrefix(trimmed, first), true
}
