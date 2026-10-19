//go:build !windows

package relay

import "os"

func renamePrivateFile(from, to string) error { return os.Rename(from, to) }
func linkPrivateFile(from, to string) error   { return os.Link(from, to) }
func syncPrivateDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
