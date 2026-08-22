//go:build windows

package selfupdate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	updaterBackupPrefix = ".hand-update-"
	updaterBackupSuffix = ".old.exe"
)

var renameFile = os.Rename

func replaceExecutable(execPath, stagedPath string) error {
	execDir := filepath.Dir(execPath)
	cleanupStaleBackups(execDir)
	_, err := replaceExecutableWithRollback(execPath, stagedPath)
	return err
}

type executableReplacement struct {
	rollback func() error
	commit   func() error
}

func replaceExecutableWithRollback(execPath, stagedPath string) (executableReplacement, error) {
	if _, err := os.Lstat(execPath); os.IsNotExist(err) {
		if err := renameFile(stagedPath, execPath); err != nil {
			return executableReplacement{}, err
		}
		return executableReplacement{
			rollback: func() error { return os.Remove(execPath) },
			commit:   func() error { return nil },
		}, nil
	} else if err != nil {
		return executableReplacement{}, err
	}

	backupPath, err := newBackupPath(filepath.Dir(execPath))
	if err != nil {
		return executableReplacement{}, fmt.Errorf("choose backup path for %s: %w", execPath, err)
	}
	if err := renameFile(execPath, backupPath); err != nil {
		return executableReplacement{}, fmt.Errorf("rename canonical executable %s to backup %s: %w", execPath, backupPath, err)
	}

	if err := renameFile(stagedPath, execPath); err != nil {
		rollbackErr := renameFile(backupPath, execPath)
		if rollbackErr != nil {
			return executableReplacement{}, fmt.Errorf("install staged executable %s at canonical path %s failed: %v; rollback %s to %s failed: %v; manual recovery: restore %s to %s after the running process exits, then inspect staged path %s", stagedPath, execPath, err, backupPath, execPath, rollbackErr, backupPath, execPath, stagedPath)
		}
		return executableReplacement{}, fmt.Errorf("install staged executable %s at canonical path %s: %w", stagedPath, execPath, err)
	}
	return executableReplacement{
		rollback: func() error {
			if err := os.Remove(execPath); err != nil && !os.IsNotExist(err) {
				return err
			}
			return renameFile(backupPath, execPath)
		},
		commit: func() error { return nil },
	}, nil
}

func newBackupPath(dir string) (string, error) {
	file, err := os.CreateTemp(dir, updaterBackupPrefix+"*.old.exe")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return path, nil
}

func cleanupStaleBackups(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if isUpdaterBackup(entry.Name()) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

func isUpdaterBackup(name string) bool {
	return strings.HasPrefix(name, updaterBackupPrefix) && strings.HasSuffix(name, updaterBackupSuffix) && len(name) > len(updaterBackupPrefix)+len(updaterBackupSuffix)
}
