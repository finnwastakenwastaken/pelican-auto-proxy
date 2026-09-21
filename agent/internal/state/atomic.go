package state

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileAtomic writes b to path through a temporary file in the same
// directory plus a rename, so a crash or a full disk mid-write can never leave
// a truncated file behind. Every file this agent owns that is read again after
// a restart -- rules.json, peers.json, agent.env -- goes through here.
//
// The temp file is created in the destination directory on purpose: rename(2)
// is only atomic within one filesystem.
func WriteFileAtomic(path string, b []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // a no-op once the rename succeeded

	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
