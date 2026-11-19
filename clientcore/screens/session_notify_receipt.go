package screens

import (
	"strings"
	"unicode"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

// receiptFoldColumns is how wide a notification's text may be before its
// receipt can open: design-system.md §7's 「会折行」 width.
const receiptFoldColumns = 40

// buildNotifyReceipt turns a scheduled run's push into its receipt line
// (design-system.md §6 「通知回执」). A push the computer sent on the agent's
// behalf usually copies the start of the turn's reply, so its receipt shows
// only the time. It counts as a copy only when that can be shown from the
// text; anything else (the computer's fixed sentences for a failed, stopped
// or cancelled run, or a turn without reply) shows its text, so a wrong guess
// adds a line rather than losing one.
func buildNotifyReceipt(e *v1.Event, n *v1.ScheduledNotification, ts int64, replies map[string][]string, cal lists.Calendar) *corev1.SessionNotifyReceiptItem {
	out := &corev1.SessionNotifyReceiptItem{
		Label:          text.Msg(text.KeySessionNotifyReceipt),
		Time:           receiptTimeText(ts, cal),
		ScheduledRunId: n.GetRunId(),
	}
	copyOfReply := e.GetOrigin().GetKind() == v1.OriginKind_ORIGIN_KIND_SYSTEM && e.TurnId != "" && copiesReply(n.GetText(), replies[e.TurnId])
	body := strings.TrimSpace(strings.ReplaceAll(n.GetText(), "\r\n", "\n"))
	if copyOfReply || body == "" {
		return out
	}
	first, _, multiline := strings.Cut(body, "\n")
	out.Preview = strings.TrimSpace(first)
	if multiline || pastColumns(body, receiptFoldColumns) {
		out.Expandable = true
		out.Body = body
	}
	return out
}

// pastColumns estimates whether s is wider than cols half-width columns:
// from U+2E80 up a character takes two, except the half-width forms
// (design-system.md §7, the same estimate as the clients' loose lists).
func pastColumns(s string, cols int) bool {
	w := 0
	for _, r := range s {
		if r >= 0x2e80 && (r < 0xff61 || r > 0xffdc) && (r < 0xffe8 || r > 0xffee) {
			w += 2
		} else {
			w++
		}
		if w > cols {
			return true
		}
	}
	return false
}

// receiptTimeText is the clock today, and the date with it otherwise.
func receiptTimeText(ms int64, cal lists.Calendar) *corev1.Text {
	if cal.TodayStartMs <= 0 || ms >= cal.TodayStartMs {
		return text.Time(ms, corev1.TimestampStyle_TIMESTAMP_STYLE_TIME)
	}
	if year, _, ok := lists.MonthFor(ms, cal); ok && (cal.NowYear == 0 || year == cal.NowYear) {
		return text.Time(ms, corev1.TimestampStyle_TIMESTAMP_STYLE_MONTH_DAY_TIME)
	}
	return text.Time(ms, corev1.TimestampStyle_TIMESTAMP_STYLE_DATE_TIME)
}

// replyTexts collects each turn's agent replies, kept to letters and
// digits for copiesReply.
func replyTexts(events []*v1.Event) map[string][]string {
	out := map[string][]string{}
	for _, e := range events {
		if m := e.GetAgentMessage(); m != nil && e.TurnId != "" {
			if t := lettersAndDigits(m.Text); t != "" {
				out[e.TurnId] = append(out[e.TurnId], t)
			}
		}
	}
	return out
}

// copiesReply reports whether a push's text is the start of one of the
// turn's replies. The computer cuts the reply to 200 characters and drops
// its line breaks, and Markdown marks differ from what was sent, so both
// sides keep only letters and digits before comparing.
func copiesReply(notice string, replies []string) bool {
	n := lettersAndDigits(notice)
	if n == "" {
		return false
	}
	for _, r := range replies {
		if strings.HasPrefix(r, n) {
			return true
		}
	}
	return false
}

func lettersAndDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// placeReceiptsInTurns moves each receipt that came after its turn's
// 「本轮用时」 line to just before it: the computer sends its push only once
// the turn has ended, but the receipt belongs to that turn. The moved item
// takes the line's sequence so that items stay in sequence order for paging.
func placeReceiptsInTurns(items []*corev1.SessionTimelineItem) {
	for i, it := range items {
		if it.GetNotifyReceipt() == nil || it.TurnId == "" {
			continue
		}
		at := -1
		for j := i - 1; j >= 0; j-- {
			if te := items[j].GetTurnEnd(); te != nil && !te.IsSessionEnded && items[j].TurnId == it.TurnId {
				at = j
				break
			}
		}
		if at < 0 {
			continue
		}
		it.Seq, it.EndSeq = items[at].Seq, items[at].Seq
		copy(items[at+1:i+1], items[at:i])
		items[at] = it
	}
}
