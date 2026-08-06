package harness

import (
	"fmt"
	"testing"
)

func TestDetectPrefersOverride(t *testing.T) {
	got, err := detectCurrent(Claude, []processInfo{{name: ".codex-wrapped"}}, map[string]string{"CODEX_THREAD_ID": "thread"})
	if err != nil {
		t.Fatal(err)
	}
	if want := (Detection{Name: Claude, Source: "override"}); got != want {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestDetectRejectsUnsupportedOverride(t *testing.T) {
	if _, err := detectCurrent("unsupported", nil, nil); err == nil {
		t.Fatal("detectCurrent accepted an unsupported override")
	}
}

func TestDetectPrefersNearestProcessOverInheritedMarker(t *testing.T) {
	got, err := detectCurrent("", []processInfo{
		{name: ".codex-wrapped", args: "codex"},
		{name: "bash", args: "bash"},
	}, map[string]string{"CLAUDECODE": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if want := (Detection{Name: Codex, Source: "process"}); got != want {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestDetectPrefersNearestMatchingProcess(t *testing.T) {
	got, err := detectCurrent("", []processInfo{{name: "claude"}, {name: ".codex-wrapped"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Detection{Name: Claude, Source: "process"}); got != want {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestDetectMatchesHarnessProcesses(t *testing.T) {
	tests := []struct {
		name string
		info processInfo
		want string
	}{
		{name: "claude", info: processInfo{name: "claude"}, want: Claude},
		{name: "codex wrapper", info: processInfo{name: ".codex-wrapped"}, want: Codex},
		{name: "opencode", info: processInfo{name: "opencode"}, want: OpenCode},
		{name: "grok", info: processInfo{name: "grok"}, want: Grok},
		{name: "pi", info: processInfo{name: "pi"}, want: Pi},
		{name: "pi signed", info: processInfo{name: "pi-signed"}, want: Pi},
		{name: "node opencode", info: processInfo{name: "node", args: "node /path/to/opencode"}, want: OpenCode},
		{name: "python opencode", info: processInfo{name: "python", args: "python /path/to/opencode"}, want: OpenCode},
		{name: "python3 opencode", info: processInfo{name: "python3", args: "python3 /path/to/opencode"}, want: OpenCode},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := detectCurrent("", []processInfo{test.info}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if want := (Detection{Name: test.want, Source: "process"}); got != want {
				t.Fatalf("got %#v, want %#v", got, want)
			}
		})
	}
}

func TestDetectDoesNotInspectNonInterpreterArguments(t *testing.T) {
	got, err := detectCurrent("", []processInfo{{name: "bash", args: "bash /path/to/opencode"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Detection{Source: "unknown"}); got != want {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestDetectUsesVerifiedMarkerWhenProcessIsUnknown(t *testing.T) {
	got, err := detectCurrent("", []processInfo{{name: "bash"}}, map[string]string{"PI_CODING_AGENT": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if want := (Detection{Name: Pi, Source: "environment"}); got != want {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestDetectUsesKnownEnvironmentMarkers(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "claude", env: map[string]string{"CLAUDECODE": "1"}, want: Claude},
		{name: "codex", env: map[string]string{"CODEX_THREAD_ID": "thread"}, want: Codex},
		{name: "pi", env: map[string]string{"PI_CODING_AGENT": "true"}, want: Pi},
		{name: "grok", env: map[string]string{"GROK_AGENT": "true"}, want: Grok},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := detectCurrent("", nil, test.env)
			if err != nil {
				t.Fatal(err)
			}
			if want := (Detection{Name: test.want, Source: "environment"}); got != want {
				t.Fatalf("got %#v, want %#v", got, want)
			}
		})
	}
}

func TestDetectCanBeForcedUnknown(t *testing.T) {
	got, err := detectCurrent("unknown", []processInfo{{name: "claude"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Detection{Source: "override"}); got != want {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestDetectReturnsUnknownWithoutSignals(t *testing.T) {
	got, err := detectCurrent("", []processInfo{{name: "bash"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Detection{Source: "unknown"}); got != want {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestCurrentProcessAncestryStopsAtDepth(t *testing.T) {
	previousLookup := processLookup
	t.Cleanup(func() { processLookup = previousLookup })

	calls := 0
	processLookup = func(pid int) ([]byte, error) {
		calls++
		return []byte(fmt.Sprintf("%d process-%d process-%d\n", pid+1, pid, pid)), nil
	}

	got := currentProcessAncestry(1, maxAncestorDepth)
	if len(got) != maxAncestorDepth {
		t.Fatalf("got %d ancestors, want %d", len(got), maxAncestorDepth)
	}
	if calls != maxAncestorDepth {
		t.Fatalf("got %d lookups, want %d", calls, maxAncestorDepth)
	}
}
