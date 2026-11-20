package screens

import (
	"strings"
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/world"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

func baseEligibleQuestion() *lists.PendingQuestionData {
	return &lists.PendingQuestionData{
		QuestionID:       "q1",
		QuestionKey:      "mode",
		Text:             "这次怎么处理回归测试？",
		QuestionCount:    1,
		MultiSelect:      false,
		CanFreeText:      true, // Codex supports free text in terminal; still eligible with terminal fallback link
		ObservedEventSeq: 42,
		AskedAtMs:        1_700_000_000_000,
		Options: []lists.PendingQuestionOption{
			{Label: "先跑单元测试 (Recommended)", Description: "只跑 affected packages"},
			{Label: "跑全套集成测试"},
		},
	}
}

func TestIsHomeInlineQuestionEligibleCriteria(t *testing.T) {
	if !IsHomeInlineQuestionEligible(baseEligibleQuestion()) {
		t.Fatalf("expected Codex 1-question single-select with 2 short options to be eligible")
	}

	cases := []struct {
		name   string
		mutate func(*lists.PendingQuestionData)
	}{
		{"nil", nil},
		{"async", func(q *lists.PendingQuestionData) { q.Async = true }},
		{"multi_question", func(q *lists.PendingQuestionData) { q.QuestionCount = 2 }},
		{"multi_select", func(q *lists.PendingQuestionData) { q.MultiSelect = true }},
		{"zero_options", func(q *lists.PendingQuestionData) { q.Options = nil }},
		{"four_options", func(q *lists.PendingQuestionData) {
			q.Options = []lists.PendingQuestionOption{{Label: "1"}, {Label: "2"}, {Label: "3"}, {Label: "4"}}
		}},
		{"long_stem", func(q *lists.PendingQuestionData) { q.Text = strings.Repeat("a", 91) }},
		{"code_block", func(q *lists.PendingQuestionData) { q.Text = "确认：\n```go\nfmt.Println(1)\n```" }},
		{"markdown_table", func(q *lists.PendingQuestionData) { q.Text = "| A | B |\n|---|---|\n| 1 | 2 |" }},
		{"long_option", func(q *lists.PendingQuestionData) {
			q.Options[0].Label = strings.Repeat("x", 37)
		}},
	}
	for _, tc := range cases {
		var q *lists.PendingQuestionData
		if tc.mutate != nil {
			q = baseEligibleQuestion()
			tc.mutate(q)
		}
		if IsHomeInlineQuestionEligible(q) {
			t.Fatalf("%s: expected ineligible", tc.name)
		}
	}
}

func TestBuildHomeTopAttentionQuestion(t *testing.T) {
	reachable := true
	st := &State{State: &world.State{
		Cal: lists.Calendar{NowMs: 1_700_000_060_000},
		World: lists.World{
			Machines: []*lists.Machine{
				{ID: "m1", Online: true, Reachable: &reachable, Meta: &v1.MachineMeta{Hostname: "MacBook"}},
			},
			Sessions: []*lists.Session{
				{
					ID:        "s1",
					MachineID: "m1",
					UpdatedMs: 1_700_000_000_000,
					Info: &v1.Session{
						Id:               "s1",
						Title:            "修复构建流程",
						Cwd:              "/Users/me/Code/app",
						Agent:            v1.Agent_AGENT_CODEX,
						Status:           v1.SessionStatus_SESSION_STATUS_RUNNING,
						PendingQuestions: 1,
					},
					Pending: lists.PendingAction{
						HasQuestion: true,
						Question:    baseEligibleQuestion(),
					},
				},
			},
		},
	}}

	vm := BuildHome(st, 10)
	if !vm.TopAttentionActionable || vm.TopAttentionCard != nil || vm.TopAttentionQuestion == nil {
		t.Fatalf("expected TopAttentionQuestion only, got actionable=%v card=%v question=%v",
			vm.TopAttentionActionable, vm.TopAttentionCard != nil, vm.TopAttentionQuestion)
	}
	q := vm.TopAttentionQuestion
	if q.SessionId != "s1" || q.QuestionId != "q1" || q.ObservedEventSeq != 42 || !q.CanAnswer {
		t.Fatalf("unexpected HomeQuestionCardView metadata: %+v", q)
	}
	if len(q.Options) != 2 || q.Options[0].Label != "先跑单元测试" || !q.Options[0].Recommended {
		t.Fatalf("expected cleaned option labels with recommended flag, got %+v", q.Options)
	}
	if len(vm.Attention) != 1 || vm.Attention[0].KindText.Key != "list_state.agent_asking" {
		t.Fatalf("unexpected Attention rows: %+v", vm.Attention)
	}

	// Disconnected session disables direct answer buttons while keeping card visible
	unlinked := true
	st.World.Unlinked = &unlinked
	vmOffline := BuildHome(st, 10)
	if vmOffline.TopAttentionQuestion == nil || vmOffline.TopAttentionQuestion.CanAnswer || vmOffline.TopAttentionQuestion.OfflineNote == nil {
		t.Fatalf("expected CanAnswer=false with OfflineNote when unlinked, got %+v", vmOffline.TopAttentionQuestion)
	}

	// Ensure BuildHome does not emit Question card if >3 options (even when top attention)
	st.World.Unlinked = nil
	st.World.Sessions[0].Pending.Question.Options = []lists.PendingQuestionOption{{Label: "1"}, {Label: "2"}, {Label: "3"}, {Label: "4"}}
	vmLong := BuildHome(st, 10)
	if vmLong.TopAttentionQuestion != nil || vmLong.TopAttentionActionable {
		t.Fatalf("expected 4-option question to fall back to flat attention row, got %+v", vmLong.TopAttentionQuestion)
	}
	if vmLong.TopAttentionSessionId != "s1" || len(vmLong.Attention) != 1 {
		t.Fatalf("expected s1 in flat Attention list, got %+v", vmLong.Attention)
	}
}

func TestIsHalfWidthAccounting(t *testing.T) {
	if IsHomeInlineQuestionEligible(&lists.PendingQuestionData{
		QuestionID:    "q-cjk",
		QuestionCount: 1,
		Text:          strings.Repeat("字", 45), // 45 CJK chars = 90 half-width
		Options:       []lists.PendingQuestionOption{{Label: strings.Repeat("选", 18)}},
	}) != true {
		t.Fatalf("expected 45 CJK stem + 18 CJK option to pass")
	}
	_ = corev1.HeadlineKind_HEADLINE_KIND_WAITING
}
