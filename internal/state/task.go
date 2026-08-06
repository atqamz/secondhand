package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/atqamz/secondhand/internal/store"
)

// ErrTaskNotFound is wrapped into errors returned by Read and Delete when no
// task row exists for the given ID, rendering as `task "<id>" not found`.
var ErrTaskNotFound = store.ErrTaskNotFound

// ErrTaskActive is wrapped into errors returned by Claim when the task is
// already claimed by another running command, rendering as
// `task "<id>" already active`.
var ErrTaskActive = errors.New("already active")

// ErrLockBusy is returned by TryLock when another process holds the lock.
var ErrLockBusy = errors.New("lock held by another process")

func Dir(homeDir string) string {
	return store.Dir(homeDir)
}

func ValidateID(id string) error {
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id {
		return fmt.Errorf("invalid task ID %q", id)
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return fmt.Errorf("invalid task ID %q", id)
	}
	return nil
}

func Claim(homeDir, id string) (func(), error) {
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	release, err := store.Lock(homeDir, "task:"+id, true)
	if err != nil {
		if err == syscall.EWOULDBLOCK {
			return nil, fmt.Errorf("task %q %w", id, ErrTaskActive)
		}
		return nil, fmt.Errorf("lock task: %w", err)
	}
	active, err := Exists(homeDir, id)
	if err != nil {
		release()
		return nil, err
	}
	if active {
		release()
		return nil, fmt.Errorf("task %q %w", id, ErrTaskActive)
	}
	return release, nil
}

func Lock(homeDir, name string) (func(), error) {
	return store.Lock(homeDir, name, false)
}

// TryLock is Lock for callers that must never wait - a poll loop, or anything
// holding no claim of its own on the work the lock protects. It reports
// ErrLockBusy instead of blocking behind a holder that may be mid-network-call.
func TryLock(homeDir, name string) (func(), error) {
	release, err := store.Lock(homeDir, name, true)
	if err == syscall.EWOULDBLOCK {
		return nil, ErrLockBusy
	}
	return release, err
}

func Exists(homeDir, id string) (bool, error) {
	if err := ValidateID(id); err != nil {
		return false, err
	}
	db, err := store.Open(homeDir)
	if err != nil {
		return false, err
	}
	defer func() { _ = db.Close() }()
	return db.TaskExists(id)
}

func Read(homeDir, id string) (Task, error) {
	if err := ValidateID(id); err != nil {
		return Task{}, err
	}
	db, err := store.Open(homeDir)
	if err != nil {
		return Task{}, err
	}
	defer func() { _ = db.Close() }()

	t, ok, err := db.ReadTask(id)
	if err != nil {
		return Task{}, err
	}
	if !ok {
		return Task{}, fmt.Errorf("task %q %w", id, ErrTaskNotFound)
	}
	return t, nil
}

func Write(homeDir string, t Task) error {
	if err := ValidateID(t.ID); err != nil {
		return err
	}
	db, err := store.Open(homeDir)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	return db.WriteTask(t)
}

func List(homeDir string) ([]Task, error) {
	db, err := store.Open(homeDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	return db.ListTasks()
}

func ListReadOnly(homeDir string) ([]Task, error) {
	db, err := store.OpenReadOnly(homeDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	return db.ListTasks()
}

// Delete removes a task's row along with its report channel at state/<id>.status, leaving
// the durable deliverables in data/<id>/. That file is the volatile wake log: a respawn
// under a used ID starts at report_offset 0, so a surviving log replays as new lines.
func Delete(homeDir, id string) error {
	if err := ValidateID(id); err != nil {
		return err
	}
	// A replayed log re-raises resolved decisions, absorbs a genuine unexplained stop, and
	// auto-records a PR URL out of an old done line onto a task nobody recorded it for.
	if err := os.Remove(ReportPath(homeDir, id)); err != nil && !os.IsNotExist(err) {
		// Failing here leaves nothing durable gone yet, so the whole command is retryable.
		// Removing the row first would strand the caller with the state gone and no way to
		// retry (see cmd/teardown.go's guarded path).
		return fmt.Errorf("remove report channel %q: %w", id, err)
	}
	db, err := store.Open(homeDir)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	return db.DeleteTask(id)
}
