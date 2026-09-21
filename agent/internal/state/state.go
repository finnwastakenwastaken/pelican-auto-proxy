// Package state persists the last applied rule set so a restart or reboot
// restores the forwards without waiting for the plugin's next sync.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/finnwastakenwastaken/pelican-auto-proxy/agent/internal/rules"
)

// FileName is the state file inside the state directory.
const FileName = "rules.json"

// Snapshot is what we persist.
type Snapshot struct {
	Rules     []rules.Rule `json:"rules"`
	AppliedAt time.Time    `json:"applied_at"`
}

// Store reads and writes the snapshot atomically.
type Store struct {
	Dir string
}

func (s Store) path() string { return filepath.Join(s.Dir, FileName) }

// Load returns the stored snapshot. It returns (nil, nil) when nothing has
// been stored yet, which is the normal state on a fresh VPS.
func (s Store) Load() (*Snapshot, error) {
	b, err := os.ReadFile(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var snap Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path(), err)
	}
	return &snap, nil
}

// Save writes the snapshot atomically (see WriteFileAtomic).
func (s Store) Save(snap Snapshot) error {
	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return WriteFileAtomic(s.path(), b, 0o640)
}
