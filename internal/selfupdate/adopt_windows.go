//go:build windows

package selfupdate

import "path/filepath"

type adoptedReplacement = executableReplacement

func replaceAdoptedExecutable(target, staged string) (adoptedReplacement, error) {
	cleanupStaleBackups(filepath.Dir(target))
	return replaceExecutableWithRollback(target, staged)
}
