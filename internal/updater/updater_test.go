package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func archive(t *testing.T, extra *tar.Header) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	root := "remote-agent-v1.2.0-linux-amd64/"
	for _, entry := range []struct{ name, text string }{{"remote-agent", "new"}, {"remote-agent-service", "#!/bin/sh\n"}, {"THIRD_PARTY_NOTICES.txt", "licenses"}} {
		if err := tw.WriteHeader(&tar.Header{Name: root + entry.name, Typeflag: tar.TypeReg, Size: int64(len(entry.text)), Mode: 0755}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(entry.text)); err != nil {
			t.Fatal(err)
		}
	}
	if extra != nil {
		if err := tw.WriteHeader(extra); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

type fixture struct {
	u                   *Updater
	live                Health
	metadata, archive   []byte
	restarts, downloads int
	badStart            bool
	verifyErr           error
	managerPID          int
}
type fakeService struct{ f *fixture }

func (s fakeService) Verify(context.Context, string, string) error { return s.f.verifyErr }
func (s fakeService) PID(context.Context) (int, error) {
	if s.f.managerPID > 0 {
		return s.f.managerPID, nil
	}
	return s.f.live.PID, nil
}
func (s fakeService) Restart(context.Context) error {
	f := s.f
	f.restarts++
	b, err := os.ReadFile(f.u.exe)
	if err != nil {
		return err
	}
	if string(b) == "new" && f.badStart {
		return errors.New("simulated startup failure")
	}
	f.live.PID++
	if string(b) == "new" {
		f.live.Version = "1.2.0"
	} else {
		f.live.Version = "1.1.0"
	}
	return nil
}

func setup(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "remote-agent")
	if err := os.WriteFile(exe, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}
	u, err := New(exe, filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	u.current = Health{Format: 1, Version: "1.1.0", Channel: "release", Protocol: 6, State: 1, PID: 100, Executable: u.exe, IdleSince: time.Now().Add(-time.Minute)}
	u.platform = "linux-amd64"
	u.stableFor = 0
	u.healthTimeout = 20 * time.Millisecond
	f := &fixture{u: u, live: u.current, archive: archive(t, nil)}
	h := sha256.Sum256(f.archive)
	f.metadata = []byte(fmt.Sprintf("version=1.2.0\nprotocol=6\ndaemonUpdate=1\ndaemonState=1\ndaemonAutomatic=true\ndaemon.linux-amd64.sha256=%s\ndaemon.linux-amd64.size=%d\nnotes=Unicode 中文 and escaped\\nnotes\\\\path\n", hex.EncodeToString(h[:]), len(f.archive)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/latest.properties") {
			w.Write(f.metadata)
			return
		}
		if strings.HasSuffix(r.URL.Path, ".tar.gz") {
			f.downloads++
			w.Write(f.archive)
			return
		}
		w.WriteHeader(404)
	}))
	t.Cleanup(srv.Close)
	u.baseURL = srv.URL
	u.http = srv.Client()
	u.health = func(context.Context) (Health, error) { return f.live, nil }
	u.service = fakeService{f}
	u.probe = func(_ context.Context, path string) (Health, error) {
		b, err := os.ReadFile(path)
		h := u.current
		if string(b) == "new" {
			h.Version = "1.2.0"
		}
		return h, err
	}
	return f
}

func TestApplyAndRetainPrevious(t *testing.T) {
	f := setup(t)
	r, err := f.u.Apply(context.Background(), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Outcome != "updated" || f.restarts != 1 || f.live.Version != "1.2.0" {
		t.Fatalf("result=%+v restarts=%d live=%+v", r, f.restarts, f.live)
	}
	b, _ := os.ReadFile(f.u.exe + ".previous")
	if string(b) != "old" {
		t.Fatalf("backup=%q", b)
	}
	if _, err = os.Stat(f.u.exe + ".update-transaction.json"); !os.IsNotExist(err) {
		t.Fatalf("journal remains: %v", err)
	}
	b, _ = os.ReadFile(filepath.Join(f.u.state, "THIRD_PARTY_NOTICES.txt"))
	if string(b) != "licenses" {
		t.Fatalf("notices=%q", b)
	}
}

func TestPolicyBlocksBeforeDownload(t *testing.T) {
	for _, tc := range []struct{ name, from, to string }{{"protocol", "protocol=6", "protocol=7"}, {"storage", "daemonState=1", "daemonState=2"}, {"manual", "daemonAutomatic=true", "daemonAutomatic=false"}, {"oldManifest", "daemonUpdate=1", "daemonUpdate=0"}} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			f.metadata = bytes.ReplaceAll(f.metadata, []byte(tc.from), []byte(tc.to))
			r, err := f.u.Apply(context.Background(), "", false)
			if err == nil || r.Outcome != "blocked" || f.downloads != 0 || f.restarts != 0 {
				t.Fatalf("result=%+v error=%v downloads=%d", r, err, f.downloads)
			}
		})
	}
}

func TestUnsafeLocalStateCannotReplaceBinary(t *testing.T) {
	for _, name := range []string{"busy", "recentlyIdle", "differentDaemon", "sourceBuild", "wrongServicePID", "unmanaged", "diskChanged"} {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			switch name {
			case "busy":
				f.live.Busy = true
			case "recentlyIdle":
				f.live.IdleSince = time.Now()
			case "differentDaemon":
				f.live.Version = "1.0.0"
			case "sourceBuild":
				f.u.current.Channel = "source"
			case "wrongServicePID":
				f.managerPID = 999
			case "unmanaged":
				f.verifyErr = errors.New("not managed")
			case "diskChanged":
				f.u.probe = func(context.Context, string) (Health, error) { h := f.u.current; h.Version = "1.0.0"; return h, nil }
			}
			_, err := f.u.Apply(context.Background(), "", false)
			if err == nil || f.restarts != 0 {
				t.Fatalf("err=%v restarts=%d", err, f.restarts)
			}
			b, _ := os.ReadFile(f.u.exe)
			if string(b) != "old" {
				t.Fatalf("binary changed: %q", b)
			}
		})
	}
}

func TestCorruptArchiveNeverExecutesOrRestarts(t *testing.T) {
	f := setup(t)
	f.archive[len(f.archive)/2] ^= 1
	f.u.probe = func(_ context.Context, path string) (Health, error) {
		if path != f.u.exe {
			t.Fatal("executed unverified download")
		}
		return f.u.current, nil
	}
	_, err := f.u.Apply(context.Background(), "", false)
	if err == nil || f.restarts != 0 {
		t.Fatalf("error=%v restarts=%d", err, f.restarts)
	}
}

func TestArchiveRejectsLinksAndTraversal(t *testing.T) {
	for _, header := range []*tar.Header{{Name: "../remote-agent", Typeflag: tar.TypeReg}, {Name: "remote-agent-v1.2.0-linux-amd64/remote-agent", Typeflag: tar.TypeSymlink, Linkname: "/bin/sh"}, {Name: "remote-agent-v1.2.0-linux-amd64/extra", Typeflag: tar.TypeReg}} {
		f := setup(t)
		f.archive = archive(t, header)
		h := sha256.Sum256(f.archive)
		r := Release{Version: "1.2.0", Archive: "remote-agent-v1.2.0-linux-amd64.tar.gz", SHA256: hex.EncodeToString(h[:]), Size: int64(len(f.archive))}
		_, _, err := f.u.stage(context.Background(), r)
		if err == nil {
			t.Fatalf("accepted %+v", header)
		}
		matches, _ := filepath.Glob(filepath.Join(filepath.Dir(f.u.exe), ".remote-agent-stage-*"))
		if len(matches) > 0 {
			t.Fatalf("staging leak: %v", matches)
		}
	}
}

func TestRollbackAndScheduledFailureSuppression(t *testing.T) {
	f := setup(t)
	f.badStart = true
	r, err := f.u.Apply(context.Background(), "", false)
	if err == nil || r.Outcome != "rolled_back" || f.restarts != 2 {
		t.Fatalf("%+v err=%v restarts=%d", r, err, f.restarts)
	}
	b, _ := os.ReadFile(f.u.exe)
	if string(b) != "old" {
		t.Fatal("old binary not restored")
	}
	if err = writeJSON(filepath.Join(f.u.state, "update-auto.json"), AutoConfig{Enabled: true, Executable: f.u.exe}); err != nil {
		t.Fatal(err)
	}
	// A subsequent failed check must not erase the remembered bad version.
	if err := writeJSON(filepath.Join(f.u.state, "update-status.json"), Result{Outcome: "failed", Error: "network unavailable"}); err != nil {
		t.Fatal(err)
	}
	_, err = f.u.Apply(context.Background(), "", true)
	if err == nil || f.restarts != 2 || f.downloads != 1 {
		t.Fatalf("failed version retried: %v restarts=%d downloads=%d", err, f.restarts, f.downloads)
	}
	f.badStart = false
	r, err = f.u.Apply(context.Background(), "", false)
	if err != nil || r.Outcome != "updated" {
		t.Fatalf("manual retry failed: %+v %v", r, err)
	}
}

func TestInterruptedTransactionsRecoverBeforeNetwork(t *testing.T) {
	for _, name := range []string{"beforeRename", "newRunning", "newUnhealthy", "missingTarget", "committing", "badBackup"} {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			if err := os.MkdirAll(f.u.state, 0700); err != nil {
				t.Fatal(err)
			}
			digest, _ := fileDigest(f.u.exe)
			if err := atomicFile(f.u.exe+".rollback", []byte("old"), 0755); err != nil {
				t.Fatal(err)
			}
			tr := transaction{Previous: f.live, Target: Release{Version: "1.2.0", Protocol: 6, State: 1}, BackupSHA256: digest}
			if err := writeJSON(f.u.exe+".update-transaction.json", tr); err != nil {
				t.Fatal(err)
			}
			if name != "beforeRename" {
				_ = os.WriteFile(f.u.exe, []byte("new"), 0755)
			}
			if name == "missingTarget" {
				_ = os.Remove(f.u.exe)
				if _, e := NewHelper(f.u.exe, f.u.state); e != nil {
					t.Fatal(e)
				}
			}
			if name == "committing" {
				_ = os.Rename(f.u.exe+".rollback", f.u.exe+".previous")
			}
			if name == "newRunning" {
				f.live.Version = "1.2.0"
				f.live.PID++
			}
			if name == "badBackup" {
				_ = os.WriteFile(f.u.exe+".rollback", []byte("corrupt"), 0755)
			}
			f.u.baseURL = "http://127.0.0.1:1"
			r, err := f.u.Apply(context.Background(), "", false)
			if name == "badBackup" {
				if err == nil || r.Outcome != "recovery_failed" {
					t.Fatalf("%+v %v", r, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := "rolled_back"
			if name == "missingTarget" {
				_ = os.Remove(f.u.exe)
				if _, e := NewHelper(f.u.exe, f.u.state); e != nil {
					t.Fatal(e)
				}
			}
			if name == "committing" {
				_ = os.Rename(f.u.exe+".rollback", f.u.exe+".previous")
			}
			if name == "newRunning" {
				want = "recovered"
			}
			if r.Outcome != want || f.downloads != 0 {
				t.Fatalf("%+v downloads=%d", r, f.downloads)
			}
		})
	}
}

func TestHealthyRequiresStableManagedPID(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// More than one 250 ms health poll must run: the former 30 ms
		// deadline rejected even a stable PID before its second observation.
		f := &fixture{u: &Updater{exe: "/test/daemon", stableFor: 500 * time.Millisecond, healthTimeout: 2 * time.Second}}
		f.live = Health{Version: "1.1.0", PID: 100, Executable: f.u.exe}
		f.u.service = fakeService{f}
		calls := 0
		f.u.health = func(context.Context) (Health, error) { calls++; f.live.PID++; return f.live, nil }
		if err := f.u.waitHealthy(context.Background(), f.live, false); err == nil {
			t.Fatal("accepted unstable process")
		}
		if calls < 2 {
			t.Fatal("stability was not checked across polls")
		}
		f.u.health = func(context.Context) (Health, error) { return f.live, nil }
		start := time.Now()
		if err := f.u.waitHealthy(context.Background(), f.live, false); err != nil {
			t.Fatal(err)
		}
		if time.Since(start) != f.u.stableFor {
			t.Fatal("did not wait for stability")
		}
		f.u.stableFor = 0
		f.managerPID = 999
		if err := f.u.waitHealthy(context.Background(), f.live, false); err == nil {
			t.Fatal("accepted unmanaged process")
		}
	})
}

func TestExclusiveUpdaterLock(t *testing.T) {
	f := setup(t)
	unlock, err := f.u.lock()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err = f.u.Apply(context.Background(), "", false); err == nil {
		t.Fatal("second updater acquired lock")
	}
}

func TestAutomaticOffByDefault(t *testing.T) {
	f := setup(t)
	r, err := f.u.Apply(context.Background(), "", true)
	if err != nil || r.Outcome != "disabled" || f.downloads != 0 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestManifestValidation(t *testing.T) {
	for _, tc := range []struct{ from, to string }{{"version=1.2.0", "version=../../evil"}, {"daemonState=1", "daemonState=0"}, {"daemonAutomatic=true", "daemonAutomatic=yes"}, {"protocol=6", "protocol=bad"}, {"daemon.linux-amd64.size=", "duplicate=1\nduplicate=2\ndaemon.linux-amd64.size="}} {
		f := setup(t)
		f.metadata = bytes.ReplaceAll(f.metadata, []byte(tc.from), []byte(tc.to))
		if _, err := f.u.Check(context.Background(), ""); err == nil {
			t.Fatalf("accepted %q", tc.to)
		}
	}
	for _, v := range []string{"1.0", "v1.2.3", "01.2.3", "1.2.3-rc1", "1.2.4294967296", "../../x"} {
		if _, err := versionParts(v); err == nil {
			t.Fatalf("accepted %q", v)
		}
	}
	if ok, err := newer("1.10.0", "1.9.9"); err != nil || !ok {
		t.Fatalf("semver comparison: %v %v", ok, err)
	}
}

func TestReleaseRedirectCannotDowngradeTLS(t *testing.T) {
	reached := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer source.Close()
	c := downloadClient()
	c.Transport = source.Client().Transport
	if _, err := c.Get(source.URL); err == nil || reached {
		t.Fatalf("unsafe redirect: err=%v reached=%v", err, reached)
	}
}

func TestRetainedHelperReadsInstalledVersion(t *testing.T) {
	f := setup(t)
	f.u.helper = true
	f.u.current.Version = "1.0.0"
	f.u.probe = func(_ context.Context, path string) (Health, error) {
		h := f.live
		b, err := os.ReadFile(path)
		if string(b) == "new" {
			h.Version = "1.2.0"
		}
		return h, err
	}
	r, err := f.u.Apply(context.Background(), "", false)
	if err != nil || r.Outcome != "updated" || r.From != "1.1.0" {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestRecoveryAttemptLimit(t *testing.T) {
	f := setup(t)
	_ = os.MkdirAll(f.u.state, 0700)
	digest, _ := fileDigest(f.u.exe)
	tr := transaction{Previous: f.live, Target: Release{Version: "1.2.0", Protocol: 6, State: 1}, BackupSHA256: digest, Attempts: 3}
	if err := writeJSON(f.u.exe+".update-transaction.json", tr); err != nil {
		t.Fatal(err)
	}
	_, err := f.u.Apply(context.Background(), "", true)
	if err == nil || f.restarts != 0 {
		t.Fatalf("exceeded automatic recovery attempts: %v restarts=%d", err, f.restarts)
	}
}
