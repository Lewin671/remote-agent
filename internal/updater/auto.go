package updater

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

type AutoConfig struct {
	Enabled    bool   `json:"enabled"`
	Time       string `json:"time"`
	Executable string `json:"executable"`
}

func (u *Updater) readAuto() (AutoConfig, error) {
	var c AutoConfig
	b, err := os.ReadFile(filepath.Join(u.state, "update-auto.json"))
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(b, &c)
	if err == nil && c.Enabled && c.Executable != u.exe {
		err = errors.New("automatic updates belong to another executable")
	}
	return c, err
}

func xmlText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
func unitQuote(s string) string {
	s = strings.ReplaceAll(s, "%", "%%")
	return strconv.Quote(s)
}

// Capture the same CLI environment as daemon.sh so launchd/systemd's minimal
// environment cannot silently switch the daemon's state, tmux socket or tools.
func (u *Updater) autoFiles(osName, home, at string) (map[string]string, error) {
	if !regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`).MatchString(at) {
		return nil, errors.New("time must be HH:MM in local time")
	}
	parts := strings.Split(at, ":")
	hour, _ := strconv.Atoi(parts[0])
	minute, _ := strconv.Atoi(parts[1])
	env := map[string]string{"REMOTE_AGENT_HOME": u.state, "PATH": os.Getenv("PATH")}
	for _, key := range []string{"REMOTE_AGENT_TMUX_SOCKET", "REMOTE_AGENT_CODEX", "CODEX_HOME", "REMOTE_AGENT_CLAUDE", "CLAUDE_CONFIG_DIR"} {
		if val := os.Getenv(key); val != "" {
			env[key] = val
		}
	}
	// The task configuration is owner-only.
	switch osName {
	case "darwin":
		var e strings.Builder
		for key, val := range env {
			fmt.Fprintf(&e, "<key>%s</key><string>%s</string>", key, xmlText(val))
		}
		body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>update</string><string>--scheduled</string><string>--helper-for</string><string>%s</string></array>
<key>EnvironmentVariables</key><dict>%s</dict>
<key>WorkingDirectory</key><string>%s</string>
<key>StartCalendarInterval</key><dict><key>Hour</key><integer>%d</integer><key>Minute</key><integer>%d</integer></dict>
<key>RunAtLoad</key><true/>
<key>StandardOutPath</key><string>%s</string>
<key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, updateLabel, xmlText(u.exe+".updater"), xmlText(u.exe), e.String(), xmlText(home), hour, minute, xmlText(filepath.Join(u.state, "update.log")), xmlText(filepath.Join(u.state, "update.log")))
		return map[string]string{filepath.Join(home, "Library/LaunchAgents", updateLabel+".plist"): body}, nil
	case "linux":
		var e strings.Builder
		for key, val := range env {
			fmt.Fprintf(&e, "Environment=%s\n", unitQuote(key+"="+val))
		}
		// $$ is systemd's literal dollar, not an environment expansion.
		exe := strings.ReplaceAll(unitQuote(u.exe+".updater"), "$", "$$")
		target := strings.ReplaceAll(unitQuote(u.exe), "$", "$$")
		body := fmt.Sprintf("[Unit]\nDescription=Check for compatible remote-agent daemon updates\n\n[Service]\nType=oneshot\nExecStart=%s update --scheduled --helper-for %s\nWorkingDirectory=%s\n%sTimeoutStartSec=10min\n", exe, target, unitQuote(home), e.String())
		dir := filepath.Join(home, ".config/systemd/user")
		return map[string]string{filepath.Join(dir, "remote-agent-update.service"): body, filepath.Join(dir, "remote-agent-update.timer"): fmt.Sprintf("[Unit]\nDescription=Check for remote-agent updates daily\n\n[Timer]\nOnCalendar=*-*-* %s:00\nPersistent=true\nRandomizedDelaySec=10min\n\n[Install]\nWantedBy=timers.target\n", at)}, nil
	default:
		return nil, errors.New("automatic updates support macOS and Linux only")
	}
}

func (u *Updater) Auto(ctx context.Context, enabled bool, at string) error {
	unlock, err := u.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if err = os.MkdirAll(u.state, 0700); err != nil {
		return err
	}
	home, err := userHome()
	if err != nil {
		return err
	}
	if at == "" {
		at = "04:00"
	}
	files, err := u.autoFiles(runtime.GOOS, home, at)
	if err != nil {
		return err
	}
	if enabled {
		if _, e := os.Stat(u.exe + ".update-transaction.json"); e == nil {
			return errors.New("recover the pending update transaction before enabling automatic updates")
		} else if !os.IsNotExist(e) {
			return e
		}
		if u.current.Channel != "release" {
			return errors.New("automatic updates require an official release installation")
		}
		if _, err = versionParts(u.current.Version); err != nil {
			return errors.New("automatic updates require an installed stable release")
		}
		if err = u.service.Verify(ctx, u.exe, u.state); err != nil {
			return err
		}
		// An installer may have just restarted the service. Wait for the
		// installed version and service PID, with the same bounded health check
		// used after updates, before loading the automatic task.
		if err = u.waitHealthy(ctx, u.current, false); err != nil {
			return err
		}
		disk, e := u.probe(ctx, u.exe)
		if e != nil {
			return e
		}
		if disk.Version != u.current.Version || disk.Channel != "release" || disk.Protocol != u.current.Protocol || disk.State != u.current.State {
			return errors.New("installed binary differs from the running command")
		}
		current, err := os.ReadFile(u.exe)
		if err != nil {
			return err
		}
		if err = atomicFile(u.exe+".updater", current, 0755); err != nil {
			return err
		}
		for path, body := range files {
			if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return err
			}
			if err = atomicFile(path, []byte(body), 0600); err != nil {
				return err
			}
		}
	}
	configPath := filepath.Join(u.state, "update-auto.json")
	previous, readErr := os.ReadFile(configPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	// The scheduled command checks this flag too, so a failure to unload the
	// OS task cannot keep installing versions after the user disables updates.
	if err = writeJSON(configPath, AutoConfig{Enabled: enabled, Time: at, Executable: u.exe}); err != nil {
		return err
	}
	if runtime.GOOS == "darwin" {
		_, _ = u.autoCommand(ctx, "/bin/launchctl", "bootout", domain()+"/"+updateLabel)
		if enabled {
			_, err = u.autoCommand(ctx, "/bin/launchctl", "bootstrap", domain(), filepath.Join(home, "Library/LaunchAgents", updateLabel+".plist"))
		}
	} else {
		_, err = u.autoCommand(ctx, "systemctl", "--user", "daemon-reload")
		if err == nil {
			action := "disable"
			if enabled {
				action = "enable"
			}
			_, err = u.autoCommand(ctx, "systemctl", "--user", action, "--now", "remote-agent-update.timer")
		}
	}
	if err != nil && enabled {
		// A failed enable is not a user's choice to disable updates. Restore
		// the previous choice, or leave no file so the installer can retry.
		var restoreErr error
		if os.IsNotExist(readErr) {
			restoreErr = os.Remove(configPath)
		} else {
			restoreErr = atomicFile(configPath, previous, 0600)
		}
		return errors.Join(err, restoreErr)
	}
	if !enabled {
		for path := range files {
			if e := os.Remove(path); e != nil && !os.IsNotExist(e) {
				err = errors.Join(err, e)
			}
		}
	}
	if !enabled && runtime.GOOS == "linux" {
		_, e := u.autoCommand(ctx, "systemctl", "--user", "daemon-reload")
		err = errors.Join(err, e)
	}
	return err
}
