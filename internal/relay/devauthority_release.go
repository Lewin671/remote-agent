//go:build !dev

package relay

import (
	"context"
	"errors"
)

// DevInit is absent from release binaries. Build with -tags dev for the
// isolated development environment and the distribution smoke test.
func DevInit(ctx context.Context, server, setupToken string) (*Account, error) {
	return nil, errors.New("this binary was not built with the dev tag")
}
