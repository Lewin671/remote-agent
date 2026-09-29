package main

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func runModel(args []string) error {
	if len(args) == 0 {
		res, err := dial().ListModels(context.Background(), connect.NewRequest(&pb.ListModelsRequest{}))
		if err != nil {
			return explain(err)
		}
		for _, m := range res.Msg.Models {
			def := ""
			if m.Slug == res.Msg.DefaultModel {
				def = " (default)"
			}
			fmt.Printf("%-16s %-14s %s%s\n", m.Slug, m.DisplayName, strings.Join(m.Efforts, ","), def)
		}
		return nil
	}
	if err := needArgs(args, 3, "model [<id> <model> <effort>]"); err != nil {
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
