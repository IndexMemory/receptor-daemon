package core

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
)

// randomID returns a random hex string, used anywhere a unique identifier
// is needed (e.g. distinguishing test fixture paths).
func randomID() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// atomicWriteFile writes to a temp file in the same directory then renames
// it into place, so a crash or concurrent read never sees a half-written
// file.
func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}
