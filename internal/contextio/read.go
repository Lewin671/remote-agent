// Package contextio isolates uncancellable filesystem calls from request lifetimes.
package contextio

import (
	"context"
	"os"
	"time"
)

// Stuck OS calls retain a slot, never an application lock or an unbounded
// stream of new goroutines. Their buffered result can be discarded safely.
const Limit = 8

var slots = make(chan struct{}, Limit)

const Timeout = 8 * time.Second

func Read[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		return zero, ctx.Err()
	}
	type result struct {
		value T
		err   error
	}
	done := make(chan result, 1)
	go func() { defer func() { <-slots }(); v, err := fn(); done <- result{v, err} }()
	select {
	case r := <-done:
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		return r.value, r.err
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}
func ReadDir(ctx context.Context, path string) ([]os.DirEntry, error) {
	return Read(ctx, func() ([]os.DirEntry, error) { return os.ReadDir(path) })
}
func ReadFile(ctx context.Context, path string) ([]byte, error) {
	return Read(ctx, func() ([]byte, error) { return os.ReadFile(path) })
}
func Stat(ctx context.Context, path string) (os.FileInfo, error) {
	return Read(ctx, func() (os.FileInfo, error) { return os.Stat(path) })
}
