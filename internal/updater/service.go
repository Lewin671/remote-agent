package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const daemonLabel = "com.github.lewin671.remote-agent"
const updateLabel = daemonLabel + ".update"

type systemService struct{}

func command(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s failed: %w: %s", filepath.Base(name), err, strings.TrimSpace(string(b)))
	}
	return strings.TrimSpace(string(b)), nil
}

func userHome() (string, error) { return os.UserHomeDir() }
func domain() string            { return "gui/" + strconv.Itoa(os.Getuid()) }

func (systemService) Verify(ctx context.Context, exe, state string) error {
	home, err := userHome()
	if err != nil {
		return err
	}
	switch runtime.GOOS {
	case "darwin":
		unit := filepath.Join(home, "Library/LaunchAgents", daemonLabel+".plist")
		p, err := command(ctx, "/usr/bin/plutil", "-extract", "ProgramArguments.0", "raw", "-o", "-", unit)
		if err != nil || p != exe {
			return errors.New("updates require the standard daemon service running this binary; reinstall remote-agent-service")
		}
		arg, err := command(ctx, "/usr/bin/plutil", "-extract", "ProgramArguments.1", "raw", "-o", "-", unit)
		if err != nil || arg != "daemon" {
			return errors.New("unexpected daemon service arguments")
		}
		loaded, err := command(ctx, "/bin/launchctl", "print", domain()+"/"+daemonLabel)
		if err != nil {
			return err
		}
		match := regexp.MustCompile(`(?m)^\s*program = (.+)$`).FindStringSubmatch(loaded)
		if len(match) != 2 || match[1] != exe {
			return errors.New("loaded daemon service points at another binary")
		}
		return nil
	case "linux":
		value, err := command(ctx, "systemctl", "--user", "show", "remote-agent.service", "--property=ExecStart", "--value")
		if err != nil {
			return err
		}
		match := regexp.MustCompile(`^\{ path=(.*?) ; argv\[\]=(.*?) ;`).FindStringSubmatch(value)
		if len(match) != 3 || match[1] != exe || match[2] != exe+" daemon" {
			return errors.New("updates require the standard daemon service running this binary; reinstall remote-agent-service")
		}
		return nil
	default:
		return errors.New("daemon updates support macOS and Linux only")
	}
}

func (systemService) Restart(ctx context.Context) error {
	if runtime.GOOS == "darwin" {
		_, err := command(ctx, "/bin/launchctl", "kickstart", "-k", domain()+"/"+daemonLabel)
		return err
	}
	_, err := command(ctx, "systemctl", "--user", "restart", "remote-agent.service")
	return err
}

func (systemService) PID(ctx context.Context) (int, error) {
	var value string
	var err error
	if runtime.GOOS == "darwin" {
		value, err = command(ctx, "/bin/launchctl", "print", domain()+"/"+daemonLabel)
		if err != nil {
			return 0, err
		}
		match := regexp.MustCompile(`(?m)^\s*pid = ([0-9]+)$`).FindStringSubmatch(value)
		if len(match) != 2 {
			return 0, errors.New("daemon service is not running")
		}
		value = match[1]
	} else {
		value, err = command(ctx, "systemctl", "--user", "show", "remote-agent.service", "--property=MainPID", "--value")
		if err != nil {
			return 0, err
		}
	}
	pid, err := strconv.Atoi(value)
	if err != nil || pid < 1 {
		return 0, errors.New("daemon service is not running")
	}
	return pid, nil
}
