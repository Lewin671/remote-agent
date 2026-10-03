package main

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func runModel(args []string) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "codex" || args[0] == "claude")) {
		kind := pb.Agent_AGENT_CODEX
		if len(args) == 1 && args[0] == "claude" {
			kind = pb.Agent_AGENT_CLAUDE
		}
		res, err := dial().ListModels(context.Background(), connect.NewRequest(&pb.ListModelsRequest{Agent: kind}))
		if err != nil {
			return explain(err)
		}
		for _, m := range res.Msg.Models {
			def := ""
			if m.Slug == res.Msg.DefaultModel {
				def = " (default)"
			}
			fmt.Printf("%-16s %-14s %s%s\n", terminalText(m.Slug, false), terminalText(m.DisplayName, false), terminalText(strings.Join(m.Efforts, ","), false), def)
		}
		return nil
	}
	if len(args) == 2 {
		args = append(args, "") // Claude Code models without effort levels
	}
	if err := needArgs(args, 3, "model [codex|claude] | model <id> <model> [<effort>]"); err != nil {
		return err
	}
	_, err := dial().SetModel(context.Background(), connect.NewRequest(&pb.SetModelRequest{SessionId: args[0], Model: args[1], Effort: args[2]}))
	return explain(err)
}

var modeNames = map[string]pb.ApprovalMode{
	"ask": pb.ApprovalMode_APPROVAL_MODE_ASK, "auto": pb.ApprovalMode_APPROVAL_MODE_AUTO,
	"full": pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS, "plan": pb.ApprovalMode_APPROVAL_MODE_PLAN,
}

func runMode(args []string) error {
	if err := needArgs(args, 2, "mode <id> ask|auto|full|plan"); err != nil {
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
	if err := needArgs(args, 1, "rename <id> [<title>]"); err != nil {
		return err
	}
	_, err := dial().RenameSession(context.Background(), connect.NewRequest(&pb.RenameSessionRequest{SessionId: args[0], Title: strings.Join(args[1:], " ")}))
	return explain(err)
}

func runCompact(args []string) error {
	if err := needArgs(args, 1, "compact <id>"); err != nil {
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
	if err := needArgs(args, 2, "plan <id> implement|clear|keep"); err != nil {
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
	if err := needArgs(args, 1, "fork <id>"); err != nil {
		return err
	}
	res, err := dial().ForkSession(context.Background(), connect.NewRequest(&pb.ForkSessionRequest{SessionId: args[0]}))
	if err != nil {
		return explain(err)
	}
	fmt.Printf("forked into %s; attach with `remote-agent attach %s`\n", res.Msg.Session.Id, res.Msg.Session.Id[:8])
	return nil
}

func runResume(args []string) error {
	if err := needArgs(args, 1, "resume <id>"); err != nil {
		return err
	}
	res, err := dial().ResumeSession(context.Background(), connect.NewRequest(&pb.ResumeSessionRequest{SessionId: args[0]}))
	if err != nil {
		return explain(err)
	}
	fmt.Printf("resumed %s; attach with `remote-agent attach %s`\n", res.Msg.Session.Id, res.Msg.Session.Id[:8])
	return nil
}
