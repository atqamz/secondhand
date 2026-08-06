// Package shellquote renders shell arguments for commands shown to users or sent to a POSIX shell.
package shellquote

import "strings"

// Single quotes keep every byte literal; embedded quotes briefly leave and re-enter that context.
func Quote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
