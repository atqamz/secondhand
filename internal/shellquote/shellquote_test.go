package shellquote

import (
	"os/exec"
	"testing"
)

func TestQuoteRoundTripsPOSIXShellArguments(t *testing.T) {
	for _, value := range []string{
		"fleet home",
		"fleet's home",
		"fleet`printf injected`home",
		"fleet;printf injected",
	} {
		t.Run(value, func(t *testing.T) {
			got, err := exec.Command("sh", "-c", "printf %s "+Quote(value)).CombinedOutput()
			if err != nil {
				t.Fatalf("execute quoted argument: %v: %s", err, got)
			}
			if string(got) != value {
				t.Fatalf("shell produced %q, want the one original argument %q", got, value)
			}
		})
	}
}
