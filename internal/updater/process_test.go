package updater

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

type processService struct {
	u   *Updater
	cmd *exec.Cmd
}

func (s *processService) Verify(context.Context, string, string) error { return nil }
func (s *processService) PID(context.Context) (int, error)             { return s.cmd.Process.Pid, nil }
func (s *processService) stop() {
	if s.cmd != nil {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
		s.cmd = nil
	}
}
func (s *processService) Restart(ctx context.Context) error {
	s.stop()
	s.cmd = exec.Command(s.u.exe, "daemon", filepath.Join(s.u.state, "daemon.sock"))
	// Service readiness is a subprocess handshake, not a guess that a
	// 2-second health budget covers OS process startup under -race.
	out, err := s.cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := s.cmd.Start(); err != nil {
		return err
	}
	ready := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(out).ReadString('\n')
		if err == nil && line != "ready\n" {
			err = fmt.Errorf("unexpected startup message %q", line)
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestUpdateWithRealProcesses(t *testing.T) {
	// Short Unix socket path on macOS; every resource belongs to this test.
	dir, err := os.MkdirTemp("/tmp", "ra-update-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	build := func(version string) []byte {
		path := filepath.Join(dir, "helper-"+version)
		cmd := exec.Command("go", "build", "-o", path, "-ldflags=-X main.version="+version, "./testdata/daemon.go")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("helper build: %s: %v", b, err)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	old, newBuild := build("1.1.0"), build("1.2.0")
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint("startupFailure=", fail), func(t *testing.T) {
			f := setup(t)
			f.u.exe = filepath.Join(dir, "remote-agent")
			f.u.state = filepath.Join(dir, "state")
			if err := os.MkdirAll(f.u.state, 0700); err != nil {
				t.Fatal(err)
			}
			if err := atomicFile(f.u.exe, old, 0755); err != nil {
				t.Fatal(err)
			}
			u, err := New(f.u.exe, f.u.state)
			if err != nil {
				t.Fatal(err)
			}
			f.u.current.Executable = f.u.exe
			// Keep the genuine downloaded-build probe and Unix health transport.
			f.u.probe = u.probe
			f.u.health = u.health
			// Stability/repeated PID checks have their own virtual-clock test.
			// Here the helper is already listening when Restart returns.
			f.u.healthTimeout = u.healthTimeout
			f.u.stableFor = 0
			service := &processService{u: f.u}
			f.u.service = service
			defer service.stop()
			if fail {
				t.Setenv("UPDATE_HELPER_FAIL_NEW", "1")
			} else {
				t.Setenv("UPDATE_HELPER_FAIL_NEW", "")
			}
			if err := service.Restart(context.Background()); err != nil {
				t.Fatal(err)
			}
			h, err := u.health(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			f.u.current = h
			var b bytes.Buffer
			gz := gzip.NewWriter(&b)
			tw := tar.NewWriter(gz)
			for _, entry := range []struct {
				name string
				data []byte
			}{{"remote-agent", newBuild}, {"remote-agent-service", []byte("#!/bin/sh\n")}, {"THIRD_PARTY_NOTICES.txt", []byte("licenses")}} {
				if err := tw.WriteHeader(&tar.Header{Name: "remote-agent-v1.2.0-linux-amd64/" + entry.name, Typeflag: tar.TypeReg, Mode: 0755, Size: int64(len(entry.data))}); err != nil {
					t.Fatal(err)
				}
				if _, err := tw.Write(entry.data); err != nil {
					t.Fatal(err)
				}
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			f.archive = b.Bytes()
			hash := sha256.Sum256(f.archive)
			f.metadata = []byte(fmt.Sprintf("version=1.2.0\nprotocol=%d\ndaemonUpdate=1\ndaemonState=1\ndaemonAutomatic=true\ndaemon.linux-amd64.sha256=%s\ndaemon.linux-amd64.size=%d\n", f.u.current.Protocol, hex.EncodeToString(hash[:]), len(f.archive)))
			previousPID := service.cmd.Process.Pid
			r, err := f.u.Apply(context.Background(), "", false)
			want := "updated"
			wantVersion := "1.2.0"
			if fail {
				want = "rolled_back"
				wantVersion = "1.1.0"
			}
			if r.Outcome != want || (fail != (err != nil)) {
				t.Fatalf("result=%+v err=%v", r, err)
			}
			live, err := u.health(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if live.Version != wantVersion || live.PID == previousPID || live.PID != service.cmd.Process.Pid {
				t.Fatalf("live=%+v previous PID=%d", live, previousPID)
			}
		})
	}
}
