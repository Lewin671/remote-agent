package updater

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/Lewin671/remote-agent/internal/buildinfo"
	"github.com/Lewin671/remote-agent/internal/protocol"
)

//go:embed policy.json
var policyJSON []byte

type Policy struct {
	State     int  `json:"state"`
	Automatic bool `json:"automatic"`
}

func BuildPolicy() Policy {
	var p Policy
	if json.Unmarshal(policyJSON, &p) != nil || p.State < 1 {
		panic("invalid embedded update policy")
	}
	return p
}

// Health is served only on the daemon's owner-only Unix socket. It is not a
// client/relay RPC and exposes no session content or account information.
type Health struct {
	Format     int       `json:"format"`
	Version    string    `json:"version"`
	Commit     string    `json:"commit"`
	Channel    string    `json:"channel"`
	IdleSince  time.Time `json:"idleSince"`
	Protocol   int       `json:"protocol"`
	State      int       `json:"state"`
	PID        int       `json:"pid"`
	Executable string    `json:"executable"`
	Busy       bool      `json:"busy"`
	BusyReason string    `json:"busyReason,omitempty"`
}

func Build() Health {
	return Health{Format: 1, Version: buildinfo.Version, Commit: buildinfo.Commit, Channel: buildinfo.Channel, Protocol: protocol.Version, State: BuildPolicy().State, PID: os.Getpid()}
}

type service interface {
	Verify(context.Context, string, string) error
	Restart(context.Context) error
	PID(context.Context) (int, error)
}

type Updater struct {
	helper                        bool
	exe, state, platform, baseURL string
	current                       Health
	http                          *http.Client
	health                        func(context.Context) (Health, error)
	service                       service
	autoCommand                   func(context.Context, string, ...string) (string, error)
	probe                         func(context.Context, string) (Health, error)
	stableFor, healthTimeout      time.Duration
}

func New(exe, state string) (*Updater, error) {
	resolved, err := filepath.EvalSymlinks(exe)
	if os.IsNotExist(err) {
		parent, e := filepath.EvalSymlinks(filepath.Dir(exe))
		if e != nil {
			return nil, e
		}
		resolved, err = filepath.Join(parent, filepath.Base(exe)), nil
	}
	if err != nil {
		return nil, err
	}
	exe = resolved
	exe, err = filepath.Abs(exe)
	if err != nil {
		return nil, err
	}
	state, err = filepath.Abs(state)
	if err != nil {
		return nil, err
	}
	u := &Updater{exe: exe, state: state, platform: runtime.GOOS + "-" + runtime.GOARCH, baseURL: releases, current: Build(), http: downloadClient(), service: systemService{}, autoCommand: command, stableFor: 5 * time.Second, healthTimeout: 30 * time.Second}
	u.health = func(ctx context.Context) (Health, error) { return ReadHealth(ctx, filepath.Join(state, "daemon.sock")) }
	u.probe = func(ctx context.Context, path string) (Health, error) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		b, err := exec.CommandContext(ctx, path, "update-build-info").Output()
		if err != nil {
			return Health{}, fmt.Errorf("cannot run downloaded build: %w", err)
		}
		var h Health
		err = json.Unmarshal(b, &h)
		return h, err
	}
	return u, nil
}

// NewHelper targets the installation while the caller runs its retained updater copy.
func NewHelper(exe, state string) (*Updater, error) {
	u, err := New(exe, state)
	if err == nil {
		u.helper = true
	}
	return u, err
}

func ReadHealth(ctx context.Context, socket string) (Health, error) {
	var h Health
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer tr.CloseIdleConnections()
	c := &http.Client{Transport: tr, Timeout: 2 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://remote-agent/update/health", nil)
	if err != nil {
		return h, err
	}
	res, err := c.Do(req)
	if err != nil {
		return h, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return h, errors.New("daemon does not support update health checks; bootstrap with the release installer")
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 16<<10)).Decode(&h)
	if err == nil && (h.Format != 1 || h.PID <= 0 || h.Executable == "") {
		err = errors.New("invalid daemon health response")
	}
	return h, err
}

type Result struct {
	At      time.Time `json:"at"`
	Outcome string    `json:"outcome"`
	From    string    `json:"from"`
	To      string    `json:"to,omitempty"`
	Error   string    `json:"error,omitempty"`
}
type transaction struct {
	Attempts     int     `json:"attempts"`
	Previous     Health  `json:"previous"`
	Target       Release `json:"target"`
	BackupSHA256 string  `json:"backupSHA256"`
}

func atomicFile(path string, b []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".update-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(b)
	}
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
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return atomicFile(path, append(b, '\n'), 0600)
}
func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	_, err = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), err
}

func (u *Updater) lock() (func(), error) {
	f, err := os.OpenFile(u.exe+".update.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another updater is already running")
	}
	// Do not unlink the lock file: another process may already have opened it.
	return func() { f.Close() }, nil
}

func (u *Updater) Status() (map[string]any, error) {
	out := map[string]any{"installed": u.current.Version, "executable": u.exe, "automatic": AutoConfig{}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if h, e := u.health(ctx); e == nil {
		out["daemon"] = h
	} else {
		out["daemonError"] = e.Error()
	}
	for key, name := range map[string]string{"automatic": "update-auto.json", "last": "update-status.json", "failedVersion": "update-failed-version.json"} {
		b, err := os.ReadFile(filepath.Join(u.state, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var v any
		if err = json.Unmarshal(b, &v); err != nil {
			return nil, err
		}
		out[key] = v
	}
	_, err := os.Stat(u.exe + ".update-transaction.json")
	out["recoveryPending"] = err == nil
	return out, nil
}

func (u *Updater) waitHealthy(ctx context.Context, want Health, changedPID bool) error {
	ctx, cancel := context.WithTimeout(ctx, u.healthTimeout)
	defer cancel()
	var since time.Time
	var pid int
	for {
		h, err := u.health(ctx)
		servicePID, pidErr := u.service.PID(ctx)
		if err == nil && pidErr == nil && h.PID == servicePID && h.Version == want.Version && h.Protocol == want.Protocol && h.State == want.State && h.Executable == u.exe && (!changedPID || h.PID != want.PID) {
			if pid != h.PID || since.IsZero() {
				since = time.Now()
				pid = h.PID
			}
			if time.Since(since) >= u.stableFor {
				return nil
			}
		} else {
			since = time.Time{}
			pid = 0
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("new daemon did not become healthy: %w", ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (u *Updater) finish() error {
	// Keep a recoverable backup at every crash point, including commit itself.
	if err := os.Rename(u.exe+".rollback", u.exe+".previous"); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := syncDir(filepath.Dir(u.exe)); err != nil {
		return err
	}
	if err := os.Remove(u.exe + ".update-transaction.json"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncDir(filepath.Dir(u.exe))
}

func (u *Updater) rollback(ctx context.Context, t transaction) error {
	backup := u.exe + ".rollback"
	if _, e := os.Stat(backup); os.IsNotExist(e) {
		backup = u.exe + ".previous"
	}
	hash, err := fileDigest(backup)
	if err != nil {
		return fmt.Errorf("recovery backup unavailable: %w", err)
	}
	if hash != t.BackupSHA256 {
		return errors.New("recovery backup checksum mismatch; preserved for manual repair")
	}
	// Keep the backup until recovery is verified, even if power fails again.
	b, err := os.ReadFile(backup)
	if err != nil {
		return err
	}
	if err = atomicFile(u.exe, b, 0755); err != nil {
		return err
	}
	if err = u.service.Restart(ctx); err != nil {
		return err
	}
	if err = u.waitHealthy(ctx, t.Previous, false); err != nil {
		return err
	}
	return u.finish()
}

// Recover is run before downloading or evaluating any new update. An interrupted
// transaction is either verified and committed, or rolled back from its journal.
func (u *Updater) recover(ctx context.Context, scheduled bool) (bool, error) {
	b, err := os.ReadFile(u.exe + ".update-transaction.json")
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	var t transaction
	if err = json.Unmarshal(b, &t); err != nil {
		return true, err
	}
	if !digestPattern.MatchString(t.BackupSHA256) || t.Previous.Executable != u.exe {
		return true, errors.New("invalid update recovery journal")
	}
	if err = u.service.Verify(ctx, u.exe, u.state); err != nil {
		return true, err
	}
	if scheduled && t.Attempts >= 3 {
		return true, errors.New("automatic recovery stopped after three attempts; run a manual update to retry recovery")
	}
	t.Attempts++
	if err = writeJSON(u.exe+".update-transaction.json", t); err != nil {
		return true, err
	}
	want := Health{Version: t.Target.Version, Protocol: t.Target.Protocol, State: t.Target.State, PID: t.Previous.PID}
	outcome := "recovered"
	if err = u.waitHealthy(ctx, want, true); err == nil {
		err = u.finish()
	} else {
		outcome = "rolled_back"
		err = u.rollback(ctx, t)
	}
	result := Result{At: time.Now().UTC(), Outcome: outcome, From: t.Previous.Version, To: t.Target.Version}
	if err != nil {
		result.Outcome = "recovery_failed"
		result.Error = err.Error()
	}
	if outcome == "rolled_back" || result.Outcome == "recovery_failed" {
		err = errors.Join(err, writeJSON(filepath.Join(u.state, "update-failed-version.json"), result))
	}
	return true, errors.Join(err, writeJSON(filepath.Join(u.state, "update-status.json"), result))
}

func (u *Updater) Apply(ctx context.Context, version string, scheduled bool) (result Result, err error) {
	unlock, err := u.lock()
	if err != nil {
		return result, err
	}
	defer unlock()
	if err = os.MkdirAll(u.state, 0700); err != nil {
		return result, err
	}
	if recovered, e := u.recover(ctx, scheduled); recovered {
		b, re := os.ReadFile(filepath.Join(u.state, "update-status.json"))
		if re == nil {
			re = json.Unmarshal(b, &result)
		}
		return result, errors.Join(e, re)
	}
	if scheduled {
		cfg, e := u.readAuto()
		if e != nil {
			return result, e
		}
		if !cfg.Enabled {
			return Result{Outcome: "disabled"}, nil
		}
	}
	if u.helper {
		if u.current.Channel != "release" {
			return result, errors.New("recovery helper is not an official release")
		}
		disk, e := u.probe(ctx, u.exe)
		if e != nil {
			return result, e
		}
		if disk.Format != 1 || disk.Channel != "release" {
			return result, errors.New("installed build cannot be updated by this helper")
		}
		u.current = disk
	}
	result = Result{At: time.Now().UTC(), From: u.current.Version, Outcome: "failed"}
	defer func() {
		if err != nil {
			result.Error = err.Error()
		}
		if result.Outcome == "rolled_back" || result.Outcome == "recovery_failed" {
			err = errors.Join(err, writeJSON(filepath.Join(u.state, "update-failed-version.json"), result))
		}
		err = errors.Join(err, writeJSON(filepath.Join(u.state, "update-status.json"), result))
	}()
	r, err := u.Check(ctx, version)
	if err != nil {
		return result, err
	}
	result.To = r.Version
	if u.current.Channel != "release" {
		return result, errors.New("self-update requires an official release installation; use the release installer to switch from a source build")
	}
	if !r.Available {
		result.Outcome = "current"
		return result, nil
	}
	if !r.Allowed {
		result.Outcome = "blocked"
		return result, errors.New(r.Reason)
	}
	if scheduled {
		var failed Result
		b, e := os.ReadFile(filepath.Join(u.state, "update-failed-version.json"))
		if e != nil && !os.IsNotExist(e) {
			return result, e
		}
		if e == nil {
			if e = json.Unmarshal(b, &failed); e != nil {
				return result, e
			}
			if failed.To == r.Version {
				result.Outcome = "blocked"
				return result, errors.New("this version previously failed; retry with a manual update")
			}
		}
	}

	if err = u.service.Verify(ctx, u.exe, u.state); err != nil {
		return result, err
	}
	before, err := u.health(ctx)
	if err != nil {
		return result, err
	}
	pid, pidErr := u.service.PID(ctx)
	if pidErr != nil || pid != before.PID {
		return result, errors.New("running daemon is not the process managed by the service")
	}
	if before.Version != u.current.Version || before.Protocol != u.current.Protocol || before.State != u.current.State || before.Executable != u.exe {
		return result, errors.New("installed command and running daemon differ; restart the managed daemon first")
	}
	if !idle(before) {
		result.Outcome = "deferred"
		return result, errors.New("daemon has active work or pending interaction; retry when idle")
	}
	disk, err := u.probe(ctx, u.exe)
	if err != nil {
		return result, err
	}
	if disk.Version != u.current.Version || disk.Channel != "release" {
		return result, errors.New("installed binary differs from the updater process")
	}
	oldHash, err := fileDigest(u.exe)
	if err != nil {
		return result, err
	}
	staged, notices, err := u.stage(ctx, r)
	if err != nil {
		return result, err
	}
	defer os.Remove(staged)
	info, err := u.probe(ctx, staged)
	if err != nil {
		return result, err
	}
	if info.Format != 1 || info.Channel != "release" || info.Version != r.Version || info.Protocol != r.Protocol || info.State != r.State {
		return result, errors.New("downloaded build disagrees with release manifest")
	}
	// Downloading may have taken minutes. Recheck immediately before replacement.
	before, err = u.health(ctx)
	if err != nil {
		return result, err
	}
	if before.Version != u.current.Version || before.Executable != u.exe || !idle(before) {
		result.Outcome = "deferred"
		return result, errors.New("daemon changed or became busy; update deferred")
	}
	if err = u.service.Verify(ctx, u.exe, u.state); err != nil {
		return result, err
	}
	pid, pidErr = u.service.PID(ctx)
	if pidErr != nil || pid != before.PID {
		return result, errors.New("daemon service changed while downloading update")
	}
	hash, err := fileDigest(u.exe)
	if err != nil {
		return result, err
	}
	if hash != oldHash {
		return result, errors.New("installed binary changed during update")
	}
	if err = atomicFile(filepath.Join(u.state, "THIRD_PARTY_NOTICES.txt"), []byte(notices), 0644); err != nil {
		return result, err
	}
	old, err := os.ReadFile(u.exe)
	if err != nil {
		return result, err
	}
	if err = atomicFile(u.exe+".rollback", old, 0755); err != nil {
		return result, err
	}
	// Refresh the stable recovery entry point before replacing the installation.
	if err = atomicFile(u.exe+".updater", old, 0755); err != nil {
		return result, err
	}
	t := transaction{Previous: before, Target: r, BackupSHA256: oldHash, Attempts: 1}
	if err = writeJSON(u.exe+".update-transaction.json", t); err != nil {
		return result, err
	}
	// From this point every failure leaves a recovery journal or restores the old build.
	if err = os.Rename(staged, u.exe); err == nil {
		err = syncDir(filepath.Dir(u.exe))
	}
	if err == nil {
		err = u.service.Restart(ctx)
	}
	if err == nil {
		err = u.waitHealthy(ctx, Health{Version: r.Version, Protocol: r.Protocol, State: r.State, PID: before.PID}, true)
	}
	if err != nil {
		cause := err
		// A canceled download/update must not prevent recovery of an already replaced binary.
		rctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if re := u.rollback(rctx, t); re != nil {
			result.Outcome = "recovery_failed"
			return result, errors.Join(cause, fmt.Errorf("rollback failed: %w", re))
		}
		result.Outcome = "rolled_back"
		return result, fmt.Errorf("update failed; previous version restored: %w", cause)
	}
	if err = u.finish(); err != nil {
		return result, err
	}
	result.Outcome = "updated"
	return result, nil
}

func idle(h Health) bool {
	return !h.Busy && !h.IdleSince.IsZero() && time.Since(h.IdleSince) >= 30*time.Second
}
