package state

import "github.com/atqamz/hand/internal/store"

func FleetID(homeDir string) (string, error) {
	db, err := store.Open(homeDir)
	if err != nil {
		return "", err
	}
	defer func() { _ = db.Close() }()
	return db.FleetID()
}

func FleetIDReadOnly(homeDir string) (string, error) {
	return store.FleetIDReadOnly(homeDir)
}

func ValidateInitTarget(homeDir string) error {
	return store.ValidateInitTarget(homeDir)
}
