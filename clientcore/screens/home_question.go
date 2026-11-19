package screens

import (
	"strings"
	"unicode"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

const (
	homeQuestionMaxTextWidth   = 90 // <= 3 lines at ~30 half-width chars per line
	homeQuestionLineWidth      = 30
	homeQuestionMaxOptionWidth = 36 // <= 1 line per option title
	homeQuestionMaxDescWidth   = 72 // <= 2 lines per optional option description
)

// IsHomeInlineQuestionEligible reports whether q satisfies all 5 v63 criteria
// for rendering as an inline single-select dark focus card on Home.
func IsHomeInlineQuestionEligible(q *lists.PendingQuestionData) bool {
	if q == nil || q.Async || q.QuestionCount != 1 || q.MultiSelect {
		return false
	}
	if len(q.Options) < 1 || len(q.Options) > 3 {
		return false
	}
	stem := strings.TrimSpace(q.Text)
	if stem == "" || strings.Contains(stem, "```") || hasMarkdownTable(stem) {
		return false
	}
	if wrappedHalfWidth(stem, homeQuestionLineWidth) > homeQuestionMaxTextWidth {
		return false
	}
	for _, opt := range q.Options {
		label, _ := lists.OptionLabel(opt.Label)
		label = strings.TrimSpace(label)
		if label == "" || strings.ContainsRune(label, '\n') {
			return false
		}
		if halfWidth(label) > homeQuestionMaxOptionWidth {
			return false
		}
		desc := strings.TrimSpace(opt.Description)
		if desc != "" {
			if strings.Contains(desc, "```") || hasMarkdownTable(desc) {
				return false
			}
			if wrappedHalfWidth(desc, homeQuestionMaxOptionWidth) > homeQuestionMaxDescWidth {
				return false
			}
		}
	}
	return true
}

func hasMarkdownTable(s string) bool {
	return strings.Contains(s, "\n|") || strings.Contains(s, "|---")
}

func halfWidth(s string) int {
	w := 0
	for _, r := range s {
		if r <= 0x7f {
			w++
		} else if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) ||
			unicode.Is(unicode.Hangul, r) || (r >= 0x3000 && r <= 0x303f) || (r >= 0xff00 && r <= 0xffef) {
			w += 2
		} else {
			w += 2
		}
	}
	return w
}

func wrappedHalfWidth(s string, lineWidth int) int {
	lines := strings.Split(s, "\n")
	total := 0
	for i, line := range lines {
		w := halfWidth(strings.TrimSpace(line))
		if w == 0 {
			if i > 0 {
				total += lineWidth
			}
			continue
		}
		if len(lines) == 1 {
			return w
		}
		rows := (w + lineWidth - 1) / lineWidth
		total += rows * lineWidth
	}
	return total
}

func buildHomeQuestionCardView(item *lists.SessionItem, st *State) *corev1.HomeQuestionCardView {
	if item == nil || item.Session == nil || !IsHomeInlineQuestionEligible(item.Session.Pending.Question) {
		return nil
	}
	q := item.Session.Pending.Question
	agentName := lists.AgentInfo(item.Agent).Name

	var whereParts []*corev1.Text
	if item.ProjectName != "" {
		whereParts = append(whereParts, text.Plain(item.ProjectName))
	}
	if item.MachineName != "" {
		whereParts = append(whereParts, text.Plain(item.MachineName))
	}

	askedMs := q.AskedAtMs
	if askedMs <= 0 {
		askedMs = item.AttentionMs
	}
	if askedMs <= 0 {
		askedMs = item.UpdatedMs
	}
	var timeText *corev1.Text
	if st != nil && askedMs > 0 {
		timeText = formatRelativeTime(askedMs, st.Cal)
	}

	opts := make([]*corev1.HomeQuestionOptionView, 0, len(q.Options))
	for i, opt := range q.Options {
		label, rec := lists.OptionLabel(opt.Label)
		opts = append(opts, &corev1.HomeQuestionOptionView{
			OptionNum:   int32(i + 1),
			Label:       strings.TrimSpace(label),
			Description: strings.TrimSpace(opt.Description),
			Recommended: rec,
		})
	}

	canAnswer := true
	var offlineNote *corev1.Text
	if st != nil && lists.WorldUnlinked(&st.World) || item.ShownStatus == lists.StatusUnlinked {
		canAnswer = false
		offlineNote = text.Msg(text.KeyListStateUnlinked)
	} else if item.ShownStatus == lists.StatusOffline {
		canAnswer = false
		offlineNote = text.Msg(text.KeyListStateOffline)
	}

	return &corev1.HomeQuestionCardView{
		SessionId:        item.ID,
		MachineId:        item.MachineID,
		QuestionId:       q.QuestionID,
		ObservedEventSeq: q.ObservedEventSeq,
		BadgeText:        text.Msg(text.KeyListStateAgentAsking, text.Str("agent", agentName)),
		TimeText:         timeText,
		Title:            sessionTitleText(item),
		Where:            text.JoinLiteral(" · ", whereParts...),
		Header:           q.Header,
		QuestionText:     strings.TrimSpace(q.Text),
		Options:          opts,
		CanAnswer:        canAnswer,
		OfflineNote:      offlineNote,
	}
}
