package main

import (
	"os"
	"path/filepath"
)

// tempDir returns a stable subdirectory of the OS temp dir for downloads.
func tempDir(sub string) string {
	dir := filepath.Join(os.TempDir(), sub)
	os.MkdirAll(dir, 0o755)
	return dir
}
