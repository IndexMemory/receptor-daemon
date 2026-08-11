package core

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// SHA256File hashes a file's contents. io.Copy streams in fixed-size
// chunks internally, so large files never need to be loaded fully into
// memory.
func SHA256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
