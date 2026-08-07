package sessionhook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func mkHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state", "hand.db"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readSettings(t *testing.T, dir string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, settingsDir, settingsFile))
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatalf("settings %q: %v", raw, err)
	}
	return settings
}

func hookCommands(t *testing.T, settings map[string]any) []string {
	t.Helper()
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("settings = %+v, want a hooks object", settings)
	}
	matchers, ok := hooks[event].([]any)
	if !ok {
		t.Fatalf("hooks = %+v, want a %s array", hooks, event)
	}
	var lines []string
	for _, matcher := range matchers {
		for _, hook := range matcher.(map[string]any)["hooks"].([]any) {
			command := hook.(map[string]any)["command"]
			if command != nil {
				lines = append(lines, command.(string))
			}
		}
	}
	return lines
}

func TestRemoveDeletesOnlyOwnedHandHook(t *testing.T) {
	dir := mkHome(t)
	writeSettings(t, dir, map[string]any{"hooks": map[string]any{
		event: []any{
			map[string]any{"matcher": "startup", "hooks": []any{
				map[string]any{"type": "command", "command": "/old/path/hand"},
				map[string]any{"type": "command", "command": "/usr/bin/custom"},
			}},
		},
	}})

	changed, err := Remove(dir, "/new/path/hand")
	if err != nil {
		t.Fatal(err)
	}
	if !changed || !reflect.DeepEqual(hookCommands(t, readSettings(t, dir)), []string{"/usr/bin/custom"}) {
		t.Fatal("settings did not preserve only the unrelated hook")
	}
}

func TestRemovePreservesNonCommandHooksWithAHandCommandField(t *testing.T) {
	dir := mkHome(t)
	prompt := map[string]any{"type": "prompt", "command": "hand session start", "prompt": "summarize"}
	writeSettings(t, dir, map[string]any{"hooks": map[string]any{event: []any{map[string]any{"hooks": []any{
		prompt,
		map[string]any{"type": "command", "command": "/old/path/hand"},
	}}}}})

	changed, err := Remove(dir, "/new/path/hand")
	if err != nil || !changed {
		t.Fatalf("Remove = %v, %v, want only the command hook removed", changed, err)
	}
	events := readSettings(t, dir)["hooks"].(map[string]any)
	matchers, ok := events[event].([]any)
	if !ok || len(matchers) != 1 {
		t.Fatalf("hooks = %+v, want the matcher containing the prompt hook preserved", events)
	}
	hooks := matchers[0].(map[string]any)["hooks"].([]any)
	if len(hooks) != 1 || !reflect.DeepEqual(hooks[0], prompt) {
		t.Fatalf("hooks = %+v, want the prompt hook preserved", hooks)
	}
}

func TestRemoveLeavesAMissingSettingsFileAlone(t *testing.T) {
	dir := mkHome(t)
	changed, err := Remove(dir, "/opt/bin/hand")
	if err != nil || changed {
		t.Fatalf("Remove = %v, %v, want no change", changed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, settingsDir)); !os.IsNotExist(err) {
		t.Fatalf("stat %s = %v, want no settings directory created", settingsDir, err)
	}
}

func TestRemoveLeavesSettingsWithoutAnOwnedHookByteForByte(t *testing.T) {
	dir := mkHome(t)
	path := filepath.Join(dir, settingsDir, settingsFile)
	body := []byte("{\n  \"permissions\": {},\n  \"hooks\": {\"SessionStart\": [{\"hooks\": [{\"type\": \"command\", \"command\": \"/usr/bin/custom\"}]}], \"PreToolUse\": []}\n}\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}

	changed, err := Remove(dir, "/opt/bin/hand")
	if err != nil || changed {
		t.Fatalf("Remove = %v, %v, want no change", changed, err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) || !info.ModTime().Equal(stamp) {
		t.Fatalf("settings changed on a no-op removal: contents %q, mtime %v", got, info.ModTime())
	}
}

func TestRemovePreservesLargeUnrelatedJSONNumbersExactly(t *testing.T) {
	dir := mkHome(t)
	path := filepath.Join(dir, settingsDir, settingsFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"permissions":{"limit":9007199254740993},"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"hand session start"},{"type":"command","command":"/usr/bin/custom"}]}]}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := Remove(dir, "/opt/bin/hand")
	if err != nil || !changed {
		t.Fatalf("Remove = %v, %v, want the owned hook removed", changed, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"limit": 9007199254740993`) {
		t.Fatalf("settings = %s, want the unrelated large integer preserved exactly", raw)
	}
	if strings.Contains(string(raw), "hand session start") {
		t.Fatalf("settings = %s, want the owned hook removed", raw)
	}
}

func TestRemoveDeletesOwnedOnlyMatcherAndPreservesUnrelatedMatchers(t *testing.T) {
	dir := mkHome(t)
	empty := map[string]any{"matcher": "already-empty", "hooks": []any{}}
	custom := map[string]any{"matcher": "custom", "hooks": []any{
		map[string]any{"type": "prompt", "prompt": "summarize"},
		map[string]any{"type": "command", "command": "/usr/bin/custom"},
	}}
	writeSettings(t, dir, map[string]any{
		"permissions": map[string]any{},
		"hooks": map[string]any{
			event: []any{
				map[string]any{"matcher": "owned", "hooks": []any{map[string]any{"type": "command", "command": "hand session start"}}},
				empty,
				custom,
			},
			"PreToolUse": []any{},
		},
	})

	changed, err := Remove(dir, "/new/path/not-called-hand")
	if err != nil || !changed {
		t.Fatalf("Remove = %v, %v, want owned matcher removal", changed, err)
	}
	settings := readSettings(t, dir)
	hooks := settings["hooks"].(map[string]any)
	matchers := hooks[event].([]any)
	if len(matchers) != 2 || matchers[0].(map[string]any)["matcher"] != "already-empty" || matchers[1].(map[string]any)["matcher"] != "custom" {
		t.Fatalf("matchers = %+v, want the unrelated empty and custom matchers", matchers)
	}
	if _, ok := hooks["PreToolUse"]; !ok {
		t.Fatalf("hooks = %+v, want unrelated empty event preserved", hooks)
	}
	if _, ok := settings["permissions"]; !ok {
		t.Fatalf("settings = %+v, want unrelated empty permissions preserved", settings)
	}
}

func TestRemoveDeletesSessionStartOnlyWhenNoMatchersRemain(t *testing.T) {
	dir := mkHome(t)
	writeSettings(t, dir, map[string]any{"hooks": map[string]any{
		event:        []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "/old/path/hand --version"}}}},
		"PreToolUse": []any{},
	}})

	changed, err := Remove(dir, "/current/path/hand")
	if err != nil || !changed {
		t.Fatalf("Remove = %v, %v, want the event removed", changed, err)
	}
	hooks := readSettings(t, dir)["hooks"].(map[string]any)
	if _, ok := hooks[event]; ok {
		t.Fatalf("hooks = %+v, want empty %s removed", hooks, event)
	}
	if _, ok := hooks["PreToolUse"]; !ok {
		t.Fatalf("hooks = %+v, want unrelated empty event preserved", hooks)
	}
}

func TestRemoveRecognizesMovedHandAndCurrentExecutableNames(t *testing.T) {
	dir := mkHome(t)
	writeSettings(t, dir, map[string]any{"hooks": map[string]any{event: []any{map[string]any{"hooks": []any{
		map[string]any{"type": "command", "command": "  /old/path/hand status"},
		map[string]any{"type": "command", "command": "/tmp/hand.test session start"},
		map[string]any{"type": "command", "command": "/somewhere/hand.test session start"},
	}}}}})

	changed, err := Remove(dir, "/tmp/hand.test")
	if err != nil || !changed {
		t.Fatalf("Remove = %v, %v, want owned commands removed", changed, err)
	}
	if got := hookCommands(t, readSettings(t, dir)); !reflect.DeepEqual(got, []string{"/somewhere/hand.test session start"}) {
		t.Fatalf("commands = %v, want only the unrelated binary name", got)
	}
}

func TestRemoveIsIdempotent(t *testing.T) {
	dir := mkHome(t)
	writeSettings(t, dir, map[string]any{"hooks": map[string]any{event: []any{map[string]any{"hooks": []any{
		map[string]any{"type": "command", "command": "/old/path/hand"},
		map[string]any{"type": "command", "command": "/usr/bin/custom"},
	}}}}})
	if changed, err := Remove(dir, "/opt/bin/hand"); err != nil || !changed {
		t.Fatalf("first Remove = %v, %v, want a change", changed, err)
	}
	path := filepath.Join(dir, settingsDir, settingsFile)
	stamp := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	changed, err := Remove(dir, "/opt/bin/hand")
	if err != nil || changed {
		t.Fatalf("second Remove = %v, %v, want no change", changed, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || !info.ModTime().Equal(stamp) {
		t.Fatalf("settings changed on an idempotent removal: before %q, after %q, mtime %v", before, after, info.ModTime())
	}
}

func TestRemoveRefusesToOverwriteUnparseableSettings(t *testing.T) {
	dir := mkHome(t)
	if err := os.MkdirAll(filepath.Join(dir, settingsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, settingsDir, settingsFile)
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Remove(dir, "/opt/bin/hand"); err == nil {
		t.Fatal("Remove = nil, want an error naming the unparseable file")
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "{not json" {
		t.Fatalf("settings = %q, %v, want the file untouched", raw, err)
	}
}

func TestRemoveRefusesSettingsOfAnUnexpectedShape(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"settings is not an object", `null`},
		{"hooks is not an object", `{"hooks": "SessionStart"}`},
		{"the event is not an array", `{"hooks": {"SessionStart": {"command": "/usr/bin/tea"}}}`},
		{"a matcher is not an object", `{"hooks": {"SessionStart": ["startup"]}}`},
		{"matcher hooks is not an array", `{"hooks": {"SessionStart": [{"hooks": {"command": "hand"}}]}}`},
		{"a nested hook is not an object", `{"hooks": {"SessionStart": [{"hooks": ["hand"]}]}}`},
		{"a hook type is not a string", `{"hooks": {"SessionStart": [{"hooks": [{"type": 1, "command": "hand"}]}]}}`},
		{"a command is not a string", `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": 1}]}]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := mkHome(t)
			if err := os.MkdirAll(filepath.Join(dir, settingsDir), 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, settingsDir, settingsFile)
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}

			changed, err := Remove(dir, "/opt/bin/hand")
			if err == nil {
				t.Fatal("Remove = nil, want an error naming the key at fault")
			}
			if changed {
				t.Fatal("Remove reported a change it refused to make")
			}
			if !strings.Contains(err.Error(), relPath()) {
				t.Fatalf("err = %v, want it to name %s", err, relPath())
			}
			raw, err := os.ReadFile(path)
			if err != nil || string(raw) != tc.body {
				t.Fatalf("settings = %q, %v, want the file untouched", raw, err)
			}
		})
	}
}

func writeSettings(t *testing.T, dir string, settings map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, settingsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, settingsDir, settingsFile), encoded, 0o644); err != nil {
		t.Fatal(err)
	}
}
