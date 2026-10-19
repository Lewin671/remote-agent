package relay

import "golang.org/x/sys/windows"

func movePrivateFile(from, to string, replace bool) error {
	src, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	dst, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	flags := uint32(windows.MOVEFILE_WRITE_THROUGH)
	if replace {
		flags |= windows.MOVEFILE_REPLACE_EXISTING
	}
	return windows.MoveFileEx(src, dst, flags)
}
func renamePrivateFile(from, to string) error { return movePrivateFile(from, to, true) }
func linkPrivateFile(from, to string) error   { return movePrivateFile(from, to, false) }

// The move itself uses WRITE_THROUGH; Windows does not expose POSIX directory fsync.
func syncPrivateDirectory(string) error { return nil }
