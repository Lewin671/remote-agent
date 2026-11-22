package schedule

import (
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// DecodeTaskList parses {"tasks": [...]} from a ScheduleResponse.result_json payload without reflection.
func DecodeTaskList(raw []byte) ([]*Task, error) {
	p := jsonParser{b: raw}
	p.skipWS()
	if p.eof() {
		return nil, errEmptyListResponse
	}
	if !p.consume('{') {
		return nil, errMalformedJSON
	}
	hasTasks := false
	var tasks []*Task
	p.skipWS()
	if !p.consume('}') {
		for {
			key, err := p.readString()
			if err != nil {
				return nil, err
			}
			p.skipWS()
			if !p.consume(':') {
				return nil, errMalformedJSON
			}
			p.skipWS()
			if key == "tasks" {
				hasTasks = true
				parsed, err := p.readTaskArray()
				if err != nil {
					return nil, err
				}
				tasks = parsed
			} else if err := p.skipValue(); err != nil {
				return nil, err
			}
			p.skipWS()
			if p.consume('}') {
				break
			}
			if !p.consume(',') {
				return nil, errMalformedJSON
			}
			p.skipWS()
		}
	}
	p.skipWS()
	if !p.eof() {
		return nil, errMalformedJSON
	}
	if !hasTasks {
		return nil, errMissingTasksField
	}
	out := make([]*Task, 0, len(tasks))
	for _, t := range tasks {
		if t != nil && t.ID != "" {
			out = append(out, t)
		}
	}
	return out, nil
}

// DecodeTask parses a single Task JSON object from a mutation ScheduleResponse.result_json without reflection.
func DecodeTask(raw []byte) (*Task, error) {
	p := jsonParser{b: raw}
	p.skipWS()
	if p.eof() {
		return nil, nil
	}
	t, err := p.readTaskNullable()
	if err != nil {
		return nil, err
	}
	p.skipWS()
	if !p.eof() {
		return nil, errTrailingTaskJSON
	}
	if t == nil || t.ID == "" {
		return nil, nil
	}
	return t, nil
}

func appendJSONKey(b []byte, first *bool, k string) []byte {
	if !*first {
		b = append(b, ',')
	}
	*first = false
	b = appendJSONString(b, k)
	return append(b, ':')
}

func appendJSONStrField(b []byte, first *bool, k, v string, omitEmpty bool) []byte {
	if omitEmpty && v == "" {
		return b
	}
	return appendJSONString(appendJSONKey(b, first, k), v)
}

// EncodeSpec serializes a Spec to compact UTF-8 JSON without reflection.
func EncodeSpec(s Spec) ([]byte, error) {
	b := make([]byte, 0, 256)
	b = append(b, '{')
	first := true
	if s.Notify != "" {
		b = appendJSONString(appendJSONKey(b, &first, "notify"), s.Notify)
	}
	b = appendJSONString(appendJSONKey(b, &first, "title"), s.Title)
	b = appendJSONString(appendJSONKey(b, &first, "message"), s.Message)
	b = appendTriggerJSON(appendJSONKey(b, &first, "trigger"), s.Trigger)
	b = appendTargetJSON(appendJSONKey(b, &first, "target"), s.Target)
	if s.LateSeconds != 0 {
		b = strconv.AppendInt(appendJSONKey(b, &first, "late_seconds"), s.LateSeconds, 10)
	}
	return append(b, '}'), nil
}

func appendJSONString(b []byte, s string) []byte {
	b = append(b, '"')
	for i := 0; i < len(s); {
		r, width := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && width == 1 {
			b = append(b, `\ufffd`...)
			i++
			continue
		}
		switch r {
		case '"':
			b = append(b, `\"`...)
		case '\\':
			b = append(b, `\\`...)
		case '\n':
			b = append(b, `\n`...)
		case '\r':
			b = append(b, `\r`...)
		case '\t':
			b = append(b, `\t`...)
		default:
			if r < 0x20 {
				b = append(b, `\u00`...)
				const hex = "0123456789abcdef"
				b = append(b, hex[r>>4], hex[r&0x0f])
			} else {
				b = append(b, s[i:i+width]...)
			}
		}
		i += width
	}
	return append(b, '"')
}

func appendTriggerJSON(b []byte, tr Trigger) []byte {
	b = append(b, '{')
	first := true
	if tr.Time != "" {
		b = appendJSONString(appendJSONKey(b, &first, "time"), tr.Time)
	}
	if len(tr.Weekdays) > 0 {
		b = append(appendJSONKey(b, &first, "weekdays"), '[')
		for i, wd := range tr.Weekdays {
			if i > 0 {
				b = append(b, ',')
			}
			b = strconv.AppendInt(b, int64(wd), 10)
		}
		b = append(b, ']')
	}
	if tr.Timezone != "" {
		b = appendJSONString(appendJSONKey(b, &first, "timezone"), tr.Timezone)
	}
	if tr.Until != "" {
		b = appendJSONString(appendJSONKey(b, &first, "until"), tr.Until)
	}
	b = appendJSONString(appendJSONKey(b, &first, "type"), tr.Type)
	if tr.At != "" {
		b = appendJSONString(appendJSONKey(b, &first, "at"), tr.At)
	}
	if tr.DelaySeconds != 0 {
		b = strconv.AppendInt(appendJSONKey(b, &first, "delay_seconds"), tr.DelaySeconds, 10)
	}
	if tr.IntervalSeconds != 0 {
		b = strconv.AppendInt(appendJSONKey(b, &first, "interval_seconds"), tr.IntervalSeconds, 10)
	}
	if tr.Repeat {
		b = append(appendJSONKey(b, &first, "repeat"), "true"...)
	}
	if tr.MaxRuns != 0 {
		b = strconv.AppendInt(appendJSONKey(b, &first, "max_runs"), tr.MaxRuns, 10)
	}
	return append(b, '}')
}

func appendTargetJSON(b []byte, tg Target) []byte {
	b = append(b, '{')
	first := true
	b = appendJSONStrField(b, &first, "type", tg.Type, false)
	b = appendJSONStrField(b, &first, "session", tg.Session, true)
	b = appendJSONStrField(b, &first, "cwd", tg.Cwd, true)
	b = appendJSONStrField(b, &first, "agent", tg.Agent, true)
	b = appendJSONStrField(b, &first, "model", tg.Model, true)
	b = appendJSONStrField(b, &first, "effort", tg.Effort, true)
	b = appendJSONStrField(b, &first, "mode", tg.Mode, true)
	return append(b, '}')
}

type jsonParser struct {
	b []byte
	i int
}

func (p *jsonParser) eof() bool { return p.i >= len(p.b) }

func (p *jsonParser) skipWS() {
	for p.i < len(p.b) {
		switch p.b[p.i] {
		case ' ', '\t', '\r', '\n':
			p.i++
		default:
			return
		}
	}
}

func (p *jsonParser) consume(c byte) bool {
	if p.i < len(p.b) && p.b[p.i] == c {
		p.i++
		return true
	}
	return false
}

func (p *jsonParser) consumeNull() bool {
	if p.i+4 <= len(p.b) && string(p.b[p.i:p.i+4]) == "null" {
		p.i += 4
		return true
	}
	return false
}

func (p *jsonParser) readString() (string, error) {
	if !p.consume('"') {
		return "", errMalformedJSON
	}
	start := p.i
	hasEscape := false
	for p.i < len(p.b) {
		c := p.b[p.i]
		if c == '"' {
			raw := p.b[start:p.i]
			p.i++
			if !hasEscape {
				return string(raw), nil
			}
			return unescapeJSONString(raw)
		}
		if c == '\\' {
			hasEscape = true
			p.i += 2
			if p.i > len(p.b) {
				return "", errMalformedJSON
			}
			continue
		}
		if c < 0x20 {
			return "", errMalformedJSON
		}
		p.i++
	}
	return "", errMalformedJSON
}

func (p *jsonParser) readNullableString() (string, error) {
	if p.consumeNull() {
		return "", nil
	}
	return p.readString()
}

func unescapeJSONString(raw []byte) (string, error) {
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); {
		if raw[i] != '\\' {
			out = append(out, raw[i])
			i++
			continue
		}
		i++
		if i >= len(raw) {
			return "", errMalformedJSON
		}
		switch raw[i] {
		case '"', '\\', '/':
			out = append(out, raw[i])
			i++
		case 'b':
			out = append(out, '\b')
			i++
		case 'f':
			out = append(out, '\f')
			i++
		case 'n':
			out = append(out, '\n')
			i++
		case 'r':
			out = append(out, '\r')
			i++
		case 't':
			out = append(out, '\t')
			i++
		case 'u':
			if i+5 > len(raw) {
				return "", errMalformedJSON
			}
			v, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
			if err != nil {
				return "", errMalformedJSON
			}
			i += 5
			r := rune(v)
			if utf16.IsSurrogate(r) && i+6 <= len(raw) && raw[i] == '\\' && raw[i+1] == 'u' {
				v2, err := strconv.ParseUint(string(raw[i+2:i+6]), 16, 16)
				if err == nil {
					r = utf16.DecodeRune(r, rune(v2))
					i += 6
				}
			}
			out = utf8.AppendRune(out, r)
		default:
			return "", errMalformedJSON
		}
	}
	return string(out), nil
}

func (p *jsonParser) readBool() (bool, error) {
	if p.i+4 <= len(p.b) && string(p.b[p.i:p.i+4]) == "true" {
		p.i += 4
		return true, nil
	}
	if p.i+5 <= len(p.b) && string(p.b[p.i:p.i+5]) == "false" {
		p.i += 5
		return false, nil
	}
	return false, errMalformedJSON
}

func (p *jsonParser) readNumberToken() (string, error) {
	start := p.i
	if p.i < len(p.b) && p.b[p.i] == '-' {
		p.i++
	}
	digits := 0
	for p.i < len(p.b) && p.b[p.i] >= '0' && p.b[p.i] <= '9' {
		p.i++
		digits++
	}
	if digits == 0 {
		return "", errMalformedJSON
	}
	if p.i < len(p.b) && p.b[p.i] == '.' {
		p.i++
		frac := 0
		for p.i < len(p.b) && p.b[p.i] >= '0' && p.b[p.i] <= '9' {
			p.i++
			frac++
		}
		if frac == 0 {
			return "", errMalformedJSON
		}
	}
	if p.i < len(p.b) && (p.b[p.i] == 'e' || p.b[p.i] == 'E') {
		p.i++
		if p.i < len(p.b) && (p.b[p.i] == '+' || p.b[p.i] == '-') {
			p.i++
		}
		exp := 0
		for p.i < len(p.b) && p.b[p.i] >= '0' && p.b[p.i] <= '9' {
			p.i++
			exp++
		}
		if exp == 0 {
			return "", errMalformedJSON
		}
	}
	return string(p.b[start:p.i]), nil
}

func (p *jsonParser) readInt64() (int64, error) {
	tok, err := p.readNumberToken()
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseInt(tok, 10, 64)
	if err != nil {
		return 0, errMalformedJSON
	}
	return v, nil
}

func (p *jsonParser) readUint64() (uint64, error) {
	tok, err := p.readNumberToken()
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseUint(tok, 10, 64)
	if err != nil {
		return 0, errMalformedJSON
	}
	return v, nil
}

func (p *jsonParser) readInt() (int, error) {
	v, err := p.readInt64()
	return int(v), err
}

func (p *jsonParser) skipValue() error {
	p.skipWS()
	if p.eof() {
		return errMalformedJSON
	}
	switch p.b[p.i] {
	case '"':
		_, err := p.readString()
		return err
	case 't', 'f':
		_, err := p.readBool()
		return err
	case 'n':
		if p.consumeNull() {
			return nil
		}
		return errMalformedJSON
	case '{':
		p.i++
		p.skipWS()
		if p.consume('}') {
			return nil
		}
		for {
			if _, err := p.readString(); err != nil {
				return err
			}
			p.skipWS()
			if !p.consume(':') {
				return errMalformedJSON
			}
			if err := p.skipValue(); err != nil {
				return err
			}
			p.skipWS()
			if p.consume('}') {
				return nil
			}
			if !p.consume(',') {
				return errMalformedJSON
			}
			p.skipWS()
		}
	case '[':
		p.i++
		p.skipWS()
		if p.consume(']') {
			return nil
		}
		for {
			if err := p.skipValue(); err != nil {
				return err
			}
			p.skipWS()
			if p.consume(']') {
				return nil
			}
			if !p.consume(',') {
				return errMalformedJSON
			}
			p.skipWS()
		}
	default:
		_, err := p.readNumberToken()
		return err
	}
}

func (p *jsonParser) beginObject() (bool, error) {
	if !p.consume('{') {
		return false, errMalformedJSON
	}
	p.skipWS()
	return !p.consume('}'), nil
}

func (p *jsonParser) nextObjectKey(first *bool) (string, bool, error) {
	if *first {
		*first = false
	} else {
		p.skipWS()
		if p.consume('}') {
			return "", false, nil
		}
		if !p.consume(',') {
			return "", false, errMalformedJSON
		}
		p.skipWS()
	}
	key, err := p.readString()
	if err != nil {
		return "", false, err
	}
	p.skipWS()
	if !p.consume(':') {
		return "", false, errMalformedJSON
	}
	p.skipWS()
	return key, true, nil
}

func (p *jsonParser) readTaskArray() ([]*Task, error) {
	if p.consumeNull() {
		return nil, nil
	}
	if !p.consume('[') {
		return nil, errMalformedJSON
	}
	p.skipWS()
	if p.consume(']') {
		return nil, nil
	}
	var out []*Task
	for {
		t, err := p.readTaskNullable()
		if err != nil {
			return nil, err
		}
		out = append(out, t)
		p.skipWS()
		if p.consume(']') {
			return out, nil
		}
		if !p.consume(',') {
			return nil, errMalformedJSON
		}
		p.skipWS()
	}
}

func (p *jsonParser) readTaskNullable() (*Task, error) {
	if p.consumeNull() {
		return nil, nil
	}
	open, err := p.beginObject()
	if err != nil {
		return nil, err
	}
	var t Task
	if !open {
		return &t, nil
	}
	first := true
	for {
		key, more, err := p.nextObjectKey(&first)
		if err != nil {
			return nil, err
		}
		if !more {
			return &t, nil
		}
		switch key {
		case "takeover":
			t.Takeover, err = p.readTakeoverNullable()
		case "interval_phase":
			t.IntervalPhase, err = p.readNullableString()
		case "manual_request":
			t.ManualRequest, err = p.readManualRequestNullable()
		case "id":
			t.ID, err = p.readNullableString()
		case "revision":
			t.Revision, err = p.readUint64()
		case "spec":
			t.Spec, err = p.readSpec()
		case "status":
			t.Status, err = p.readNullableString()
		case "reason":
			t.Reason, err = p.readNullableString()
		case "created_at":
			t.CreatedAt, err = p.readNullableString()
		case "updated_at":
			t.UpdatedAt, err = p.readNullableString()
		case "next_run_at":
			t.NextRunAt, err = p.readNullableString()
		case "scoped_session":
			t.ScopedSession, err = p.readNullableString()
		case "scoped_mode":
			t.ScopedMode, err = p.readNullableString()
		case "source":
			t.Source, err = p.readNullableString()
		case "dispatches":
			t.Dispatches, err = p.readInt64()
		case "runs":
			t.Runs, err = p.readRuns()
		case "active_run":
			t.Active, err = p.readRunNullable()
		case "creation_key":
			t.CreationKey, err = p.readNullableString()
		default:
			err = p.skipValue()
		}
		if err != nil {
			return nil, err
		}
	}
}

func (p *jsonParser) readTakeoverNullable() (*Takeover, error) {
	if p.consumeNull() {
		return nil, nil
	}
	open, err := p.beginObject()
	if err != nil {
		return nil, err
	}
	var tk Takeover
	if !open {
		return &tk, nil
	}
	first := true
	for {
		key, more, err := p.nextObjectKey(&first)
		if err != nil {
			return nil, err
		}
		if !more {
			return &tk, nil
		}
		switch key {
		case "session":
			tk.Session, err = p.readNullableString()
		case "via":
			tk.Via, err = p.readNullableString()
		default:
			err = p.skipValue()
		}
		if err != nil {
			return nil, err
		}
	}
}

func (p *jsonParser) readManualRequestNullable() (*ManualRequest, error) {
	if p.consumeNull() {
		return nil, nil
	}
	open, err := p.beginObject()
	if err != nil {
		return nil, err
	}
	var mr ManualRequest
	if !open {
		return &mr, nil
	}
	first := true
	for {
		key, more, err := p.nextObjectKey(&first)
		if err != nil {
			return nil, err
		}
		if !more {
			return &mr, nil
		}
		switch key {
		case "request_id":
			mr.RequestID, err = p.readNullableString()
		case "requested_at":
			mr.RequestedAt, err = p.readNullableString()
		default:
			err = p.skipValue()
		}
		if err != nil {
			return nil, err
		}
	}
}

func (p *jsonParser) readSpec() (Spec, error) {
	var s Spec
	open, err := p.beginObject()
	if err != nil || !open {
		return s, err
	}
	first := true
	for {
		key, more, err := p.nextObjectKey(&first)
		if err != nil || !more {
			return s, err
		}
		switch key {
		case "notify":
			s.Notify, err = p.readNullableString()
		case "title":
			s.Title, err = p.readNullableString()
		case "message":
			s.Message, err = p.readNullableString()
		case "trigger":
			s.Trigger, err = p.readTrigger()
		case "target":
			s.Target, err = p.readTarget()
		case "late_seconds":
			s.LateSeconds, err = p.readInt64()
		default:
			err = p.skipValue()
		}
		if err != nil {
			return s, err
		}
	}
}

func (p *jsonParser) readTrigger() (Trigger, error) {
	var tr Trigger
	open, err := p.beginObject()
	if err != nil || !open {
		return tr, err
	}
	first := true
	for {
		key, more, err := p.nextObjectKey(&first)
		if err != nil || !more {
			return tr, err
		}
		switch key {
		case "time":
			tr.Time, err = p.readNullableString()
		case "weekdays":
			tr.Weekdays, err = p.readIntArray()
		case "timezone":
			tr.Timezone, err = p.readNullableString()
		case "until":
			tr.Until, err = p.readNullableString()
		case "type":
			tr.Type, err = p.readNullableString()
		case "at":
			tr.At, err = p.readNullableString()
		case "delay_seconds":
			tr.DelaySeconds, err = p.readInt64()
		case "interval_seconds":
			tr.IntervalSeconds, err = p.readInt64()
		case "repeat":
			tr.Repeat, err = p.readBool()
		case "max_runs":
			tr.MaxRuns, err = p.readInt64()
		default:
			err = p.skipValue()
		}
		if err != nil {
			return tr, err
		}
	}
}

func (p *jsonParser) readTarget() (Target, error) {
	var tg Target
	open, err := p.beginObject()
	if err != nil || !open {
		return tg, err
	}
	first := true
	for {
		key, more, err := p.nextObjectKey(&first)
		if err != nil || !more {
			return tg, err
		}
		switch key {
		case "type":
			tg.Type, err = p.readNullableString()
		case "session":
			tg.Session, err = p.readNullableString()
		case "cwd":
			tg.Cwd, err = p.readNullableString()
		case "agent":
			tg.Agent, err = p.readNullableString()
		case "model":
			tg.Model, err = p.readNullableString()
		case "effort":
			tg.Effort, err = p.readNullableString()
		case "mode":
			tg.Mode, err = p.readNullableString()
		default:
			err = p.skipValue()
		}
		if err != nil {
			return tg, err
		}
	}
}

func (p *jsonParser) readIntArray() ([]int, error) {
	if p.consumeNull() {
		return nil, nil
	}
	if !p.consume('[') {
		return nil, errMalformedJSON
	}
	p.skipWS()
	if p.consume(']') {
		return nil, nil
	}
	var out []int
	for {
		n, err := p.readInt()
		if err != nil {
			return nil, err
		}
		out = append(out, n)
		p.skipWS()
		if p.consume(']') {
			return out, nil
		}
		if !p.consume(',') {
			return nil, errMalformedJSON
		}
		p.skipWS()
	}
}

func (p *jsonParser) readRuns() ([]Run, error) {
	if p.consumeNull() {
		return nil, nil
	}
	if !p.consume('[') {
		return nil, errMalformedJSON
	}
	p.skipWS()
	if p.consume(']') {
		return nil, nil
	}
	var out []Run
	for {
		r, err := p.readRunNullable()
		if err != nil {
			return nil, err
		}
		if r != nil {
			out = append(out, *r)
		}
		p.skipWS()
		if p.consume(']') {
			return out, nil
		}
		if !p.consume(',') {
			return nil, errMalformedJSON
		}
		p.skipWS()
	}
}

func (p *jsonParser) readRunNullable() (*Run, error) {
	if p.consumeNull() {
		return nil, nil
	}
	open, err := p.beginObject()
	if err != nil {
		return nil, err
	}
	var r Run
	if !open {
		return &r, nil
	}
	first := true
	for {
		key, more, err := p.nextObjectKey(&first)
		if err != nil {
			return nil, err
		}
		if !more {
			return &r, nil
		}
		switch key {
		case "notify_on_finish":
			r.NotifyOnFinish, err = p.readBool()
		case "notification_session_id":
			r.NotificationSessionID, err = p.readNullableString()
		case "notification_seq":
			r.NotificationSeq, err = p.readUint64()
		case "notification_source":
			r.NotificationSource, err = p.readNullableString()
		case "target_type":
			r.TargetType, err = p.readNullableString()
		case "task_title":
			r.TaskTitle, err = p.readNullableString()
		case "start_seq":
			r.StartSeq, err = p.readUint64()
		case "manual":
			r.Manual, err = p.readBool()
		case "notified":
			r.Notified, err = p.readBool()
		case "notification_text":
			r.NotificationText, err = p.readNullableString()
		case "final_summary":
			r.FinalSummary, err = p.readNullableString()
		case "id":
			r.ID, err = p.readNullableString()
		case "session_id":
			r.SessionID, err = p.readNullableString()
		case "request_id":
			r.RequestID, err = p.readNullableString()
		case "scheduled_at":
			r.ScheduledAt, err = p.readNullableString()
		case "started_at":
			r.StartedAt, err = p.readNullableString()
		case "ended_at":
			r.EndedAt, err = p.readNullableString()
		case "status":
			r.Status, err = p.readNullableString()
		case "reason":
			r.Reason, err = p.readNullableString()
		case "after_seq":
			r.AfterSeq, err = p.readUint64()
		case "input_paused":
			r.InputPaused, err = p.readBool()
		default:
			err = p.skipValue()
		}
		if err != nil {
			return nil, err
		}
	}
}
