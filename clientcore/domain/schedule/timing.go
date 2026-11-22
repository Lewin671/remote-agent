package schedule

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

// TimingKind mirrors ScheduleTiming.Kind across clients.
type TimingKind uint8

const (
	TimingOnce TimingKind = iota + 1
	TimingDaily
	TimingWeekly
	TimingInterval
	TimingAfterTurn
)

// IntervalUnit selects the unit multiplier for interval triggers.
type IntervalUnit uint8

const (
	UnitMinutes IntervalUnit = iota + 1
	UnitHours
	UnitDays
	UnitSeconds
)

// TimingEnd selects the recurrence end condition.
type TimingEnd uint8

const (
	EndNever TimingEnd = iota + 1
	EndCount
	EndDate
)

// TimingDraft holds editable trigger form state matching Android ScheduleTime / iOS ScheduleTiming.
type TimingDraft struct {
	Kind                TimingKind
	AfterTurnUnselected bool
	Date                string // YYYY-MM-DD
	Time                string // HH:MM
	Zone                string // IANA zone ID
	Workdays            bool
	Weekdays            []int // 1..7
	Interval            string
	Unit                IntervalUnit
	End                 TimingEnd
	Count               string
	UntilDate           string // YYYY-MM-DD
	UntilMs             int64
	UntilEdited         bool
	JitterMinutes       int64
	Original            *Trigger
	InitialEnd          TimingEnd
	InitialCount        string
	InitialUntilDate    string
	InitialUntilMs      int64
}

// TimingBuildInput provides current time and optional shell-resolved DST/zone checks.
type TimingBuildInput struct {
	NowMs       int64
	AtMs        *int64
	UntilMs     *int64
	ZoneValid   *bool
	AtDSTGap    bool
	UntilDSTGap bool
}

func unitSeconds(u IntervalUnit) int64 {
	switch u {
	case UnitMinutes:
		return 60
	case UnitHours:
		return 3600
	case UnitDays:
		return 86400
	case UnitSeconds:
		return 1
	default:
		return 3600
	}
}

func twoDigits(v int) string {
	return string([]byte{byte('0' + (v/10)%10), byte('0' + v%10)})
}

func formatYMD(yr, mo, day int) string {
	y := strconv.Itoa(yr)
	for len(y) < 4 {
		y = "0" + y
	}
	return y + "-" + twoDigits(mo) + "-" + twoDigits(day)
}

func civilDateStr(ms int64, zone string, bounds CalendarBounds) (dateStr, timeStr string) {
	if ms <= 0 {
		return "", ""
	}
	if zone != "" && zone != "Local" {
		if loc, err := loadIANALocation(zone); err == nil && loc != nil {
			t := time.UnixMilli(ms).In(loc)
			return t.Format("2006-01-02"), t.Format("15:04")
		}
	}
	yr, mo, day, _, hr, min := lists.CivilFromUnixMs(ms, bounds.EffectiveOffsetMinutes())
	return formatYMD(yr, mo, day), twoDigits(hr) + ":" + twoDigits(min)
}

func uniqueSortedWeekdays(days []int) []int {
	seen := make(map[int]bool, len(days))
	out := make([]int, 0, len(days))
	for _, d := range days {
		if d >= 1 && d <= 7 && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	sortutil.Ints(out)
	return out
}

var defaultJitterOptions = []int64{0, 15, 30, 60}

// JitterOptionMinutes returns sorted minute choices for the calendar jitter selector,
// always including the fixed presets and any positive custom value currently on the draft.
func JitterOptionMinutes(current int64) []int64 {
	out := slices.Clone(defaultJitterOptions)
	if current > 0 && !slices.Contains(out, current) {
		out = append(out, current)
		sortutil.Int64s(out)
	}
	return out
}

// NewTimingDraft initializes a default daily 08:00 timing draft.
func NewTimingDraft(nowMs int64, localZone string, bounds CalendarBounds) TimingDraft {
	z := strings.TrimSpace(localZone)
	if z == "" {
		z = "UTC"
	}
	tomorrowMs := nowMs + 86_400_000
	if nowMs == 0 {
		tomorrowMs = bounds.Cal.NowMs + 86_400_000
	}
	dateStr, _ := civilDateStr(tomorrowMs, z, bounds)
	if dateStr == "" {
		dateStr = "2026-01-02"
	}
	untilDateStr, _ := civilDateStr(tomorrowMs+29*86_400_000, z, bounds)
	if untilDateStr == "" {
		untilDateStr = dateStr
	}
	return TimingDraft{
		Kind:         TimingDaily,
		Date:         dateStr,
		Time:         "08:00",
		Zone:         z,
		Weekdays:     []int{1},
		Interval:     "1",
		Unit:         UnitHours,
		End:          EndNever,
		Count:        "10",
		UntilDate:    untilDateStr,
		InitialEnd:   EndNever,
		InitialCount: "10",
	}
}

// TimingDraftFromTrigger converts an existing Trigger into an editable TimingDraft.
func TimingDraftFromTrigger(tr Trigger, nowMs int64, localZone string, bounds CalendarBounds) TimingDraft {
	d := NewTimingDraft(nowMs, localZone, bounds)
	orig := tr
	orig.Weekdays = slices.Clone(tr.Weekdays)
	d.Original = &orig

	if tr.Timezone != "" {
		d.Zone = tr.Timezone
	}

	atMs := ParseRFC3339Ms(tr.At)
	if atMs > 0 {
		if ds, ts := civilDateStr(atMs, d.Zone, bounds); ds != "" {
			d.Date = ds
			if tr.Time == "" {
				d.Time = ts
			}
		}
	}
	if tr.Time != "" {
		d.Time = tr.Time
	}

	days := uniqueSortedWeekdays(tr.Weekdays)
	switch tr.Type {
	case "at":
		d.Kind = TimingOnce
	case "interval":
		d.Kind = TimingInterval
		secs := tr.IntervalSeconds
		switch {
		case secs <= 0:
			d.Unit = UnitHours
			d.Interval = "1"
		case secs%86400 == 0:
			d.Unit = UnitDays
			d.Interval = strconv.FormatInt(secs/86400, 10)
		case secs%3600 == 0:
			d.Unit = UnitHours
			d.Interval = strconv.FormatInt(secs/3600, 10)
		case secs%60 == 0:
			d.Unit = UnitMinutes
			d.Interval = strconv.FormatInt(secs/60, 10)
		default:
			d.Unit = UnitSeconds
			d.Interval = strconv.FormatInt(secs, 10)
		}
	case "after_turn":
		d.Kind = TimingAfterTurn
		d.AfterTurnUnselected = true
	default:
		if tr.JitterMinutes > 0 {
			d.JitterMinutes = tr.JitterMinutes
		}
		if slices.Equal(days, []int{1, 2, 3, 4, 5}) {
			d.Kind = TimingDaily
			d.Workdays = true
			d.Weekdays = []int{1}
		} else if len(days) == 0 || len(days) == 7 {
			d.Kind = TimingDaily
			d.Workdays = false
			d.Weekdays = []int{1}
		} else {
			d.Kind = TimingWeekly
			d.Weekdays = days
		}
	}

	if tr.MaxRuns > 0 {
		d.End = EndCount
		d.Count = strconv.FormatInt(tr.MaxRuns, 10)
	} else if tr.Until != "" {
		d.End = EndDate
		if ms := ParseRFC3339Ms(tr.Until); ms > 0 {
			d.UntilMs = ms
			if ds, _ := civilDateStr(ms, d.Zone, bounds); ds != "" {
				d.UntilDate = ds
			}
		}
	} else {
		d.End = EndNever
	}

	d.InitialEnd = d.End
	d.InitialCount = d.Count
	d.InitialUntilDate = d.UntilDate
	d.InitialUntilMs = d.UntilMs
	return d
}

// InZone updates the draft's timezone while preserving any tracked cutoff instant.
func (d TimingDraft) InZone(nextZone string, resolvedUntilDate string, resolvedUntilMs int64, bounds CalendarBounds) TimingDraft {
	if d.End == EndDate && resolvedUntilMs == 0 && d.UntilMs == 0 && d.UntilDate != "" && hasNativeTZData() {
		if y, mo, day, ok := parseYMD(d.UntilDate); ok {
			if ms, gap, avail, err := resolveLocalZoned(y, mo, day, 23, 59, 59, d.Zone); err == nil && avail && !gap {
				d.UntilMs = ms
			}
		}
	}
	d.Zone = strings.TrimSpace(nextZone)
	if resolvedUntilMs > 0 {
		d.UntilMs = resolvedUntilMs
	}
	if resolvedUntilDate != "" {
		d.UntilDate = resolvedUntilDate
	} else if d.UntilMs > 0 && d.Zone != "" && d.Zone != "Local" {
		if ds, _ := civilDateStr(d.UntilMs, d.Zone, bounds); ds != "" {
			d.UntilDate = ds
		}
	}
	return d
}

func parseHHMM(s string) (int, int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, 0, false
	}
	h, err1 := strconv.Atoi(s[:2])
	m, err2 := strconv.Atoi(s[3:])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

func parseYMD(s string) (int, time.Month, int, bool) {
	parts := strings.Split(s, "-")
	if len(parts) != 3 || len(parts[0]) != 4 || len(parts[1]) != 2 || len(parts[2]) != 2 {
		return 0, 0, 0, false
	}
	y, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	d, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil || m < 1 || m > 12 || d < 1 || d > 31 {
		return 0, 0, 0, false
	}
	t := time.Date(y, time.Month(m), d, 12, 0, 0, 0, time.UTC)
	if t.Year() != y || int(t.Month()) != m || t.Day() != d {
		return 0, 0, 0, false
	}
	return y, time.Month(m), d, true
}

const (
	zoneLocal  = "Local"
	zoneEtcUTC = "Etc/UTC"
	zoneEtcGMT = "Etc/GMT"
)

// resolveLocalZoned attempts native Go zone resolution when available (non-WASM hosts or UTC).
func resolveLocalZoned(y int, m time.Month, d, hr, min, sec int, zone string) (unixMs int64, dstGap bool, tzAvailable bool, err error) {
	if zone == "" || zone == zoneLocal {
		return 0, false, true, errInvalidZone
	}
	canon := CanonicalZone(zone)
	var loc *time.Location
	if canon == zoneEtcUTC || canon == zoneEtcGMT {
		loc = time.UTC
	} else {
		var loadErr error
		loc, loadErr = loadIANALocation(canon)
		if loadErr != nil {
			// On WASM without tzdata, LoadLocation fails for IANA zones; caller relies on shell resolution.
			if !hasNativeTZData() && strings.Contains(zone, "/") {
				return 0, false, false, nil
			}
			return 0, false, true, loadErr
		}
	}
	// Scan a 36h window around UTC nominal date so we match the earliest occurrence on fall-back
	// and detect nonexistent wall times on spring-forward (same rule as internal/schedule.CalendarOn).
	targetWall := y*10000000000 + int(m)*100000000 + d*1000000 + hr*10000 + min*100 + sec
	base := time.Date(y, m, d, hr, min, sec, 0, loc)
	if base.Year() == y && base.Month() == m && base.Day() == d && base.Hour() == hr && base.Minute() == min && base.Second() == sec {
		// Check 1 hour earlier for DST fall-back fold so we always pick the earlier occurrence.
		earlier := base.Add(-time.Hour)
		if earlier.Year() == y && earlier.Month() == m && earlier.Day() == d && earlier.Hour() == hr && earlier.Minute() == min && earlier.Second() == sec {
			return earlier.UnixMilli(), false, true, nil
		}
		return base.UnixMilli(), false, true, nil
	}
	_ = targetWall
	return 0, true, true, nil
}

// BuildTrigger validates TimingDraft and produces a Trigger JSON model or a localizable error Text.
func (d TimingDraft) BuildTrigger(in TimingBuildInput) (Trigger, *corev1.Text) {
	// Validate timezone when needed (calendar/once; or if shell explicitly flagged invalid).
	zone := strings.TrimSpace(d.Zone)
	if in.ZoneValid != nil && !*in.ZoneValid {
		return Trigger{}, text.Msg(text.KeyScheduleIosChooseAValidIanaTimeZone)
	}
	if d.Kind != TimingInterval && d.Kind != TimingAfterTurn {
		if zone == "" || zone == "Local" {
			return Trigger{}, text.Msg(text.KeyScheduleIosChooseAValidIanaTimeZone)
		}
		if in.ZoneValid == nil {
			canon := CanonicalZone(zone)
			if canon != "Etc/UTC" && canon != "Etc/GMT" {
				if _, err := loadIANALocation(canon); err != nil && (hasNativeTZData() || !strings.Contains(zone, "/")) {
					return Trigger{}, text.Msg(text.KeyScheduleIosChooseAValidIanaTimeZone)
				}
			}
		}
	}

	var out Trigger
	switch d.Kind {
	case TimingOnce:
		hr, min, okTime := parseHHMM(d.Time)
		if !okTime {
			return Trigger{}, text.Msg(text.KeyScheduleSelectATime)
		}
		y, mo, day, okDate := parseYMD(d.Date)
		if !okDate {
			return Trigger{}, text.Msg(text.KeyScheduleChooseAValidDateAndTime)
		}
		if in.AtDSTGap {
			return Trigger{}, text.Msg(text.KeyScheduleDaylightSavingTimeSkipsThisTimeInTheSelected)
		}
		var atMs int64
		if in.AtMs != nil {
			atMs = *in.AtMs
		} else {
			ms, gap, avail, err := resolveLocalZoned(y, mo, day, hr, min, 0, zone)
			if err != nil {
				return Trigger{}, text.Msg(text.KeyScheduleIosChooseAValidIanaTimeZone)
			}
			if gap {
				return Trigger{}, text.Msg(text.KeyScheduleDaylightSavingTimeSkipsThisTimeInTheSelected)
			}
			if !avail {
				return Trigger{}, text.Msg(text.KeyScheduleChooseAValidDateAndTime)
			}
			atMs = ms
		}
		if in.NowMs > 0 && atMs <= in.NowMs {
			return Trigger{}, text.Msg(text.KeyScheduleSelectAFutureTime)
		}
		out = Trigger{
			Type: "at",
			At:   FormatRFC3339Ms(atMs),
		}
		return out, nil

	case TimingDaily, TimingWeekly:
		hr, min, okTime := parseHHMM(d.Time)
		if !okTime {
			return Trigger{}, text.Msg(text.KeyScheduleSelectATime)
		}
		clock := twoDigits(hr) + ":" + twoDigits(min)
		var days []int
		if d.Kind == TimingDaily {
			if d.Workdays {
				days = []int{1, 2, 3, 4, 5}
			}
		} else {
			days = uniqueSortedWeekdays(d.Weekdays)
			if len(days) == 0 {
				return Trigger{}, text.Msg(text.KeyScheduleSelectAtLeastOneDay)
			}
		}
		out = Trigger{
			Type:     "calendar",
			Time:     clock,
			Timezone: zone,
			Weekdays: days,
		}
		if d.JitterMinutes > 0 {
			out.JitterMinutes = d.JitterMinutes
			if room := int64(23*60 + 59 - (hr*60 + min)); out.JitterMinutes > room {
				out.JitterMinutes = room
			}
		}

	case TimingInterval:
		n, err := strconv.ParseInt(strings.TrimSpace(d.Interval), 10, 64)
		if err != nil || n <= 0 {
			return Trigger{}, text.Msg(text.KeyScheduleIosEnterAValidInterval)
		}
		mult := unitSeconds(d.Unit)
		if n > math.MaxInt64/mult {
			return Trigger{}, text.Msg(text.KeyScheduleIosEnterAValidInterval)
		}
		secs := n * mult
		if secs < 1 {
			return Trigger{}, text.Msg(text.KeyScheduleTheIntervalMustBeAtLeast1Second)
		}
		out = Trigger{
			Type:            "interval",
			IntervalSeconds: secs,
		}
		if d.Original != nil && d.Original.Type == "interval" && d.Original.IntervalSeconds == secs {
			out.At = d.Original.At
			out.DelaySeconds = d.Original.DelaySeconds
		} else {
			out.DelaySeconds = secs
		}

	case TimingAfterTurn:
		if d.Original != nil && d.Original.Type == "after_turn" {
			out = *d.Original
			out.Weekdays = nil
			out.JitterMinutes = 0
		} else {
			return Trigger{}, text.Msg(text.KeyScheduleAndroidChooseATimeType)
		}

	default:
		return Trigger{}, text.Msg(text.KeyScheduleAndroidChooseATimeType)
	}

	// Apply recurrence limits (never for one-shot "at").
	endSameAsOriginal := d.Original != nil && d.End == d.InitialEnd
	if endSameAsOriginal {
		out.MaxRuns = d.Original.MaxRuns
		out.Until = d.Original.Until
	}

	switch d.End {
	case EndCount:
		if !endSameAsOriginal || strings.TrimSpace(d.Count) != strings.TrimSpace(d.InitialCount) {
			cnt, err := strconv.ParseInt(strings.TrimSpace(d.Count), 10, 64)
			if err != nil || cnt <= 0 {
				return Trigger{}, text.Msg(text.KeyScheduleTheRunCountMustBeAPositiveInteger)
			}
			out.MaxRuns = cnt
			out.Until = ""
		}
	case EndDate:
		untilChanged := !endSameAsOriginal || d.UntilEdited ||
			(d.UntilMs > 0 && d.UntilMs != d.InitialUntilMs) ||
			(d.UntilMs == 0 && d.UntilDate != d.InitialUntilDate)
		if untilChanged || out.Until == "" {
			if in.UntilDSTGap {
				return Trigger{}, text.Msg(text.KeyScheduleDaylightSavingTimeSkipsThisTimeInTheSelected)
			}
			var uMs int64
			if in.UntilMs != nil {
				uMs = *in.UntilMs
			} else if d.UntilMs > 0 && !d.UntilEdited {
				uMs = d.UntilMs
			} else {
				y, mo, day, okDate := parseYMD(d.UntilDate)
				if !okDate {
					return Trigger{}, text.Msg(text.KeyScheduleChooseAValidDateAndTime)
				}
				ms, gap, avail, err := resolveLocalZoned(y, mo, day, 23, 59, 59, zone)
				if err != nil {
					return Trigger{}, text.Msg(text.KeyScheduleIosChooseAValidIanaTimeZone)
				}
				if gap {
					return Trigger{}, text.Msg(text.KeyScheduleDaylightSavingTimeSkipsThisTimeInTheSelected)
				}
				if !avail {
					return Trigger{}, text.Msg(text.KeyScheduleChooseAValidDateAndTime)
				}
				uMs = ms
			}
			if in.NowMs > 0 && uMs <= in.NowMs {
				return Trigger{}, text.Msg(text.KeyScheduleAndroidTheEndDateIsInThePast)
			}
			out.Until = FormatRFC3339Ms(uMs)
			out.MaxRuns = 0
		}
	case EndNever:
		out.MaxRuns = 0
		out.Until = ""
	}

	return out, nil
}
