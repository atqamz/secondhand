//go:build !windows

package atomicfile

import "os"

func replace(source, target string) error {
	return os.Rename(source, target)
}
