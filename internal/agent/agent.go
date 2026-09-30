// Package agent defines what the daemon needs from each AI coding CLI it
// runs, so that the daemon itself knows nothing about a CLI's screen,
// menus, arguments or file formats. Each CLI has its own package (codex,
// claude) implementing Adapter.
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// Errors an adapter returns for conditions the caller can fix by waiting or
// by changing its request.
var (
	ErrBusy        = errors.New("the CLI is busy; try again when the turn has ended")
	ErrNotReady    = errors.New("the input box is not ready: a popup is open or the terminal holds an unsent draft")
	ErrNoPlan      = errors.New("the CLI is not asking about a plan")
	ErrUnsupported = errors.New("this CLI does not support that")
	ErrNotFound    = errors.New("not found")
	ErrBackground  = errors.New("background work is running")
)

// HookTimeoutSeconds is the timeout the CLIs give the PermissionRequest
// hook. Approvals must be decided before it, because a hook that times out
// leaves the decision to the terminal. Changing it changes Codex's hook
// definition, which makes Codex ask to trust it again.
const HookTimeoutSeconds = 3600

// Adapter is one CLI: how to start it, where its output comes from, and how
// to operate its terminal.
type Adapter interface {
	Launcher
	Source
	Controller
}

// Launcher starts and resumes the CLI.
type Launcher interface {
	// Kind is the CLI's protocol value.
	Kind() pb.Agent
	// Name is the CLI's name for messages, e.g. "Codex".
	Name() string
	// Bin is the CLI's executable.
	Bin() string
	// Version is the installed CLI's version, read once; empty if unknown.
	Version() string
	// Tested reports whether the adapter was checked against version.
	Tested(version string) bool
	// Models lists the models a session can switch to.
	Models() (*pb.ListModelsResponse, error)
	// CheckEffort rejects an effort level the CLI does not know.
	CheckEffort(effort string) error
	// Options turns the options chosen for a new session into CLI
	// arguments, which the session keeps for resuming.
	Options(extra []string, model, effort string, mode pb.ApprovalMode) ([]string, error)
	// FullAccess reports whether a session started with args can be
	// switched to FULL_ACCESS.
	FullAccess(args []string) bool
	// DefaultMode is the approval mode the CLI's own configuration gives a
	// session started in cwd without one ("" for the machine-wide
	// default). known is false when the configuration could not be read;
	// varies, when settings in a session's folder may change it.
	DefaultMode(cwd string) (mode pb.ApprovalMode, known, varies bool)
	// Argv is the command line after the executable that starts or resumes
	// a session. It may write files the CLI reads into l.Dir and set
	// l.SessionID when the adapter chooses the CLI's session ID.
	Argv(l *Launch) ([]string, error)
}

// Launch describes a session being started or resumed.
type Launch struct {
	// Dir is the session's private state directory.
	Dir string
	// Args are the arguments kept for the session.
	Args []string
	// SessionID is the CLI's own session ID, if known.
	SessionID string
	// Transcript is where the CLI writes the session, if known.
	Transcript string
	// Resume starts the CLI again for a session that ran before.
	Resume bool
	// Context holds the settings the session last ran with, when resuming.
	Context *pb.TurnContext
	// FullAccess: the session could be switched to full access when it was
	// started (Launcher.FullAccess then), for resuming it alike.
	FullAccess bool
}

// Source reads what the CLI records.
type Source interface {
	// NewParser returns a parser for the session's transcript.
	NewParser() Parser
	// SubmitLog is the file where the CLI records submitted input, given
	// the session's transcript path (possibly empty).
	SubmitLog(transcript string) string
	// Submitted reports whether a line of the submit log records, in the
	// CLI session cliSessionID (empty if unknown), submitted text that
	// match accepts. The text is as the CLI recorded it, with its image
	// placeholders.
	Submitted(line []byte, match func(recorded string) bool, cliSessionID string) bool
	// HookEvents are events a hook call shows before the transcript does;
	// the parser is told about them and does not repeat them.
	HookEvents(h *Hook) []*pb.Event
	// ForwardPermission reports whether a PermissionRequest hook call
	// should wait for a client's decision. If not, the hook stays silent
	// and the CLI decides on its own.
	ForwardPermission(h *Hook, ctx *pb.TurnContext) bool
	// ApplySettings updates the session's settings after a change made
	// through remote-agent, before the CLI records it.
	ApplySettings(ctx *pb.TurnContext, ch *pb.SettingsChanged) *pb.TurnContext
	// Mode is the approval mode the settings amount to; UNSPECIFIED when
	// they match none of the four.
	Mode(ctx *pb.TurnContext) pb.ApprovalMode
}

// Parser turns transcript lines into unified events.
type Parser interface {
	// Parse converts one line into zero or more events without seq and
	// session ID.
	Parse(line []byte) ([]*pb.Event, error)
	// Replay feeds an event logged earlier, so that a parser created after
	// a restart knows what it needs about the past.
	Replay(e *pb.Event)
	// Reported tells whether an event a hook reports (Source.HookEvents)
	// was already parsed from the transcript.
	Reported(e *pb.Event) bool
	// Images returns the images found by the last Parse, each tied to an
	// ImageRef in the events it returned, and forgets them. The daemon
	// fills in the refs (ID, preview, sizes) before logging the events.
	Images() []FoundImage
}

// FoundImage is an image in a transcript line.
type FoundImage struct {
	// Ref is in one of the parsed events. The parser sets what the
	// record says: source, index, path, original size.
	Ref *pb.ImageRef
	// Data is the image as recorded: base64, possibly as a data: URL, or
	// a tail.BlobPrefix marker for a string left in the file.
	Data string
}

// Controller operates the CLI's terminal. Methods that take a Term run
// while the daemon holds the terminal for them: local keys are held back
// and the status check is paused.
type Controller interface {
	// Analyze reads a capture made with escape sequences kept.
	Analyze(styled string) Screen
	// Paste is what to write to submit text.
	Paste(text string) []byte
	// ComposerText is the whole text in the input box, as shown, lines
	// joined with "\n", "" when it is empty. footer is the status line
	// Screen reported while the input box was empty, for CLIs that need it
	// to find where the box ends. ok is false when the box does not show or
	// cannot be read whole: callers must then not act on its content.
	// Placeholders such as "[Image #1]" are part of it.
	ComposerText(styled, footer string) (text string, ok bool)
	// ClearInput is what to write to empty an input box that holds one
	// line. It must be harmless in an empty box and never interrupt a
	// running turn or quit the CLI (Ctrl-C and Esc do).
	ClearInput() []byte
	// Interrupt is what to write to interrupt a running turn.
	Interrupt() []byte
	// Answer picks options of the question showing. index, if set, is the
	// question of the call being answered; options are 1-based.
	Answer(t Term, q *pb.QuestionAsked, index *int32, options []int32) error
	// SetModel switches the model and effort for this session only.
	SetModel(t Term, model, effort string) error
	// SetMode switches the approval mode. running tells whether a turn is
	// in progress.
	SetMode(t Term, mode pb.ApprovalMode, running bool, facts ModeFacts) error
	// Compact starts compacting the context; started reports whether the
	// CLI has recorded a turn start meanwhile.
	Compact(t Term, started func() bool) error
	// AnswerPlan answers the CLI's prompt about a proposed plan.
	AnswerPlan(t Term, choice pb.PlanChoice) error
}

// ModeFacts is what the daemon knows about a session that tells which
// modes its CLI offers.
type ModeFacts struct {
	// FullAccess: the session could switch to full access at launch.
	FullAccess bool
	// Model is the session's model as last recorded.
	Model string
	// Seen are the modes the screen has shown in this session while it
	// used Model (or another model of its family).
	Seen []string
}

// ModelFamily groups a model's names: a Claude Code alias and its full
// IDs ("sonnet", "claude-sonnet-5-5") share one; other names are their own.
func ModelFamily(model string) string {
	m := strings.ToLower(model)
	for _, f := range []string{"haiku", "sonnet", "opus", "fable"} {
		if strings.Contains(m, f) {
			return f
		}
	}
	return m
}

// Screen is what the daemon needs to know from the terminal.
type Screen struct {
	// ComposerReady: the input box is showing and no popup covers it.
	ComposerReady bool
	// DraftEmpty: the input box holds no unsent text.
	DraftEmpty bool
	// Popup names the dialog in front of the input box, if any.
	Popup string
	// QuestionOpen: the model's question is waiting for an answer.
	QuestionOpen bool
	// PlanOpen: the CLI asks whether to carry out the plan it proposed.
	PlanOpen bool
	// PermissionOpen: the CLI's own permission prompt is showing.
	PermissionOpen bool
	// Footer is the status line under the input box, for diagnostics.
	Footer string
	// Mode is the permission mode the screen shows, in the CLI's own
	// words (Claude Code: "default", "acceptEdits", ...); empty if none.
	Mode string
	// Busy: the screen shows the CLI working (Claude Code's spinner line
	// or its dimmed prompt), whatever the transcript says yet.
	Busy bool
}

// Hook is a hook call's payload. Codex and Claude Code use the same field
// names for what remote-agent reads.
type Hook struct {
	SessionID      string          `json:"session_id"`
	TurnID         string          `json:"turn_id"`
	PromptID       string          `json:"prompt_id"` // Claude Code's turn
	TranscriptPath string          `json:"transcript_path"`
	Cwd            string          `json:"cwd"`
	HookEventName  string          `json:"hook_event_name"`
	Model          string          `json:"model"`
	PermissionMode string          `json:"permission_mode"`
	Source         string          `json:"source"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolUseID      string          `json:"tool_use_id"`
}

// InputDigest identifies a Claude Code tool call by its complete input:
// hex SHA-256 of a shell call's command, else of the input as compact
// JSON with sorted keys. Empty if the input cannot be read.
func InputDigest(tool string, raw []byte) string {
	var text []byte
	if tool == "Bash" {
		var in ShellInput
		if json.Unmarshal(raw, &in) != nil {
			return ""
		}
		text = []byte(in.Command)
	} else {
		var v any
		if json.Unmarshal(raw, &v) != nil {
			return ""
		}
		text, _ = json.Marshal(v)
	}
	sum := sha256.Sum256(text)
	return hex.EncodeToString(sum[:])
}

// ShellInput is the tool_input of a shell PermissionRequest.
type ShellInput struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// PermissionDecision is the PermissionRequest hook output that allows or
// denies a request; Codex and Claude Code read the same format.
func PermissionDecision(allow bool, message string) []byte {
	type decision struct {
		Behavior string `json:"behavior"`
		Message  string `json:"message,omitempty"`
	}
	behavior := "deny"
	if allow {
		behavior = "allow"
	}
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName": "PermissionRequest",
			"decision":      decision{Behavior: behavior, Message: message},
		},
	}
	b, _ := json.Marshal(out)
	return b
}

// PasteBytes wraps text in a bracketed paste followed by Enter, the way a
// terminal delivers pasted text and a key press. Control characters other
// than newline and tab are removed so the text cannot end the paste early
// or send keys of its own.
//
// With no text left (a message of images only) it is Enter alone: Claude
// Code does not submit after an empty paste.
func PasteBytes(text string) []byte {
	var body strings.Builder
	for _, r := range strings.ReplaceAll(text, "\r\n", "\n") {
		if r == utf8.RuneError {
			continue
		}
		if r == '\n' || r == '\t' || (r >= 0x20 && r != 0x7f && !(r >= 0x80 && r < 0xa0)) {
			body.WriteRune(r)
		}
	}
	if body.Len() == 0 {
		return []byte("\r")
	}
	return []byte("\x1b[200~" + body.String() + "\x1b[201~\r")
}

// Term is a session's terminal while the daemon holds it for an adapter.
type Term interface {
	// Capture returns the screen with escape sequences kept.
	Capture() (string, error)
	// Write sends keys.
	Write(b []byte) error
}

// Keys shared by the CLIs.
const (
	KeyUp    = "\x1b[A"
	KeyDown  = "\x1b[B"
	KeyRight = "\x1b[C"
	KeyLeft  = "\x1b[D"
	KeyEsc   = "\x1b"
	KeyEnter = "\r"
	KeyBTab  = "\x1b[Z"
)

// MenuWait bounds how long a step waits for its screen.
const MenuWait = 4 * time.Second

// WaitFor polls the screen until ok accepts it and returns that capture.
func WaitFor(t Term, what string, ok func(styled string) bool) (string, error) {
	deadline := time.Now().Add(MenuWait)
	for {
		styled, err := t.Capture()
		if err == nil && ok(styled) {
			return styled, nil
		}
		if time.Now().After(deadline) {
			return styled, errors.New("timed out waiting for " + what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Slash types a slash command and presses Enter.
func Slash(t Term, cmd string) error {
	if err := t.Write([]byte(cmd)); err != nil {
		return err
	}
	time.Sleep(150 * time.Millisecond) // let the command popup catch up
	return t.Write([]byte(KeyEnter))
}

// Checkable is implemented by adapters that `remote-agent doctor` can
// check against the installed CLI.
type Checkable interface {
	// DoctorModels are a cheap model and effort to run the check with,
	// and another to switch to and back.
	DoctorModels() (model, effort, other, otherEffort string)
	// DoctorModes are the approval modes to switch through, ending in the
	// one the check started in.
	DoctorModes() []pb.ApprovalMode
	// StartupKeys are the tmux key names a local user presses to get past
	// a startup popup; nil if there is nothing to press.
	StartupKeys(popup string) []string
}

// EventImages lists the image refs of an event.
func EventImages(e *pb.Event) []*pb.ImageRef {
	switch k := e.Kind.(type) {
	case *pb.Event_UserMessage:
		return k.UserMessage.Images
	case *pb.Event_ToolCall:
		return k.ToolCall.Images
	case *pb.Event_ImageGenerated:
		return k.ImageGenerated.Images
	}
	return nil
}

type originKey struct{}

// WithOrigin returns ctx carrying who asked for a command, for the daemon
// to record in the events the command causes.
func WithOrigin(ctx context.Context, o *pb.Origin) context.Context {
	return context.WithValue(ctx, originKey{}, o)
}

// OriginFrom returns the origin WithOrigin put in ctx, or nil.
func OriginFrom(ctx context.Context) *pb.Origin {
	o, _ := ctx.Value(originKey{}).(*pb.Origin)
	return o
}

// PastePath is a bracketed paste of a file path alone, without Enter: how
// both CLIs take an image file into the input box.
func PastePath(path string) []byte {
	return []byte("\x1b[200~" + path + "\x1b[201~")
}

// KeyClearLine is Ctrl-U, which both CLIs take as deleting the input box's
// line (codex-notes §10, claude-code-notes §10).
const KeyClearLine = "\x15"

// imagePlaceholderRE matches the placeholders the CLIs show for images.
var imagePlaceholderRE = regexp.MustCompile(`\[Image #(\d+)\]`)

// ImagePlaceholders returns the N of each "[Image #N]" in text, in order.
func ImagePlaceholders(text string) []int32 {
	var out []int32
	for _, m := range imagePlaceholderRE.FindAllStringSubmatch(text, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil {
			out = append(out, int32(n))
		}
	}
	return out
}

// StripPlaceholders removes one "[Image #N]" for each of indexes from
// text, and reports whether all were there. Other text, including
// placeholders the user wrote, stays.
func StripPlaceholders(text string, indexes []int32) (string, bool) {
	for _, n := range indexes {
		tok := "[Image #" + strconv.Itoa(int(n)) + "]"
		i := strings.Index(text, tok)
		if i < 0 {
			return text, false
		}
		text = text[:i] + " " + text[i+len(tok):]
	}
	return text, true
}
