package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/daemon"
	"github.com/Lewin671/remote-agent/internal/i18n"
)

func runSessionModel(args []string) error {
	if len(args) == 2 {
		args = append(args, "")
	}
	if err := needArgs(args, 3, "session model ID MODEL [EFFORT]"); err != nil {
		return err
	}
	_, err := dial().SetModel(context.Background(), connect.NewRequest(&pb.SetModelRequest{SessionId: args[0], Model: args[1], Effort: args[2]}))
	if err != nil {
		if connect.CodeOf(err) == connect.CodeFailedPrecondition {
			if s, getErr := getSession(args[0]); getErr == nil && s.Agent == pb.Agent_AGENT_ANTIGRAVITY {
				return errors.New(i18n.NewSessionCliAgyModelAtLaunch(cliLocale, args[1]))
			}
		}
		return explain(err)
	}
	return nil
}

type agentModelsSummary struct {
	Agent           string           `json:"agent"`
	Name            string           `json:"name"`
	DefaultModel    string           `json:"defaultModel,omitempty"`
	MachineDefaults *machineDefaults `json:"machineDefaults,omitempty"`
	Models          []modelEntryJSON `json:"models"`
}

type machineDefaults struct {
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	Mode   string `json:"mode,omitempty"`
}

type modelEntryJSON struct {
	Slug            string   `json:"slug"`
	DisplayName     string   `json:"displayName,omitempty"`
	Efforts         []string `json:"efforts,omitempty"`
	DefaultEffort   string   `json:"defaultEffort,omitempty"`
	ContextWindow   int64    `json:"contextWindow,omitempty"`
	AutoUnavailable bool     `json:"autoUnavailable,omitempty"`
}

func runModels(args []string) error {
	if isHelpArg(args) {
		os.Stdout.WriteString(i18n.NewSessionCliModelsHelp(cliLocale, strings.Join(daemon.AgentIDs(), "|")))
		return nil
	}
	fs := flag.NewFlagSet("models", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "print models as JSON")
	var pos []string
	for len(args) > 0 {
		if strings.HasPrefix(args[0], "-") {
			if err := fs.Parse(args); err != nil {
				if errors.Is(err, flag.ErrHelp) {
					os.Stdout.WriteString(i18n.NewSessionCliModelsHelp(cliLocale, strings.Join(daemon.AgentIDs(), "|")))
					return nil
				}
				return err
			}
			args = fs.Args()
			continue
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
	if len(pos) > 1 {
		return errors.New(i18n.NewSessionCliUnexpectedArgs(cliLocale))
	}
	var specs []daemon.AgentSpec
	explicit := len(pos) == 1
	if explicit {
		spec, ok := daemon.FindAgent(pos[0])
		if !ok {
			allowed := allowedAgentNames()
			return errors.New(i18n.NewSessionCliUnknownAgent(cliLocale, allowed, pos[0]))
		}
		specs = []daemon.AgentSpec{spec}
	} else {
		specs = daemon.RegisteredAgents
	}
	c := dial()
	var st *pb.DaemonSettings
	if stResp, err := c.GetSettings(context.Background(), connect.NewRequest(&pb.GetSettingsRequest{})); err == nil {
		st = stResp.Msg.Settings
	}
	var summaries []agentModelsSummary
	for _, spec := range specs {
		if !explicit && st != nil && slices.Contains(st.DisabledAgents, spec.Kind) {
			continue
		}
		res, err := c.ListModels(context.Background(), connect.NewRequest(&pb.ListModelsRequest{Agent: spec.Kind}))
		if err != nil {
			if explicit {
				return explain(err)
			}
			continue
		}
		summaries = append(summaries, buildAgentModelsSummary(spec, res.Msg, st))
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if explicit && len(summaries) == 1 {
			return enc.Encode(summaries[0])
		}
		return enc.Encode(summaries)
	}
	return writeModelsTables(os.Stdout, summaries)
}

func buildAgentModelsSummary(spec daemon.AgentSpec, res *pb.ListModelsResponse, st *pb.DaemonSettings) agentModelsSummary {
	def := settingsDefaultsFor(st, spec.Kind)
	var mDef *machineDefaults
	if def.Model != "" || def.Effort != "" || def.Mode != pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED {
		mDef = &machineDefaults{
			Model:  def.Model,
			Effort: def.Effort,
			Mode:   approvalModeFlag(def.Mode),
		}
	}
	effDefaultModel := def.Model
	if effDefaultModel == "" {
		effDefaultModel = res.DefaultModel
	}
	entries := make([]modelEntryJSON, 0, len(res.Models))
	for _, m := range res.Models {
		entries = append(entries, modelEntryJSON{
			Slug:            m.Slug,
			DisplayName:     m.DisplayName,
			Efforts:         m.Efforts,
			DefaultEffort:   m.DefaultEffort,
			ContextWindow:   m.ContextWindow,
			AutoUnavailable: m.AutoUnavailable,
		})
	}
	return agentModelsSummary{
		Agent:           spec.ID,
		Name:            spec.Name,
		DefaultModel:    effDefaultModel,
		MachineDefaults: mDef,
		Models:          entries,
	}
}

func settingsDefaultsFor(st *pb.DaemonSettings, kind pb.Agent) *pb.AgentDefaults {
	if st == nil {
		return &pb.AgentDefaults{}
	}
	switch kind {
	case pb.Agent_AGENT_CLAUDE:
		if st.Claude != nil {
			return st.Claude
		}
	case pb.Agent_AGENT_ANTIGRAVITY:
		if st.Antigravity != nil {
			return st.Antigravity
		}
	default:
		return &pb.AgentDefaults{Model: st.DefaultModel, Effort: st.DefaultEffort, Mode: st.DefaultMode}
	}
	return &pb.AgentDefaults{}
}

func approvalModeFlag(m pb.ApprovalMode) string {
	switch m {
	case pb.ApprovalMode_APPROVAL_MODE_ASK:
		return "ask"
	case pb.ApprovalMode_APPROVAL_MODE_AUTO:
		return "auto"
	case pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS:
		return "full"
	case pb.ApprovalMode_APPROVAL_MODE_PLAN:
		return "plan"
	default:
		return ""
	}
}

func allowedAgentNames() string {
	return strings.Join(daemon.AgentIDs(), " or ")
}

func formatDefaultsHeader(defModel, defEffort, defMode string) string {
	parts := []string{"model=" + defModel, "effort=" + defEffort, "mode=" + defMode}
	return "Defaults: " + strings.Join(parts, ", ") + "\n"
}

const (
	cliDefaultModelLabel = "CLI default"
	modelsTableHeader    = "MODEL\tNAME\tEFFORTS\n"
)

func writeModelsTables(out io.Writer, summaries []agentModelsSummary) error {
	for idx, sum := range summaries {
		if idx > 0 {
			io.WriteString(out, "\n")
		}
		defModel := sum.DefaultModel
		if defModel == "" {
			defModel = cliDefaultModelLabel
		} else if sum.MachineDefaults != nil && sum.MachineDefaults.Model != "" {
			defModel += " (machine)"
		} else {
			defModel += " (CLI)"
		}
		defEffort := "model default"
		if sum.MachineDefaults != nil && sum.MachineDefaults.Effort != "" {
			defEffort = sum.MachineDefaults.Effort + " (machine)"
		}
		defMode := "follow CLI settings"
		if sum.MachineDefaults != nil && sum.MachineDefaults.Mode != "" {
			defMode = sum.MachineDefaults.Mode + " (machine)"
		}
		io.WriteString(out, "Agent: "+sum.Agent+" ("+sum.Name+")\n")
		io.WriteString(out, formatDefaultsHeader(terminalText(defModel, false), terminalText(defEffort, false), defMode))
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		io.WriteString(w, modelsTableHeader)
		for _, m := range sum.Models {
			eff := "-"
			if len(m.Efforts) > 0 {
				eff = strings.Join(m.Efforts, ",")
			}
			marker := ""
			if m.Slug == sum.DefaultModel {
				marker = " (default)"
			}
			display := m.DisplayName
			if display == "" {
				display = "-"
			}
			io.WriteString(w, terminalText(m.Slug, false)+"\t"+terminalText(display, false)+"\t"+terminalText(eff, false)+marker+"\n")
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	return nil
}

var modeNames = map[string]pb.ApprovalMode{
	"ask": pb.ApprovalMode_APPROVAL_MODE_ASK, "auto": pb.ApprovalMode_APPROVAL_MODE_AUTO,
	"full": pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS, "plan": pb.ApprovalMode_APPROVAL_MODE_PLAN,
}

func runMode(args []string) error {
	if err := needArgs(args, 2, "session mode ID ask|auto|full|plan"); err != nil {
		return err
	}
	mode, ok := modeNames[args[1]]
	if !ok {
		return fmt.Errorf("mode must be ask, auto, full or plan")
	}
	_, err := dial().SetApprovalMode(context.Background(), connect.NewRequest(&pb.SetApprovalModeRequest{SessionId: args[0], Mode: mode}))
	return explain(err)
}

func runRename(args []string) error {
	if err := needArgs(args, 1, "session rename ID [TITLE]"); err != nil {
		return err
	}
	_, err := dial().RenameSession(context.Background(), connect.NewRequest(&pb.RenameSessionRequest{SessionId: args[0], Title: strings.Join(args[1:], " ")}))
	return explain(err)
}

func runCompact(args []string) error {
	if err := needArgs(args, 1, "session compact ID"); err != nil {
		return err
	}
	_, err := dial().Compact(context.Background(), connect.NewRequest(&pb.CompactRequest{SessionId: args[0]}))
	return explain(err)
}

var planChoices = map[string]pb.PlanChoice{
	"implement": pb.PlanChoice_PLAN_CHOICE_IMPLEMENT, "clear": pb.PlanChoice_PLAN_CHOICE_CLEAR_AND_IMPLEMENT,
	"keep": pb.PlanChoice_PLAN_CHOICE_KEEP_PLANNING,
}

func runPlan(args []string) error {
	if err := needArgs(args, 2, "session plan ID implement|clear|keep"); err != nil {
		return err
	}
	choice, ok := planChoices[args[1]]
	if !ok {
		return fmt.Errorf("answer must be implement, clear or keep")
	}
	_, err := dial().AnswerPlan(context.Background(), connect.NewRequest(&pb.AnswerPlanRequest{SessionId: args[0], Choice: choice}))
	return explain(err)
}

func runFork(args []string) error {
	if err := needArgs(args, 1, "session fork ID"); err != nil {
		return err
	}
	res, err := dial().ForkSession(context.Background(), connect.NewRequest(&pb.ForkSessionRequest{SessionId: args[0]}))
	if err != nil {
		return explain(err)
	}
	id := res.Msg.Session.Id
	os.Stdout.WriteString("forked into " + id + "; attach with `remote-agent session attach " + id[:8] + "`\n")
	return nil
}

func runResume(args []string) error {
	if err := needArgs(args, 1, "session resume ID"); err != nil {
		return err
	}
	res, err := dial().ResumeSession(context.Background(), connect.NewRequest(&pb.ResumeSessionRequest{SessionId: args[0]}))
	if err != nil {
		return explain(err)
	}
	id := res.Msg.Session.Id
	os.Stdout.WriteString("resumed " + id + "; attach with `remote-agent session attach " + id[:8] + "`\n")
	return nil
}
