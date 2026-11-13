package main

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
)

type v3Identity struct {
	Pin              deviceauth.Pin
	States           []deviceauth.State
	Proofs           []*deviceauth.RecoveryStatement
	ID               string
	EdSeed, XPrivate []byte
	Lease            deviceauth.LeaseCertificate
}

func (r *run) upgrade(ctx context.Context) response {
	if !r.usingV3 || r.oldEnvelope == nil {
		return response{Code: "driver_upgrade_state"}
	}
	if r.stopDaemon != nil {
		r.stopDaemon()
		r.stopDaemon = nil
	}
	for _, args := range [][]string{{"--home", r.env["REMOTE_AGENT_HOME"], "--dry-run"}, {"--home", r.env["REMOTE_AGENT_HOME"]}} {
		cmd := exec.CommandContext(ctx, filepath.Join(r.root, "bin/migrate-security-v4"), args...)
		guarded(cmd, r.env)
		if err := cmd.Run(); err != nil {
			return response{Code: "driver_migration", Error: fmt.Sprint(err)}
		}
	}
	r.binary = filepath.Join(r.root, "bin/remote-agent")
	r.usingV3 = false
	before := r.bridge.ready.Load()
	r.restart()
	if err := poll(ctx, "upgraded authenticated Ready", func() bool { return r.bridge.ready.Load() > before }); err != nil {
		return response{Code: "driver_upgrade_ready", Error: err.Error()}
	}
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := r.client.Forward(probe, r.machine, r.oldEnvelope); !remoteclient.Uncertain(err) {
		return response{Code: "driver_old_result", Error: "old ID did not remain unknown"}
	}
	return response{Code: "ok", Unknown: false}
}
