package schedule

import (
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

const (
	paramClock             = "clock"
	triggerTypeAt          = "at"
	triggerTypeCalendar    = "calendar"
	triggerTypeInterval    = "interval"
	triggerTypeAfterTurn   = "after_turn"
	statusFailed           = "failed"
	statusUncertain        = "uncertain"
	statusInterrupted      = "interrupted"
	statusSkipped          = "skipped"
	statusCancelled        = "cancelled"
	statusCompleted        = "completed"
	statusDispatching      = "dispatching"
	statusQueued           = "queued"
	statusRunning          = "running"
	notifSourceAgent       = "agent"
	notifSourceDaemon      = "daemon"
	notifSourceAgentDaemon = "agent_and_daemon"
	notifSourceUndeliver   = "undeliverable"
	takeoverViaLocal       = "local"
	reasonExecMissing      = "execution_session_missing"
	reasonAgentPerms       = "agent_schedule_permissions_changed"
)

// CalendarBounds carries LocalCalendar plus optional local timezone and future-day boundaries from the shell.
type CalendarBounds struct {
	Cal                     lists.Calendar
	LocalTimezone           string
	TomorrowStartMs         int64
	DayAfterTomorrowStartMs int64
}

func (b CalendarBounds) bounds() (today, yesterday, week, tomorrow, dayAfter int64) {
	today = b.Cal.TodayStartMs
	yesterday = b.Cal.YesterdayStartMs
	week = b.Cal.WeekStartMs
	if today == 0 && b.Cal.NowMs > 0 {
		off := b.EffectiveOffsetMinutes()
		localSec := b.Cal.NowMs/1000 + int64(off)*60
		days := localSec / 86400
		if localSec%86400 < 0 {
			days--
		}
		today = (days*86400 - int64(off)*60) * 1000
	}
	if yesterday == 0 && today > 0 {
		yesterday = today - 86_400_000
	}
	if week == 0 && today > 0 {
		week = today - 6*86_400_000
	}
	tomorrow = b.TomorrowStartMs
	if tomorrow == 0 && today > 0 {
		tomorrow = today + 86_400_000
	}
	dayAfter = b.DayAfterTomorrowStartMs
	if dayAfter == 0 && tomorrow > 0 {
		dayAfter = tomorrow + 86_400_000
	}
	return today, yesterday, week, tomorrow, dayAfter
}

// EffectiveOffsetMinutes returns the configured UTC offset or infers it from TodayStartMs.
func (b CalendarBounds) EffectiveOffsetMinutes() int32 {
	if b.Cal.UTCOffsetMinutes != 0 {
		return b.Cal.UTCOffsetMinutes
	}
	if b.Cal.TodayStartMs > 0 {
		rem := ((b.Cal.TodayStartMs/1000)%86400 + 86400) % 86400
		if rem > 43200 {
			rem -= 86400
		}
		return int32(-rem / 60)
	}
	return 0
}

func (b CalendarBounds) sameYear(ms int64) bool {
	if b.Cal.NowYear == 0 {
		return true
	}
	if yr, _, ok := lists.MonthFor(ms, b.Cal); ok {
		return yr == b.Cal.NowYear
	}
	yr, _, _, _, _, _ := lists.CivilFromUnixMs(ms, b.EffectiveOffsetMinutes())
	return int32(yr) == b.Cal.NowYear
}

// DayBucketKey returns a stable local-calendar day key for grouping runs.
func (b CalendarBounds) DayBucketKey(ms int64) string {
	if ms <= 0 {
		return "unknown"
	}
	today, yesterday, _, tomorrow, dayAfter := b.bounds()
	if today > 0 {
		if ms >= today && ms < tomorrow {
			return "today"
		}
		if ms >= yesterday && ms < today {
			return "yesterday"
		}
		if ms >= tomorrow && ms < dayAfter {
			return "tomorrow"
		}
	}
	yr, mo, day, _, _, _ := lists.CivilFromUnixMs(ms, b.EffectiveOffsetMinutes())
	return formatYMD(yr, mo, day)
}

// RowTimeText formats a schedule row timestamp (time today, Yesterday, Weekday within 6d, MonthDay same year, else Date).
func RowTimeText(ms int64, b CalendarBounds) *corev1.Text {
	if ms <= 0 {
		return nil
	}
	today, yesterday, week, tomorrow, _ := b.bounds()
	if today > 0 {
		if ms >= today && ms < tomorrow {
			return text.Time(ms, corev1.TimestampStyle_TIMESTAMP_STYLE_TIME)
		}
		if ms >= yesterday && ms < today {
			return text.Msg(text.KeyTimeYesterday)
		}
		if ms >= week && ms < yesterday {
			return text.Time(ms, corev1.TimestampStyle_TIMESTAMP_STYLE_WEEKDAY)
		}
	}
	if b.sameYear(ms) {
		return text.Time(ms, corev1.TimestampStyle_TIMESTAMP_STYLE_MONTH_DAY)
	}
	return text.Time(ms, corev1.TimestampStyle_TIMESTAMP_STYLE_DATE)
}

// DayText formats a calendar day (Today, Tomorrow, Yesterday, MonthDay, or Date).
func DayText(ms int64, b CalendarBounds) *corev1.Text {
	if ms <= 0 {
		return nil
	}
	today, yesterday, _, tomorrow, dayAfter := b.bounds()
	if today > 0 {
		if ms >= today && ms < tomorrow {
			return text.Msg(text.KeyTimeToday)
		}
		if ms >= tomorrow && ms < dayAfter {
			return text.Msg(text.KeyTimeTomorrow)
		}
		if ms >= yesterday && ms < today {
			return text.Msg(text.KeyTimeYesterday)
		}
	}
	if b.sameYear(ms) {
		return text.Time(ms, corev1.TimestampStyle_TIMESTAMP_STYLE_MONTH_DAY)
	}
	return text.Time(ms, corev1.TimestampStyle_TIMESTAMP_STYLE_DATE)
}

// WhenText formats a moment with day + clock (Today 08:00, Tomorrow 08:00, Yesterday 08:00, MonthDayTime, or DateTime).
func WhenText(ms int64, b CalendarBounds) *corev1.Text {
	if ms <= 0 {
		return nil
	}
	today, yesterday, _, tomorrow, dayAfter := b.bounds()
	if today > 0 {
		if ms >= today && ms < tomorrow {
			return text.Msg(text.KeyTimeDayClock, text.ArgText("day", text.Msg(text.KeyTimeToday)), text.ArgTimestamp(paramClock, ms, corev1.TimestampStyle_TIMESTAMP_STYLE_TIME))
		}
		if ms >= tomorrow && ms < dayAfter {
			return text.Msg(text.KeyTimeDayClock, text.ArgText("day", text.Msg(text.KeyTimeTomorrow)), text.ArgTimestamp(paramClock, ms, corev1.TimestampStyle_TIMESTAMP_STYLE_TIME))
		}
		if ms >= yesterday && ms < today {
			return text.Msg(text.KeyTimeDayClock, text.ArgText("day", text.Msg(text.KeyTimeYesterday)), text.ArgTimestamp(paramClock, ms, corev1.TimestampStyle_TIMESTAMP_STYLE_TIME))
		}
	}
	if b.sameYear(ms) {
		return text.Time(ms, corev1.TimestampStyle_TIMESTAMP_STYLE_MONTH_DAY_TIME)
	}
	return text.Time(ms, corev1.TimestampStyle_TIMESTAMP_STYLE_DATE_TIME)
}

// QuantityText formats an integer count safely even when val > math.MaxInt32.
func QuantityText(val int64, normalKey, largeKey string) *corev1.Text {
	if val >= 0 && val <= math.MaxInt32 {
		return text.Msg(normalKey, text.Int("count", val))
	}
	return text.Msg(largeKey, text.Int("count", 2), text.Str("total", strconv.FormatInt(val, 10)))
}

var weekdayKeys = [8]string{
	"",
	text.KeyScheduleMon,
	text.KeyScheduleTue,
	text.KeyScheduleWed,
	text.KeyScheduleThu,
	text.KeyScheduleFri,
	text.KeyScheduleSat,
	text.KeyScheduleSun,
}

// FrequencyText formats the human description of a trigger's cadence without timezone suffix.
func FrequencyText(tr Trigger, b CalendarBounds) *corev1.Text {
	switch tr.Type {
	case triggerTypeAt:
		if ms := ParseRFC3339Ms(tr.At); ms > 0 {
			return text.Msg(text.KeyScheduleOnceAt, text.ArgText("time", WhenText(ms, b)))
		}
		return text.Msg(text.KeyScheduleOnceSchedules)
	case triggerTypeCalendar:
		days := slices.Clone(tr.Weekdays)
		sortutil.Ints(days)
		clock := text.Str("time", tr.Time)
		if hr, mn, ok := parseHHMM(tr.Time); ok && tr.JitterMinutes > 0 {
			end := min(hr*60+mn+int(tr.JitterMinutes), 23*60+59)
			clock = text.ArgText("time", text.Msg(text.KeyScheduleRandomWindow, text.Str("start", tr.Time), text.Str("end", twoDigits(end/60)+":"+twoDigits(end%60))))
		}
		if len(days) == 0 || len(days) == 7 {
			return text.Msg(text.KeyScheduleDailyAt, clock)
		}
		if slices.Equal(days, []int{1, 2, 3, 4, 5}) {
			return text.Msg(text.KeyScheduleWorkdaysAt, clock)
		}
		dayTexts := make([]*corev1.Text, 0, len(days))
		for _, d := range days {
			if d >= 1 && d <= 7 {
				dayTexts = append(dayTexts, text.Msg(weekdayKeys[d]))
			} else {
				dayTexts = append(dayTexts, text.Plain("?"))
			}
		}
		joinedDays := text.Join(text.KeyCommonListSeparator, dayTexts...)
		return text.Msg(text.KeyScheduleWeeklyAt, text.ArgText("days", joinedDays), clock)
	case triggerTypeInterval:
		s := tr.IntervalSeconds
		switch {
		case s == 3600:
			return text.Msg(text.KeyScheduleHourly)
		case s > 0 && s%86400 == 0:
			return QuantityText(s/86400, text.KeyScheduleEveryDays, text.KeyScheduleAndroidLargeEveryDays)
		case s > 0 && s%3600 == 0:
			return QuantityText(s/3600, text.KeyScheduleEveryHr, text.KeyScheduleAndroidLargeEveryHours)
		case s > 0 && s%60 == 0:
			return QuantityText(s/60, text.KeyScheduleEveryMin, text.KeyScheduleAndroidLargeEveryMinutes)
		default:
			return QuantityText(s, text.KeyScheduleEverySec, text.KeyScheduleAndroidLargeEverySeconds)
		}
	case triggerTypeAfterTurn:
		if tr.Repeat {
			return QuantityText(tr.DelaySeconds, text.KeyScheduleAfterEachTurn, text.KeyScheduleAndroidLargeAfterEachTurn)
		}
		return QuantityText(tr.DelaySeconds, text.KeyScheduleAfterCurrentTurn, text.KeyScheduleAndroidLargeAfterCurrentTurn)
	default:
		return text.Msg(text.KeyScheduleAndroidUnknownTime)
	}
}

// TimingText formats the frequency and appends the trigger's zone label when it differs from localZone.
func TimingText(tr Trigger, b CalendarBounds) *corev1.Text {
	freq := FrequencyText(tr, b)
	if tr.Type != triggerTypeCalendar || tr.Timezone == "" {
		return freq
	}
	canonTrig := CanonicalZone(tr.Timezone)
	canonLocal := CanonicalZone(b.LocalTimezone)
	if canonLocal != "" && canonTrig == canonLocal {
		return freq
	}
	if canonLocal == "" {
		// When shell hasn't reported local timezone yet, avoid showing redundant zone labels unless clear.
		return freq
	}
	zt := ZoneTimeText(canonTrig)
	if text.IsEmpty(zt) {
		return freq
	}
	return text.Msg(text.KeyScheduleFrequencyInZone, text.ArgText("frequency", freq), text.ArgText("zone", zt))
}

// LimitsText formats max_runs and/or until limits for a recurring trigger, or nil if unlimited.
func LimitsText(tr Trigger, b CalendarBounds) *corev1.Text {
	untilMs := ParseRFC3339Ms(tr.Until)
	if tr.MaxRuns > 0 && untilMs > 0 {
		// schedule.limits_both handles normal counts; for > MaxInt32 fallback to join.
		if tr.MaxRuns <= math.MaxInt32 {
			return text.Msg(text.KeyScheduleLimitsBoth, text.Int("count", tr.MaxRuns), text.ArgText("time", WhenText(untilMs, b)))
		}
		runsTxt := QuantityText(tr.MaxRuns, text.KeyScheduleRunsTotal, text.KeyScheduleAndroidLargeRunsTotal)
		untilTxt := text.Msg(text.KeyScheduleUntil, text.ArgText("value", WhenText(untilMs, b)))
		return text.Join(text.KeyCommonListSeparator, runsTxt, untilTxt)
	}
	if tr.MaxRuns > 0 {
		return QuantityText(tr.MaxRuns, text.KeyScheduleRunsTotal, text.KeyScheduleAndroidLargeRunsTotal)
	}
	if untilMs > 0 {
		return text.Msg(text.KeyScheduleUntil, text.ArgText("value", WhenText(untilMs, b)))
	}
	return nil
}

// NextText returns the task's next-run status text.
func NextText(t *Task, b CalendarBounds) *corev1.Text {
	if t == nil {
		return nil
	}
	if t.Active != nil && IsInProgressStatus(t.Active.Status) {
		return text.Msg(text.KeyListStateRunning)
	}
	switch GroupOf(t) {
	case GroupAttention:
		return text.Msg(text.KeyScheduleStopped)
	case GroupPaused:
		return text.Msg(text.KeyQueuePaused)
	case GroupEnded:
		return text.Msg(text.KeyListStateEnded)
	}
	if ms := ParseRFC3339Ms(t.NextRunAt); ms > 0 {
		return text.Msg(text.KeyScheduleNext, text.ArgText("value", WhenText(ms, b)))
	}
	return text.Msg(text.KeyScheduleNextRunPending)
}

// ReasonText maps daemon schedule/run reason codes to localized Text.
func ReasonText(reason string, compact bool) *corev1.Text {
	if compact {
		switch reason {
		case reasonAgentPerms:
			return text.Msg(text.KeyScheduleTaskCreationPermissionsChanged)
		case "agent_schedule_interval_below_minimum":
			return text.Msg(text.KeyScheduleTheAgentCreatedTaskIntervalIsTooShort)
		case "execution_failed":
			return text.Msg(text.KeyScheduleExecutionFailed)
		case "execution_interrupted":
			return text.Msg(text.KeyScheduleExecutionInterrupted)
		case "message_cancelled":
			return text.Msg(text.KeyScheduleTheScheduledMessageWasCanceledBeforeDelivery)
		}
	}
	switch reason {
	case "relay_session_limit":
		return text.Msg(text.KeyScheduleTheRelaySSessionLimitHasBeenReached)
	case "execution_failed":
		return text.Msg(text.KeyScheduleExecutionFailedTheTaskStopped)
	case "execution_interrupted":
		return text.Msg(text.KeyScheduleExecutionInterruptedTheTaskStopped)
	case "execution_session_exited":
		return text.Msg(text.KeyScheduleTheRunningConversationEnded)
	case "execution_session_missing", "source_session_missing":
		return text.Msg(text.KeyScheduleTheRunningConversationCouldNotBeFound)
	case "message_delivery_failed":
		return text.Msg(text.KeyScheduleTheMessageWasNotDeliveredToTheCli)
	case "could_not_queue_message":
		return text.Msg(text.KeyScheduleTheMessageCouldNotBeQueued)
	case "message_cancelled":
		return text.Msg(text.KeyScheduleTheScheduledMessageWasCanceledBeforeDeliveryTheTask)
	case "execution_input_paused":
		return text.Msg(text.KeyScheduleTheScheduledMessageIsPausedResumeOrCancelIt)
	case "restart_dispatching":
		return text.Msg(text.KeyScheduleDeliveryBeforeTheComputerRestartedCouldNotBeConfirmed)
	case "agent_schedule_permissions_changed":
		return text.Msg(text.KeyScheduleTaskCreationPermissionsChangedTheTaskStopped)
	case "agent_schedule_interval_below_minimum":
		return text.Msg(text.KeyScheduleTheAgentCreatedTaskIntervalIsTooShortThe)
	case "catch_up_window_expired":
		return text.Msg(text.KeyScheduleTheCatchUpWindowExpiredThisRunWasSkipped)
	case "completed_while_paused":
		return text.Msg(text.KeySchedulePausedTheScheduleExpiredInTheMeantime)
	case "user_paused":
		return text.Msg(text.KeyQueuePaused)
	case "user_cancelled":
		return text.Msg(text.KeyScheduleDeleted)
	case "":
		return text.Msg(text.KeyScheduleExecutionOutcomeIsUnconfirmed)
	default:
		if strings.HasPrefix(reason, "agent_disabled:") {
			id := strings.TrimPrefix(reason, "agent_disabled:")
			agName := lists.AgentByID(id).Name
			if agName == "" {
				agName = id
			}
			return text.Msg(text.KeyScheduleReasonReturnedByTheComputer, text.Str("value", agName+" disabled"))
		}
		return text.Msg(text.KeyScheduleReasonReturnedByTheComputer, text.Str("value", reason))
	}
}

// RunResultText formats the outcome badge/status label for one Run.
func RunResultText(r *Run) *corev1.Text {
	if r == nil {
		return nil
	}
	switch r.Status {
	case statusFailed:
		return text.Msg(text.KeyAttachmentFailed)
	case statusUncertain:
		return text.Msg(text.KeyScheduleUnconfirmed)
	case statusInterrupted:
		return text.Msg(text.KeySessionInterrupted)
	case statusSkipped:
		return text.Msg(text.KeyScheduleSkipped)
	case statusCancelled:
		return text.Msg(text.KeyQueueCanceled)
	}
	if r.Notified {
		return text.Msg(text.KeyScheduleNotifiedYou)
	}
	switch r.Status {
	case statusCompleted:
		return text.Msg(text.KeyScheduleNoNotification)
	case statusDispatching:
		return text.Msg(text.KeyScheduleStarting)
	case statusQueued:
		return text.Msg(text.KeyScheduleWaitingForTheCurrentTurnToEnd)
	case statusRunning:
		return text.Msg(text.KeyScheduleRunning)
	default:
		return text.Msg(text.KeyScheduleWaitingForResults)
	}
}

// RunResultTitle returns a distinct heading only when the agent authored a notification different from final_summary.
func RunResultTitle(r *Run) string {
	if r == nil {
		return ""
	}
	if r.NotificationSource != "agent" && r.NotificationSource != "agent_and_daemon" {
		return ""
	}
	nt := collapseSpaces(r.NotificationText)
	fs := collapseSpaces(r.FinalSummary)
	if nt == "" || nt == fs {
		return ""
	}
	return nt
}

// RunSummaryText formats the human summary of a run (or its reason if stopped).
func RunSummaryText(r *Run) *corev1.Text {
	if r == nil {
		return nil
	}
	if IsStoppedRun(r.Status) {
		return ReasonText(r.Reason, false)
	}
	if r.NotificationText != "" {
		return text.Plain(r.NotificationText)
	}
	if r.FinalSummary != "" {
		return text.Plain(r.FinalSummary)
	}
	if r.Reason != "" {
		return ReasonText(r.Reason, false)
	}
	return nil
}

// RunHistoryRowText formats a run row's summary with fallback to RunResultText (or schedule.no_notification for quiet).
func RunHistoryRowText(r *Run) *corev1.Text {
	if s := RunSummaryText(r); !text.IsEmpty(s) {
		return s
	}
	if rr := RunResultText(r); !text.IsEmpty(rr) {
		return rr
	}
	return text.Msg(text.KeyScheduleNoNotification)
}

// NotificationSourceText returns a label describing who sent the notification.
func NotificationSourceText(source string) *corev1.Text {
	switch source {
	case notifSourceAgent:
		return text.Msg(text.KeyScheduleAndroidAgentNotification)
	case notifSourceDaemon:
		return text.Msg(text.KeyScheduleNotifyOnCompletion)
	case notifSourceAgentDaemon:
		return text.Msg(text.KeyScheduleAndroidAgentNotificationCompletionNotification)
	case notifSourceUndeliver:
		return text.Msg(text.KeyScheduleAndroidCannotRequestNotification)
	default:
		return nil
	}
}

// LastResultText builds the preview line for a task without unread notifications.
func LastResultText(t *Task, b CalendarBounds) (preview *corev1.Text, isPlaceholder bool) {
	if t == nil {
		return nil, false
	}
	if GroupOf(t) == GroupAttention {
		return ReasonText(t.Reason, false), false
	}
	runs := AllRuns(t)
	if InProgress(t) {
		if ln := LatestNotification(t); ln != nil {
			return RunHistoryRowText(ln), false
		}
		return text.Msg(text.KeyScheduleNoResultsYet), true
	}
	if len(runs) == 0 {
		if ms := ParseRFC3339Ms(t.NextRunAt); ms > 0 {
			return text.Msg(text.KeyScheduleNoRunsNext, text.ArgText("time", WhenText(ms, b))), false
		}
		return text.Msg(text.KeyScheduleHasNotRunYet), false
	}
	if ln := LatestNotification(t); ln != nil {
		if s := RunSummaryText(ln); !text.IsEmpty(s) {
			return s, false
		}
		return RunResultText(ln), false
	}
	// Check if all runs today were quiet.
	var todayRuns []*Run
	for _, r := range runs {
		if b.DayBucketKey(RunTimeMs(r)) == "today" {
			todayRuns = append(todayRuns, r)
		}
	}
	if len(todayRuns) > 1 {
		allQuiet := true
		for _, r := range todayRuns {
			if !IsQuietRun(r) {
				allQuiet = false
				break
			}
		}
		if allQuiet {
			return text.Msg(text.KeyScheduleRunsTodayNoneNotifiedYou, text.Int("count", int64(len(todayRuns)))), false
		}
	}
	last := runs[0]
	resText := RunResultText(last)
	when := WhenText(RunTimeMs(last), b)
	var detail *corev1.Text
	if last.Notified || IsStoppedRun(last.Status) {
		detail = RunSummaryText(last)
	}
	if !text.IsEmpty(detail) {
		return text.Msg(text.KeyScheduleLastResultDetail, text.ArgText("detail", detail), text.ArgText("result", resText), text.ArgText("time", when)), false
	}
	return text.Msg(text.KeyScheduleLastResult, text.ArgText("result", resText), text.ArgText("time", when)), false
}

// AttentionLeadText returns the short orange status word ("Could not run" vs "Stopped").
func AttentionLeadText(t *Task) *corev1.Text {
	if t != nil && t.Status == statusFailed && OneShot(t) {
		return text.Msg(text.KeyScheduleCouldNotRun)
	}
	return text.Msg(text.KeyScheduleStopped)
}

// AttentionBannerText builds the full detail recovery banner message for a stopped task.
func AttentionBannerText(t *Task, b CalendarBounds) *corev1.Text {
	if t == nil {
		return nil
	}
	if TargetMissing(t) {
		reasonTxt := ReasonText(reasonExecMissing, false)
		var failedMs int64
		for _, r := range AllRuns(t) {
			if r.Status == statusFailed && r.Reason == t.Reason {
				failedMs = RunTimeMs(r)
				break
			}
		}
		if failedMs == 0 {
			for _, r := range AllRuns(t) {
				if r.Status == statusFailed {
					failedMs = RunTimeMs(r)
					break
				}
			}
		}
		if failedMs > 0 {
			return text.Msg(text.KeyScheduleMissingTargetRun, text.ArgText("reason", reasonTxt), text.ArgText("time", WhenText(failedMs, b)))
		}
		return text.Msg(text.KeyScheduleMissingTarget, text.ArgText("reason", reasonTxt))
	}
	reasonTxt := ReasonText(t.Reason, false)
	var notice *corev1.Text
	switch {
	case t.Status == statusUncertain:
		notice = text.Msg(text.KeyScheduleAttentionUncertain, text.ArgText("reason", reasonTxt))
	case FailedOnce(t):
		notice = text.Msg(text.KeyScheduleIosAttentionOnce, text.ArgText("reason", reasonTxt))
	default:
		notice = text.Msg(text.KeyScheduleAttentionResume, text.ArgText("reason", reasonTxt))
	}
	if t.Reason == reasonAgentPerms {
		return text.Msg(text.KeyScheduleIosTakeoverNotice, text.ArgText("notice", notice))
	}
	return notice
}

// TakeoverText returns a localized takeover label when the task was taken over from an agent delegation.
func TakeoverText(t *Task) *corev1.Text {
	if t == nil || t.Takeover == nil {
		return nil
	}
	if t.Takeover.Via == takeoverViaLocal {
		return text.Msg(text.KeyScheduleTakeoverViaLocal)
	}
	return text.Msg(text.KeyScheduleTakeoverViaClient)
}

// CreationFooterText builds the detail footer describing execution mode, creator, and creation timestamp.
func CreationFooterText(t *Task, b CalendarBounds) *corev1.Text {
	if t == nil {
		return nil
	}
	var kind *corev1.Text
	if t.Spec.Target.Type == "resume_session" {
		kind = text.Msg(text.KeyScheduleMessagesGoToTheCurrentConversationAndWaitFor)
	} else {
		kind = text.Msg(text.KeyScheduleEachRunOpensANewConversationGroupedUnderThis)
	}
	var owner *corev1.Text
	if t.Source == "agent" {
		owner = text.Msg(text.KeyScheduleCreatedByTheAgentInAConversation)
	} else {
		owner = text.Msg(text.KeyScheduleCreatedByYou)
	}
	if ms := ParseRFC3339Ms(t.CreatedAt); ms > 0 {
		return text.Msg(text.KeyScheduleCreationSummaryAt,
			text.ArgText("kind", kind),
			text.ArgText("owner", owner),
			text.ArgText("time", WhenText(ms, b)),
		)
	}
	return text.Msg(text.KeyScheduleCreationSummary,
		text.ArgText("kind", kind),
		text.ArgText("owner", owner),
	)
}
