//go:build !windows

package selfupdate

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var linkFile = os.Link

type adoptedReplacement struct {
	rollback func() error
	commit   func() error
}

func replaceAdoptedExecutable(target, staged string) (adoptedReplacement, error) {
	if _, err := os.Lstat(target); os.IsNotExist(err) {
		if err := os.Rename(staged, target); err != nil {
			return adoptedReplacement{}, err
		}
		return adoptedReplacement{
			rollback: func() error { return os.Remove(target) },
			commit:   func() error { return nil },
		}, nil
	} else if err != nil {
		return adoptedReplacement{}, err
	}

	backup, err := os.CreateTemp(filepath.Dir(target), ".hand-adopt-backup-*")
	if err != nil {
		return adoptedReplacement{}, fmt.Errorf("create rollback copy: %w", err)
	}
	backupPath := backup.Name()
	if err := backup.Close(); err != nil {
		_ = os.Remove(backupPath)
		return adoptedReplacement{}, fmt.Errorf("close rollback copy: %w", err)
	}
	if err := os.Remove(backupPath); err != nil {
		return adoptedReplacement{}, fmt.Errorf("prepare rollback copy: %w", err)
	}
	if err := linkFile(target, backupPath); err != nil {
		if err := copyAdoptBackup(target, backupPath); err != nil {
			_ = os.Remove(backupPath)
			return adoptedReplacement{}, fmt.Errorf("preserve previous Hand: %w", err)
		}
	}
	if err := os.Rename(staged, target); err != nil {
		_ = os.Remove(backupPath)
		return adoptedReplacement{}, err
	}
	return adoptedReplacement{
		rollback: func() error {
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				return err
			}
			return os.Rename(backupPath, target)
		},
		commit: func() error { return os.Remove(backupPath) },
	}, nil
}

func copyAdoptBackup(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		_ = output.Close()
		if remove {
			_ = os.Remove(destination)
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	if err := output.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	remove = false
	return nil
}
