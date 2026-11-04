// Package antigravity adapts the native agy TUI, hooks and transcript.
package antigravity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

// TestedVersions lists the Antigravity CLI versions verified against this adapter.
var TestedVersions = []string{"1.4.12", "1.5.0"}

// Adapter is Antigravity's agent.Adapter implementation.
type Adapter struct {
	mu         sync.Mutex // guards mutable fields
	bin        string
	commandEnv []string
	home       string
	history    string

	version agent.BinVersion
	models  *ModelCache

	account  string
	planTier string
}

var (
	_ agent.Checkable         = (*Adapter)(nil)
	_ agent.StatusLineAccount = (*Adapter)(nil)
)

// New returns the adapter for the agy executable. home is its state directory (~/.gemini/antigravity-cli).
func New(bin, home string) *Adapter {
	if home == "" {
		if userHome, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(userHome, ".gemini", "antigravity-cli")
		}
	}
	history := filepath.Join(home, "history.jsonl")
	return &Adapter{
		bin:     bin,
		home:    home,
		history: history,
		models:  NewModelCache(10 * time.Minute),
	}
}

func (a *Adapter) ID() string     { return "agy" }
func (a *Adapter) Kind() pb.Agent { return pb.Agent_AGENT_ANTIGRAVITY }
func (a *Adapter) Name() string   { return "Antigravity" }

func (a *Adapter) Bin() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.bin
}

func (a *Adapter) SetBin(bin string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.bin = bin
}

func (a *Adapter) Version() string {
	return a.version.Get(a.Bin(), func() string {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, a.Bin(), "--version")
		cmd.Env = a.execEnv()
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		s := strings.TrimSpace(string(out))
		s = strings.TrimPrefix(s, "Antigravity CLI ")
		return strings.TrimSpace(s)
	})
}

func (a *Adapter) Tested(version string) bool {
	return slices.Contains(TestedVersions, version)
}

func (a *Adapter) Models() (*pb.ListModelsResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	defaultModel := a.readDefaultModel()
	return a.models.Get(ctx, a.Bin(), defaultModel, a.execEnv())
}

func (a *Adapter) CheckEffort(effort string) error {
	return CheckEffort(effort)
}

func (a *Adapter) StandardWindow() int64 {
	return 0
}

func (a *Adapter) Env(standardContext bool) map[string]string {
	return nil
}

func (a *Adapter) FileRef(path string) string {
	return "File: " + path
}

func (a *Adapter) ParseFileRef(line string) (string, bool) {
	if path, ok := strings.CutPrefix(line, "File: "); ok && path != "" {
		return path, true
	}
	return "", false
}

func (a *Adapter) ApprovalsByInput() bool {
	return true
}

func (a *Adapter) HoldsInputMidTurn() bool {
	return true
}

func (a *Adapter) Capabilities() agent.Capabilities {
	return agent.Capabilities{NativeApproval: true, ConversationIdentity: true}
}
func (a *Adapter) ExactScheduleMode(info *pb.Session) string {
	if info.Mode == pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED {
		return ""
	}
	if info.Sandbox != "disabled" {
		return ""
	}
	if info.ApprovalPolicy == "always-proceed" && info.PermissionMode == "default" {
		return "full"
	}
	if info.ApprovalPolicy != "request-review" {
		return ""
	}
	switch info.PermissionMode {
	case "default":
		return "ask"
	case "auto":
		return "auto"
	}
	return ""
}

// SetAccountInfo updates the account email and plan tier observed from statusline.
func (a *Adapter) SetAccountInfo(email, planTier string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if email != "" {
		a.account = EmailFingerprint(email)
	}
	if planTier != "" {
		a.planTier = planTier
	}
}

// Account returns the email fingerprint, plan tier and subscription flag.
func (a *Adapter) Account() (fingerprint, plan string, subscription bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.account == "" {
		return "", "", false
	}
	return a.account, a.planTier, true
}

// EmailFingerprint computes a 12-hex-character SHA-256 fingerprint of the email address.
func EmailFingerprint(email string) string {
	if email == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("remote-agent/quota-account\x00" + strings.TrimSpace(email)))
	return hex.EncodeToString(sum[:6])
}

// DoctorModels returns low-cost test model and effort settings.
func (a *Adapter) DoctorModels() (string, string, string, string) {
	return "gemini-3.8-flash", "low", "", ""
}

// DoctorModes returns the approval modes doctor cycles through.
func (a *Adapter) DoctorModes() []pb.ApprovalMode {
	return []pb.ApprovalMode{pb.ApprovalMode_APPROVAL_MODE_PLAN, pb.ApprovalMode_APPROVAL_MODE_ASK}
}

// StartupKeys handles startup trust dialogs.
func (a *Adapter) StartupKeys(popup string) []string {
	// Settings-based trust must already cover this directory. Never persist trust.
	return nil
}

func (a *Adapter) DoctorDescription() string {
	return "Antigravity models and cycling modes"
}

func (a *Adapter) PromptMarks() string {
	return ">"
}

func (a *Adapter) readDefaultModel() string {
	path := filepath.Join(a.home, "settings.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var st struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(b, &st) == nil {
		return st.Model
	}
	return ""
}

// SetCommandEnv applies agent.env to metadata subprocesses too: agy models
// contacts the CLI's service and needs the same proxy configuration as sessions.
func (a *Adapter) SetCommandEnv(overlay map[string]string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(overlay) == 0 {
		a.commandEnv = nil
		return
	}
	a.commandEnv = os.Environ()
	for k, v := range overlay {
		a.commandEnv = append(a.commandEnv, k+"="+v)
	}
}
func (a *Adapter) execEnv() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.commandEnv)
}
