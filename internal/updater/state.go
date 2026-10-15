package updater

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// EnsureState refuses a different storage generation before any command opens
// daemon data. This is a guard, not a migration. Generation 1 marks existing
// installations at bootstrap; later generations must not adopt unmarked data.
func EnsureState(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, "state-generation")
	b, err := os.ReadFile(path)
	if err == nil {
		n, e := strconv.Atoi(strings.TrimSpace(string(b)))
		if e != nil || n != BuildPolicy().State {
			return errors.New("daemon storage generation differs from this build; follow the manual upgrade instructions")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	if BuildPolicy().State != 1 {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		if len(entries) > 0 {
			return errors.New("unmarked daemon data requires a manual upgrade")
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		return EnsureState(dir)
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, "%d\n", BuildPolicy().State)
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	return syncDir(dir)
}
