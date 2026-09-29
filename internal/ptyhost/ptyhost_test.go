package ptyhost

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func shortTempDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "ra-pty")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func waitForSocket(t *testing.T, path string) *Conn {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := Dial(path); err == nil {
			return c
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("socket %s never came up", path)
	return nil
}

// Local bytes typed while input is held must reach the child after the
// injected bytes, not interleaved with them and not lost.
func TestHoldBuffersLocalInput(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "c.sock")
	out := filepath.Join(dir, "out")

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdinW.Close()

	done := make(chan int, 1)
	go func() {
		code, err := run(sock, []string{"/bin/sh", "-c", "stty raw -echo; head -c 12 > " + out}, stdinR, io.Discard)
		if err != nil {
			t.Error(err)
		}
		done <- code
	}()

	c := waitForSocket(t, sock)
	defer c.Close()
	time.Sleep(200 * time.Millisecond) // let stty take effect

	if _, err := stdinW.Write([]byte("ab")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := c.Hold(time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := stdinW.Write([]byte("cdef")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := c.Write([]byte("XYZWVU")); err != nil {
		t.Fatal(err)
	}
	if err := c.Release(); err != nil {
		t.Fatal(err)
	}

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("child exit code %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child did not exit")
	}
	got, _ := os.ReadFile(out)
	if string(got) != "abXYZWVUcdef" {
		t.Fatalf("child read %q, want %q", got, "abXYZWVUcdef")
	}
}

// Closing the control connection releases a hold it made.
func TestDisconnectReleasesHold(t *testing.T) {
	dir := shortTempDir(t)
	sock := filepath.Join(dir, "c.sock")
	out := filepath.Join(dir, "out")
	stdinR, stdinW, _ := os.Pipe()
	defer stdinW.Close()
	done := make(chan int, 1)
	go func() {
		code, _ := run(sock, []string{"/bin/sh", "-c", "stty raw -echo; head -c 3 > " + out}, stdinR, io.Discard)
		done <- code
	}()
	c := waitForSocket(t, sock)
	time.Sleep(200 * time.Millisecond)
	if _, err := c.Hold(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	stdinW.Write([]byte("abc"))
	time.Sleep(100 * time.Millisecond)
	c.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("held input was not released after disconnect")
	}
	got, _ := os.ReadFile(out)
	if string(got) != "abc" {
		t.Fatalf("got %q", got)
	}
}
