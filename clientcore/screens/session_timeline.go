package screens

import (
	"slices"
	"strconv"
	"strings"

	"github.com/Lewin671/remote-agent/clientcore/domain/images"
	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/projection"
	"github.com/Lewin671/remote-agent/clientcore/domain/sharedfiles"
	"github.com/Lewin671/remote-agent/clientcore/domain/usage"
	"github.com/Lewin671/remote-agent/clientcore/domain/world"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

const toolStatusFailed = "failed"

type timelineCacheKey struct {
	eventRev            uint64
	eventsLen           int
	firstSeq            uint64
	lastSeq             uint64
	cwd                 string
	home                string
	machineName         string
	supportsSharedFiles bool
	sharedFileOnline    bool
	sharedFilesRev      uint64
	uiLocale            string
	sessionExited       bool
	endedAtMs           int64
	agent               v1.Agent
	thisDeviceID        string
	deviceSig           uint64
	outgoingSig         uint64
	inputsSig           uint64
	unavailableCount    uint32
	// Receipts write the date unless the push went out today.
	todayStartMs int64
}

type timelineCache struct {
	key       timelineCacheKey
	hasFork   bool
	rawItems  []*corev1.SessionTimelineItem
	spans     []quietSpan
	refsBySeq map[uint64][]*v1.ImageRef
}

const (
	fnvOffset64 uint64 = 14695981039346656037
	fnvPrime64  uint64 = 1099511628211
)

func mixUint64(h, v uint64) uint64 {
	return (h ^ v) * fnvPrime64
}

func mixString(h uint64, s string) uint64 {
	h = mixUint64(h, uint64(len(s)))
	for i := 0; i < len(s); i++ {
		h = (h ^ uint64(s[i])) * fnvPrime64
	}
	return h
}

func computeTimelineCacheKey(
	in world.SessionSnapshot,
	events []*v1.Event,
	p *corev1.ProjectionState,
	caps lists.AgentCapabilities,
	cwd, home, machineName string,
	supportsSharedFiles, sharedFileOnline, sessionExited bool,
	endedAtMs int64,
	uiLocale string,
	ui *SessionUIState,
) timelineCacheKey {
	var firstSeq, lastSeq uint64
	if len(events) > 0 {
		if events[0] != nil {
			firstSeq = events[0].Seq
		}
		if events[len(events)-1] != nil {
			lastSeq = events[len(events)-1].Seq
		}
	}

	devSig := fnvOffset64 ^ uint64(len(in.DeviceNames))
	for id, name := range in.DeviceNames {
		devSig ^= mixString(mixString(fnvOffset64, id), name)
	}

	inputsSig := mixUint64(fnvOffset64, uint64(len(p.Inputs)))
	for _, ip := range p.Inputs {
		if ip != nil {
			inputsSig = mixUint64(mixUint64(inputsSig, ip.LastSeq), uint64(ip.Status))
			inputsSig = mixString(inputsSig, ip.RequestId)
			if ip.Accepted != nil {
				inputsSig = mixString(inputsSig, ip.Accepted.Text)
				if ip.Accepted.Origin != nil {
					inputsSig = mixString(inputsSig, ip.Accepted.Origin.RequestId)
				}
			}
			if ip.Detail != nil {
				inputsSig = mixString(mixString(inputsSig, ip.Detail.Text), ip.Detail.Reason)
			}
		}
	}

	outgoingSig := fnvOffset64
	if ui != nil {
		outgoingSig = mixUint64(fnvOffset64, uint64(len(ui.Outgoing)))
		for _, og := range ui.Outgoing {
			if og != nil {
				outgoingSig = mixString(mixString(outgoingSig, og.RequestID), og.Text)
			}
		}
	}

	var sharedFilesRev uint64
	if ui != nil {
		sharedFilesRev = ui.SharedFilesRev
	}
	return timelineCacheKey{
		eventRev:            in.EventRev,
		eventsLen:           len(events),
		firstSeq:            firstSeq,
		lastSeq:             lastSeq,
		cwd:                 cwd,
		home:                home,
		machineName:         machineName,
		supportsSharedFiles: supportsSharedFiles,
		sharedFileOnline:    sharedFileOnline,
		sharedFilesRev:      sharedFilesRev,
		uiLocale:            uiLocale,
		sessionExited:       sessionExited,
		endedAtMs:           endedAtMs,
		agent:               caps.Agent,
		thisDeviceID:        in.ThisDeviceID,
		deviceSig:           devSig,
		outgoingSig:         outgoingSig,
		inputsSig:           inputsSig,
		unavailableCount:    in.UnavailableCount,
	}
}

// FindStartPromptInput locates the InputProjection belonging to ui.PendingStartPrompt.
func FindStartPromptInput(p *corev1.ProjectionState, ui *SessionUIState) *corev1.InputProjection {
	if p == nil || ui == nil || ui.PendingStartPrompt == nil || len(p.Inputs) == 0 {
		return nil
	}
	if ui.PendingStartPrompt.RequestID != "" {
		for _, ip := range p.Inputs {
			if ip != nil && ip.GetAccepted().GetOrigin().GetRequestId() == ui.PendingStartPrompt.RequestID {
				return ip
			}
		}
	}
	localIDs := make(map[string]bool, len(ui.Outgoing))
	for _, og := range ui.Outgoing {
		if og != nil && og.RequestID != "" {
			localIDs[og.RequestID] = true
		}
	}
	for _, ip := range p.Inputs {
		if ip == nil || ip.RequestId == "" || localIDs[ip.RequestId] {
			continue
		}
		if origReq := ip.GetAccepted().GetOrigin().GetRequestId(); origReq != "" && ui.PendingStartPrompt.RequestID != "" {
			continue
		}
		return ip
	}
	return nil
}

func findEventBySeq(events []*v1.Event, seq uint64) *v1.Event {
	if seq == 0 || len(events) == 0 || events[0] == nil || seq < events[0].Seq {
		return nil
	}
	idx := int(seq - events[0].Seq)
	if idx >= len(events) {
		return nil
	}
	if e := events[idx]; e != nil && e.Seq == seq {
		return e
	}
	return nil
}

func runningToolFromStarted(e *v1.Event) *corev1.RunningTool {
	if e == nil {
		return nil
	}
	ts := e.GetToolStarted()
	if ts == nil {
		return nil
	}
	return &corev1.RunningTool{
		Title:       ts.Title,
		CallId:      ts.CallId,
		SinceMs:     projection.EventTimeMs(e),
		Kind:        ts.Kind,
		ItemType:    ts.ItemType,
		Seq:         e.Seq,
		Input:       ts.Input,
		InputDigest: ts.InputDigest,
	}
}

type timelineToolCtx struct {
	in                  world.SessionSnapshot
	st                  *State
	p                   *corev1.ProjectionState
	bgBySeq             map[uint64]*corev1.BackgroundTool
	noResultSet         map[uint64]bool
	supportsSharedFiles bool
	sharedFileOnline    bool
	sessionExited       bool
	uiLocale            string
	localFiles          map[string]*sharedfiles.LocalState
	cwd                 string
	home                string
	machineName         string
}

type timelineToolRun struct {
	items  []*corev1.SessionToolCallItem
	seq    uint64
	endSeq uint64
	ts     int64
	turnID string
	key    string
}

func (r *timelineToolRun) flush(rawItems *[]*corev1.SessionTimelineItem) {
	if len(r.items) == 0 {
		return
	}
	if len(r.items) == 1 {
		*rawItems = append(*rawItems, &corev1.SessionTimelineItem{
			Id:          r.key,
			Seq:         r.seq,
			EndSeq:      r.seq,
			TimestampMs: r.ts,
			TurnId:      r.turnID,
			Kind:        &corev1.SessionTimelineItem_ToolCall{ToolCall: r.items[0]},
		})
	} else {
		grp := buildToolGroupItem(r.items)
		*rawItems = append(*rawItems, &corev1.SessionTimelineItem{
			Id:          r.key,
			Seq:         r.seq,
			EndSeq:      r.endSeq,
			TimestampMs: r.ts,
			TurnId:      r.turnID,
			Kind:        &corev1.SessionTimelineItem_ToolGroup{ToolGroup: grp},
		})
	}
	*r = timelineToolRun{}
}

func (r *timelineToolRun) add(tcCtx *timelineToolCtx, rawItems *[]*corev1.SessionTimelineItem, e *v1.Event, tc *v1.ToolCall) {
	if tc == nil {
		return
	}
	bg := tcCtx.bgBySeq[e.Seq]
	wentBg := bg != nil || (tc.Background != v1.ToolBackground_TOOL_BACKGROUND_UNSPECIFIED)
	if tcCtx.supportsSharedFiles && len(tc.SharedFiles) > 0 {
		r.flush(rawItems)
		*rawItems = append(*rawItems, &corev1.SessionTimelineItem{
			Id:          eventItemID(e.Seq),
			Seq:         e.Seq,
			EndSeq:      e.Seq,
			TimestampMs: projection.EventTimeMs(e),
			TurnId:      e.TurnId,
			Kind: &corev1.SessionTimelineItem_SharedFiles{
				SharedFiles: buildSharedFilesItem(
					tc.SharedFiles,
					tcCtx.localFiles,
					tcCtx.machineName,
					tcCtx.home,
					tcCtx.sharedFileOnline,
					tcCtx.sessionExited,
					tcCtx.uiLocale,
				),
			},
		})
		return
	}

	item := buildToolCallView(e, tc, tcCtx.cwd, tcCtx.home, tcCtx.noResultSet[e.Seq], wentBg, bg)
	if aid, ok := tcCtx.p.ApprovalCallIds[e.Seq]; ok {
		if ra := tcCtx.p.Resolved[aid]; ra != nil {
			r.flush(rawItems)
			item.DecisionLabel = approvalDecisionText(ra.Decision, ra.Message, ra.Origin, tcCtx.st, tcCtx.in.DeviceNames, tcCtx.in.ThisDeviceID, tcCtx.machineName)
			item.DecisionAllowed = ra.Decision == v1.ApprovalDecision_APPROVAL_DECISION_ALLOW || ra.Decision == v1.ApprovalDecision_APPROVAL_DECISION_ALLOW_ALWAYS
			*rawItems = append(*rawItems, &corev1.SessionTimelineItem{
				Id:          eventItemID(e.Seq),
				Seq:         e.Seq,
				EndSeq:      e.Seq,
				TimestampMs: projection.EventTimeMs(e),
				TurnId:      e.TurnId,
				Kind:        &corev1.SessionTimelineItem_ToolCall{ToolCall: item},
			})
			return
		}
	}
	if wentBg || item.Status != corev1.SessionToolStatus_SESSION_TOOL_STATUS_OK {
		r.flush(rawItems)
		*rawItems = append(*rawItems, &corev1.SessionTimelineItem{
			Id:          eventItemID(e.Seq),
			Seq:         e.Seq,
			EndSeq:      e.Seq,
			TimestampMs: projection.EventTimeMs(e),
			TurnId:      e.TurnId,
			Kind:        &corev1.SessionTimelineItem_ToolCall{ToolCall: item},
		})
		return
	}
	if len(r.items) == 0 {
		r.key = eventItemID(e.Seq)
		r.seq = e.Seq
		r.ts = projection.EventTimeMs(e)
		r.turnID = e.TurnId
	}
	r.items = append(r.items, item)
	r.endSeq = e.Seq
}

func buildSessionTimeline(
	in world.SessionSnapshot,
	st *State,
	ls *lists.Session,
	lm *lists.Machine,
	header *corev1.SessionHeaderView,
	caps lists.AgentCapabilities,
	win *corev1.SessionWindowRequest,
	ui *SessionUIState,
) *corev1.SessionTimelineView {
	out := &corev1.SessionTimelineView{
		Loading:              in.Loading,
		Loaded:               in.Loaded,
		AwaitingCache:        in.AwaitingCache,
		Backfilling:          in.Backfilling,
		Fetching:             in.Fetching,
		HasGap:               in.HasGap,
		Retired:              in.Retired,
		RetryingCorruptCache: in.RetryingCorruptCache,
		HistoryPartial:       in.UnavailableCount > 0 || in.Retired,
		UnavailableCount:     in.UnavailableCount,
		Error:                ReasonText(in.Error),
		FetchError:           ReasonText(in.FetchError),
		CanLoadEarlier:       in.HasGap && !in.Backfilling && !in.Fetching,
		TailRequested:        in.TailRequested,
		Offline:              in.Conn == lists.ConnOffline || in.Conn == lists.ConnNoNetwork,
	}

	if in.Retired {
		out.HistoryNotice = text.Msg(text.KeySessionHistoryUnreadable)
	} else if in.UnavailableCount > 0 {
		out.HistoryNotice = text.Msg(text.KeySessionHistoryUnavailable, text.Int("count", int64(in.UnavailableCount)))
	}

	if !in.Found {
		ui.ClearTimelineCache()
		return out
	}

	events := in.Events
	p := in.Projection
	if p == nil {
		p = &corev1.ProjectionState{}
	}

	var cwd, home string
	var sessionExited bool
	if ls != nil && ls.Info != nil {
		cwd = ls.Info.Cwd
		sessionExited = ls.Info.Status == v1.SessionStatus_SESSION_STATUS_EXITED
	}
	if lm != nil && lm.Meta != nil {
		home = lm.Meta.Home
	}
	supportsSharedFiles := lm != nil && lm.Meta != nil && lm.Meta.Capabilities != nil && lm.Meta.Capabilities.SharedFiles
	sharedFileOnline := header != nil && header.ComputerOnline && !header.ComputerUnlinked &&
		(in.Conn == lists.ConnConnected || (st != nil && st.DisplayGrace))
	uiLocale := ""
	if st != nil {
		uiLocale = usage.NormalizeUILocale(st.UILocale)
	}
	machineName := ""
	if lm != nil {
		machineName = lists.MachineName(lm, lm.ID)
	}
	endedAtMs := int64(0)
	if ls != nil {
		endedAtMs = lists.EndedMs(ls)
	}

	var cacheKey timelineCacheKey
	canUseCache := ui != nil && in.EventRev > 0
	if !canUseCache {
		ui.ClearTimelineCache()
	} else {
		cacheKey = computeTimelineCacheKey(
			in, events, p, caps, cwd, home, machineName,
			supportsSharedFiles, sharedFileOnline, sessionExited, endedAtMs, uiLocale, ui,
		)
		cacheKey.todayStartMs = st.Cal.TodayStartMs
		if ui.timeline != nil && !ui.timeline.hasFork && ui.timeline.key == cacheKey {
			folded := foldQuietScheduledItemsWithSpans(ui.timeline.rawItems, ui.timeline.spans, st.Cal, win)
			applyTimelineWindow(out, folded, win)
			out.ViewerImages = images.CollectViewerImages(folded, ui.timeline.refsBySeq, caps.Name, home)
			return out
		}
	}

	// Build lookup tables matching Android Chat.kt & iOS Timeline.swift.
	runningBySeq := make(map[uint64]*corev1.RunningTool, len(p.Tools))
	for _, t := range p.Tools {
		if t != nil {
			runningBySeq[t.Seq] = t
		}
	}
	unfinishedSet := make(map[uint64]bool, len(p.UnfinishedSeqs))
	for _, seq := range p.UnfinishedSeqs {
		unfinishedSet[seq] = true
	}
	noResultSet := make(map[uint64]bool, len(p.NoResultSeqs))
	for _, seq := range p.NoResultSeqs {
		noResultSet[seq] = true
	}
	typedLocallySet := make(map[uint64]bool, len(p.TypedLocallySeqs))
	for _, seq := range p.TypedLocallySeqs {
		typedLocallySet[seq] = true
	}
	unsentAt := make(map[uint64][]string)
	unsentIDs := make(map[string]bool, len(p.Unsent))
	for _, u := range p.Unsent {
		if u != nil {
			unsentAt[u.Seq] = append(unsentAt[u.Seq], u.RequestId)
			unsentIDs[u.RequestId] = true
		}
	}

	callAt := make(map[uint64]*v1.Event, len(p.CompletedAt))
	placed := make(map[uint64]bool, len(p.CompletedAt))
	for startSeq, callSeq := range p.CompletedAt {
		if ce := findEventBySeq(events, callSeq); ce != nil {
			callAt[startSeq] = ce
			placed[callSeq] = true
		}
	}

	// Approvals resolved onto tool calls vs standalone notes.
	mergedApprovals := make(map[string]bool, len(p.ApprovalCallIds))
	for _, aid := range p.ApprovalCallIds {
		mergedApprovals[aid] = true
	}

	bgBySeq := make(map[uint64]*corev1.BackgroundTool, len(p.Background))
	for _, bg := range p.Background {
		if bg != nil && bg.Tool != nil {
			bgBySeq[bg.Tool.Seq] = bg
		}
	}

	// Exited-only InputProjection notices (non-exited unsent/failed/unconfirmed items live in
	// composer.pending_tray, and non-exited cancellations never render in timeline).
	inputNoticeAt := make(map[uint64][]*corev1.InputProjection)
	if sessionExited {
		for _, ip := range p.Inputs {
			if ip == nil || unsentIDs[ip.RequestId] {
				continue
			}
			switch ip.Status {
			case v1.InputStatus_INPUT_STATUS_FAILED,
				v1.InputStatus_INPUT_STATUS_NEEDS_TERMINAL,
				v1.InputStatus_INPUT_STATUS_UNCONFIRMED:
				inputNoticeAt[ip.LastSeq] = append(inputNoticeAt[ip.LastSeq], ip)
			}
		}
	}

	var rawItems []*corev1.SessionTimelineItem
	var tools timelineToolRun
	var localFiles map[string]*sharedfiles.LocalState
	if ui != nil {
		localFiles = ui.SharedFiles
	}
	tcCtx := timelineToolCtx{
		in:                  in,
		st:                  st,
		p:                   p,
		bgBySeq:             bgBySeq,
		noResultSet:         noResultSet,
		supportsSharedFiles: supportsSharedFiles,
		sharedFileOnline:    sharedFileOnline,
		sessionExited:       sessionExited,
		uiLocale:            uiLocale,
		localFiles:          localFiles,
		cwd:                 cwd,
		home:                home,
		machineName:         machineName,
	}

	replies := replyTexts(events)

	var prevInputTokens, prevOutputTokens int64
	var turnStartInputTokens, turnStartOutputTokens int64
	hasTurnTokenBase := false
	hasFork := false
	var refsBySeq map[uint64][]*v1.ImageRef
	recordRefs := func(seq uint64, refs []*v1.ImageRef) {
		if len(refs) == 0 {
			return
		}
		if refsBySeq == nil {
			refsBySeq = make(map[uint64][]*v1.ImageRef)
		}
		refsBySeq[seq] = refs
	}

	for _, e := range events {
		if e == nil {
			continue
		}
		ts := projection.EventTimeMs(e)
		key := eventItemID(e.Seq)

		if projection.IsSkeleton(e) {
			tools.flush(&rawItems)
			rawItems = append(rawItems, &corev1.SessionTimelineItem{
				Id:          key,
				Seq:         e.Seq,
				EndSeq:      e.Seq,
				TimestampMs: ts,
				TurnId:      e.TurnId,
				Kind:        &corev1.SessionTimelineItem_Skeleton{Skeleton: &corev1.SessionSkeletonItem{Text: text.Msg(text.KeySessionSkeleton)}},
			})
			continue
		}

		for _, reqID := range unsentAt[e.Seq] {
			tools.flush(&rawItems)
			preview := findPromptText(reqID, p, ui)
			rawItems = append(rawItems, &corev1.SessionTimelineItem{
				Id:          "u:" + reqID,
				Seq:         e.Seq,
				EndSeq:      e.Seq,
				TimestampMs: ts,
				TurnId:      e.TurnId,
				Kind: &corev1.SessionTimelineItem_InputState{
					InputState: &corev1.SessionInputStateItem{
						Kind:       corev1.SessionInputNoticeKind_SESSION_INPUT_NOTICE_KIND_UNSENT_ON_EXIT,
						RequestId:  reqID,
						Preview:    preview,
						StatusText: text.Msg(text.KeySessionEndedMessageFailed),
					},
				},
			})
		}

		switch k := e.Kind.(type) {
		case *v1.Event_TurnStarted:
			turnStartInputTokens = prevInputTokens
			turnStartOutputTokens = prevOutputTokens
			hasTurnTokenBase = true

		case *v1.Event_TokenUsage:
			if k.TokenUsage != nil {
				prevInputTokens = k.TokenUsage.InputTokens
				prevOutputTokens = k.TokenUsage.OutputTokens
			}

		case *v1.Event_ToolStarted:
			if ce := callAt[e.Seq]; ce != nil {
				if tc := ce.GetToolCall(); tc != nil {
					recordRefs(ce.Seq, tc.Images)
					tools.add(&tcCtx, &rawItems, ce, tc)
				}
			} else if r := runningBySeq[e.Seq]; r != nil || unfinishedSet[e.Seq] {
				tools.flush(&rawItems)
				rt := r
				unfin := false
				if rt == nil {
					rt = runningToolFromStarted(e)
					unfin = true
				}
				rawItems = append(rawItems, &corev1.SessionTimelineItem{
					Id:          key,
					Seq:         e.Seq,
					EndSeq:      e.Seq,
					TimestampMs: ts,
					TurnId:      e.TurnId,
					Kind: &corev1.SessionTimelineItem_ToolCall{
						ToolCall: buildRunningToolView(e, rt, cwd, home, unfin, endedAtMs),
					},
				})
			}

		case *v1.Event_ToolCall:
			if !placed[e.Seq] {
				if k.ToolCall != nil {
					recordRefs(e.Seq, k.ToolCall.Images)
				}
				tools.add(&tcCtx, &rawItems, e, k.ToolCall)
			}

		case *v1.Event_UserMessage:
			if k.UserMessage == nil {
				continue
			}
			tools.flush(&rawItems)
			um := k.UserMessage
			withoutFiles := withoutFileLines(um.Text, um.Files)
			clean := cleanImagePlaceholders(withoutFiles, um.Images)
			segments := images.SplitImagePlaceholders(withoutFiles, um.Images)
			typedLocal := typedLocallySet[e.Seq]
			var caption *corev1.Text
			var schedTaskID, schedRunID string
			var isSched bool
			if e.Origin != nil && e.Origin.ScheduledTaskId != "" {
				isSched = true
				schedTaskID = e.Origin.ScheduledTaskId
				schedRunID = e.Origin.ScheduledRunId
				caption = text.Msg(text.KeySessionScheduledAt, text.ArgTimestamp("time", ts, corev1.TimestampStyle_TIMESTAMP_STYLE_TIME))
			} else {
				caption = userMessageSource(e.Origin, typedLocal, st, in.DeviceNames, in.ThisDeviceID, machineName)
			}
			var files []*corev1.SessionFileRefView
			for _, sf := range um.Files {
				if sf == nil {
					continue
				}
				disp := sharedfiles.SanitizeFileName(sf.Name)
				head, tail := sharedfiles.SplitMiddleTruncateName(disp)
				files = append(files, &corev1.SessionFileRefView{
					Name:        sf.Name,
					Path:        sf.Path,
					SizeBytes:   sf.SizeBytes,
					DisplayName: disp,
					NameHead:    head,
					NameTail:    tail,
					SizeText:    text.Plain(sharedfiles.FormatFileSize(sf.SizeBytes)),
				})
			}
			recordRefs(e.Seq, um.Images)
			rawItems = append(rawItems, &corev1.SessionTimelineItem{
				Id:          key,
				Seq:         e.Seq,
				EndSeq:      e.Seq,
				TimestampMs: ts,
				TurnId:      e.TurnId,
				Kind: &corev1.SessionTimelineItem_UserMessage{
					UserMessage: &corev1.SessionUserMessageItem{
						Text:            clean,
						ImageViews:      images.BuildImageViews(um.Images),
						Files:           files,
						OriginRequestId: e.GetOrigin().GetRequestId(),
						OriginCaption:   caption,
						ScheduledTaskId: schedTaskID,
						ScheduledRunId:  schedRunID,
						IsScheduled:     isSched,
						TypedLocally:    typedLocal,
						TextSegments:    segments,
					},
				},
			})

		case *v1.Event_ScheduledNotification:
			if k.ScheduledNotification == nil {
				continue
			}
			tools.flush(&rawItems)
			rawItems = append(rawItems, &corev1.SessionTimelineItem{
				Id:          key,
				Seq:         e.Seq,
				EndSeq:      e.Seq,
				TimestampMs: ts,
				TurnId:      e.TurnId,
				Kind: &corev1.SessionTimelineItem_NotifyReceipt{
					NotifyReceipt: buildNotifyReceipt(e, k.ScheduledNotification, ts, replies, st.Cal),
				},
			})

		case *v1.Event_AgentMessage:
			if k.AgentMessage == nil || strings.TrimSpace(k.AgentMessage.Text) == "" {
				continue
			}
			tools.flush(&rawItems)
			rawItems = append(rawItems, &corev1.SessionTimelineItem{
				Id:          key,
				Seq:         e.Seq,
				EndSeq:      e.Seq,
				TimestampMs: ts,
				TurnId:      e.TurnId,
				Kind: &corev1.SessionTimelineItem_AgentMessage{
					AgentMessage: &corev1.SessionAgentMessageItem{
						Markdown: k.AgentMessage.Text,
					},
				},
			})

		case *v1.Event_QuestionAsked:
			if k.QuestionAsked == nil || !k.QuestionAsked.Async {
				continue
			}
			tools.flush(&rawItems)
			var steps []*corev1.SessionQuestionStepView
			for _, q := range k.QuestionAsked.Questions {
				steps = append(steps, buildQuestionStep(q, caps.Agent))
			}
			rawItems = append(rawItems, &corev1.SessionTimelineItem{
				Id:          key,
				Seq:         e.Seq,
				EndSeq:      e.Seq,
				TimestampMs: ts,
				TurnId:      e.TurnId,
				Kind: &corev1.SessionTimelineItem_AsyncQuestion{
					AsyncQuestion: &corev1.SessionAsyncQuestionItem{
						QuestionId: k.QuestionAsked.QuestionId,
						Questions:  steps,
					},
				},
			})

		case *v1.Event_Reasoning:
			if k.Reasoning == nil || strings.TrimSpace(k.Reasoning.Text) == "" {
				continue
			}
			tools.flush(&rawItems)
			rawItems = append(rawItems, &corev1.SessionTimelineItem{
				Id:          key,
				Seq:         e.Seq,
				EndSeq:      e.Seq,
				TimestampMs: ts,
				TurnId:      e.TurnId,
				Kind: &corev1.SessionTimelineItem_Reasoning{
					Reasoning: &corev1.SessionReasoningItem{
						Markdown: k.Reasoning.Text,
					},
				},
			})

		case *v1.Event_ApprovalRequested:
			if k.ApprovalRequested == nil {
				continue
			}
			a := k.ApprovalRequested
			if mergedApprovals[a.ApprovalId] {
				continue
			}
			ra := p.Resolved[a.ApprovalId]
			if ra == nil || !approvalDecisionRecorded(ra.Decision) {
				continue
			}
			tools.flush(&rawItems)
			ok := ra.Decision == v1.ApprovalDecision_APPROVAL_DECISION_ALLOW || ra.Decision == v1.ApprovalDecision_APPROVAL_DECISION_ALLOW_ALWAYS
			what := a.Command
			if what == "" {
				what = a.ToolName
			}
			if what == "" {
				what = a.Description
			}
			what = lists.ShortenPaths(what, cwd, home)
			tone := corev1.SessionNoteTone_SESSION_NOTE_TONE_NEGATIVE
			if ok {
				tone = corev1.SessionNoteTone_SESSION_NOTE_TONE_POSITIVE
			}
			rawItems = append(rawItems, &corev1.SessionTimelineItem{
				Id:          key,
				Seq:         e.Seq,
				EndSeq:      e.Seq,
				TimestampMs: ts,
				TurnId:      e.TurnId,
				Kind: &corev1.SessionTimelineItem_Note{
					Note: &corev1.SessionNoteItem{
						Kind: corev1.SessionNoteKind_SESSION_NOTE_KIND_APPROVAL,
						Tone: tone,
						Text: text.Plain(what),
						Meta: approvalDecisionText(ra.Decision, ra.Message, ra.Origin, st, in.DeviceNames, in.ThisDeviceID, machineName),
					},
				},
			})

		case *v1.Event_QuestionAnswered:
			if k.QuestionAnswered == nil {
				continue
			}
			tools.flush(&rawItems)
			var flat []string
			for _, ans := range k.QuestionAnswered.Answers {
				if ans != nil {
					flat = append(flat, ans.Answers...)
				}
			}
			var label *corev1.Text
			if len(flat) == 0 {
				label = text.Msg(text.KeyDockAnswered, text.Str("answers", ""))
			} else {
				label = text.Msg(text.KeyDockAnswered, text.Str("answers", strings.Join(flat, "、")))
			}
			if src := decisionSourceSuffix(e.Origin, st, in.DeviceNames, in.ThisDeviceID, machineName); src != nil {
				label = text.JoinLiteral("", label, src)
			}
			rawItems = append(rawItems, &corev1.SessionTimelineItem{
				Id:          key,
				Seq:         e.Seq,
				EndSeq:      e.Seq,
				TimestampMs: ts,
				TurnId:      e.TurnId,
				Kind: &corev1.SessionTimelineItem_Note{
					Note: &corev1.SessionNoteItem{
						Kind: corev1.SessionNoteKind_SESSION_NOTE_KIND_QUESTION,
						Tone: corev1.SessionNoteTone_SESSION_NOTE_TONE_POSITIVE,
						Text: label,
					},
				},
			})

		case *v1.Event_ImageGenerated:
			if k.ImageGenerated == nil || len(k.ImageGenerated.Images) == 0 {
				continue
			}
			tools.flush(&rawItems)
			recordRefs(e.Seq, k.ImageGenerated.Images)
			rawItems = append(rawItems, &corev1.SessionTimelineItem{
				Id:          key,
				Seq:         e.Seq,
				EndSeq:      e.Seq,
				TimestampMs: ts,
				TurnId:      e.TurnId,
				Kind: &corev1.SessionTimelineItem_GeneratedImages{
					GeneratedImages: &corev1.SessionGeneratedImagesItem{
						RevisedPrompt: k.ImageGenerated.RevisedPrompt,
						ImageViews:    images.BuildImageViews(k.ImageGenerated.Images),
					},
				},
			})

		case *v1.Event_PlanProposed:
			if k.PlanProposed == nil {
				continue
			}
			tools.flush(&rawItems)
			rawItems = append(rawItems, &corev1.SessionTimelineItem{
				Id:          key,
				Seq:         e.Seq,
				EndSeq:      e.Seq,
				TimestampMs: ts,
				TurnId:      e.TurnId,
				Kind: &corev1.SessionTimelineItem_Plan{
					Plan: &corev1.SessionPlanItem{
						Markdown: k.PlanProposed.Text,
					},
				},
			})

		case *v1.Event_TurnEnded:
			if k.TurnEnded == nil {
				continue
			}
			tools.flush(&rawItems)
			t := k.TurnEnded
			if errReason := cliErrorTurnReason(t.Reason); errReason != "" {
				rawItems = append(rawItems, &corev1.SessionTimelineItem{
					Id:          key + "-error",
					Seq:         e.Seq,
					EndSeq:      e.Seq,
					TimestampMs: ts,
					TurnId:      e.TurnId,
					Kind: &corev1.SessionTimelineItem_ErrorLine{
						ErrorLine: &corev1.SessionErrorLineItem{
							RawReason: errReason,
							Text:      text.Msg(text.KeySessionTurnError, text.Str("reason", errReason)),
						},
					},
				})
			} else if t.Outcome == v1.TurnOutcome_TURN_OUTCOME_ABORTED {
				rawItems = append(rawItems, &corev1.SessionTimelineItem{
					Id:          key + "-interrupted",
					Seq:         e.Seq,
					EndSeq:      e.Seq,
					TimestampMs: ts,
					TurnId:      e.TurnId,
					Kind: &corev1.SessionTimelineItem_Note{
						Note: &corev1.SessionNoteItem{
							Kind: corev1.SessionNoteKind_SESSION_NOTE_KIND_INTERRUPTED,
							Tone: corev1.SessionNoteTone_SESSION_NOTE_TONE_NEUTRAL,
							Text: interruptedNoteText(e.Origin, st, in.DeviceNames, in.ThisDeviceID, machineName),
						},
					},
				})
			}
			var turnTokens int64
			if hasTurnTokenBase {
				dIn := prevInputTokens - turnStartInputTokens
				dOut := prevOutputTokens - turnStartOutputTokens
				if dIn > 0 {
					turnTokens += dIn
				}
				if dOut > 0 {
					turnTokens += dOut
				}
				hasTurnTokenBase = false
			}
			if t.DurationMs >= turnDurationMinMs {
				rawItems = append(rawItems, &corev1.SessionTimelineItem{
					Id:          key,
					Seq:         e.Seq,
					EndSeq:      e.Seq,
					TimestampMs: ts,
					TurnId:      e.TurnId,
					Kind: &corev1.SessionTimelineItem_TurnEnd{
						TurnEnd: &corev1.SessionTurnEndItem{
							Outcome:     t.Outcome,
							DurationMs:  t.DurationMs,
							TurnTokens:  turnTokens,
							Label:       turnDurationText(t.DurationMs, turnTokens),
							Interrupted: t.Outcome == v1.TurnOutcome_TURN_OUTCOME_ABORTED,
						},
					},
				})
			}

		case *v1.Event_SettingsChanged:
			if k.SettingsChanged == nil {
				continue
			}
			note := buildSettingsChangedNote(e, k.SettingsChanged, p.Compactions[e.Seq], st, in.DeviceNames, in.ThisDeviceID, machineName)
			if note != nil {
				tools.flush(&rawItems)
				rawItems = append(rawItems, &corev1.SessionTimelineItem{
					Id:          key,
					Seq:         e.Seq,
					EndSeq:      e.Seq,
					TimestampMs: ts,
					TurnId:      e.TurnId,
					Kind:        &corev1.SessionTimelineItem_Note{Note: note},
				})
			}

		case *v1.Event_TurnContext:
			if ch := p.TerminalChanges[e.Seq]; ch != nil && len(ch.Changes) > 0 {
				tools.flush(&rawItems)
				changesTxt := terminalChangesText(ch.Changes, caps.Name)
				rawItems = append(rawItems, &corev1.SessionTimelineItem{
					Id:          key,
					Seq:         e.Seq,
					EndSeq:      e.Seq,
					TimestampMs: ts,
					TurnId:      e.TurnId,
					Kind: &corev1.SessionTimelineItem_Note{
						Note: &corev1.SessionNoteItem{
							Kind: corev1.SessionNoteKind_SESSION_NOTE_KIND_TERMINAL_CHANGES,
							Tone: corev1.SessionNoteTone_SESSION_NOTE_TONE_NEUTRAL,
							Text: text.Msg(text.KeySessionTerminalChanges, text.Str("name", machineName), text.ArgText("changes", changesTxt)),
						},
					},
				})
			}

		case *v1.Event_SessionState:
			if k.SessionState != nil && k.SessionState.Status == v1.SessionStatus_SESSION_STATUS_EXITED {
				tools.flush(&rawItems)
				rawItems = append(rawItems, &corev1.SessionTimelineItem{
					Id:          key,
					Seq:         e.Seq,
					EndSeq:      e.Seq,
					TimestampMs: ts,
					TurnId:      e.TurnId,
					Kind: &corev1.SessionTimelineItem_TurnEnd{
						TurnEnd: &corev1.SessionTurnEndItem{
							IsSessionEnded: true,
							Label:          text.Msg(text.KeySessionEndedAt, text.ArgTimestamp("time", ts, corev1.TimestampStyle_TIMESTAMP_STYLE_DATE_TIME)),
						},
					},
				})
			}

		case *v1.Event_SessionForked:
			if k.SessionForked != nil {
				hasFork = true
				tools.flush(&rawItems)
				sf := k.SessionForked
				srcTitle := forkSourceTitle(sf.SourceSessionId, sf.SourceTitle, st, caps)
				rawItems = append(rawItems, &corev1.SessionTimelineItem{
					Id:          key,
					Seq:         e.Seq,
					EndSeq:      e.Seq,
					TimestampMs: ts,
					TurnId:      e.TurnId,
					Kind: &corev1.SessionTimelineItem_ForkMarker{
						ForkMarker: &corev1.SessionForkItem{
							SourceSessionId: sf.SourceSessionId,
							SourceTitle:     srcTitle,
							Text:            text.Msg(text.KeySessionForkedFrom, text.ArgTimestamp("time", ts, corev1.TimestampStyle_TIMESTAMP_STYLE_DATE_TIME), text.ArgText("source", srcTitle)),
						},
					},
				})
			}
		}

		// Attach any terminal input state notices pointing at this event sequence.
		if notices := inputNoticeAt[e.Seq]; len(notices) > 0 {
			tools.flush(&rawItems)
			for _, ip := range notices {
				rawItems = append(rawItems, buildInputStateTimelineItem(ip, e.Seq, ts, caps.Agent))
			}
		}
	}
	tools.flush(&rawItems)
	placeReceiptsInTurns(rawItems)

	// Fold contiguous same-day quiet scheduled runs.
	spans := findQuietSpans(events, p)
	if canUseCache {
		ui.timeline = &timelineCache{
			key:       cacheKey,
			hasFork:   hasFork,
			rawItems:  rawItems,
			spans:     spans,
			refsBySeq: refsBySeq,
		}
	}
	folded := foldQuietScheduledItemsWithSpans(rawItems, spans, st.Cal, win)

	// Apply window pagination and anchor resolution; viewer_images always covers the full folded timeline.
	applyTimelineWindow(out, folded, win)
	out.ViewerImages = images.CollectViewerImages(folded, refsBySeq, caps.Name, home)
	return out
}

// CollectSessionViewerAnchors computes the full chronological sequence of viewer image anchors
// directly from the session snapshot's events and projection (matching buildSessionTimeline's
// CompletedAt tool placement order) without building a view model or relying on any visible window.
func CollectSessionViewerAnchors(in world.SessionSnapshot) []ViewerImageAnchor {
	events := in.Events
	if len(events) == 0 {
		return nil
	}
	p := in.Projection
	if p == nil {
		p = &corev1.ProjectionState{}
	}
	callAt := make(map[uint64]*v1.Event, len(p.CompletedAt))
	placed := make(map[uint64]bool, len(p.CompletedAt))
	for startSeq, callSeq := range p.CompletedAt {
		if ce := findEventBySeq(events, callSeq); ce != nil {
			callAt[startSeq] = ce
			placed[callSeq] = true
		}
	}
	var anchors []ViewerImageAnchor
	appendRefs := func(seq uint64, refs []*v1.ImageRef) {
		for i, ref := range refs {
			if ref != nil {
				anchors = append(anchors, ViewerImageAnchor{Seq: seq, EventImageIndex: int32(i)})
			}
		}
	}
	for _, e := range events {
		if e == nil || projection.IsSkeleton(e) {
			continue
		}
		switch k := e.Kind.(type) {
		case *v1.Event_UserMessage:
			if k.UserMessage != nil {
				appendRefs(e.Seq, k.UserMessage.Images)
			}
		case *v1.Event_ToolStarted:
			if ce := callAt[e.Seq]; ce != nil && ce.GetToolCall() != nil {
				appendRefs(ce.Seq, ce.GetToolCall().Images)
			}
		case *v1.Event_ToolCall:
			if placed[e.Seq] || k.ToolCall == nil {
				continue
			}
			appendRefs(e.Seq, k.ToolCall.Images)
		case *v1.Event_ImageGenerated:
			if k.ImageGenerated != nil {
				appendRefs(e.Seq, k.ImageGenerated.Images)
			}
		}
	}
	return anchors
}

func eventItemID(seq uint64) string {
	return "e" + strconv.FormatUint(uint64(seq), 10)
}

func toolOk(t *v1.ToolCall) bool {
	if t == nil {
		return false
	}
	if t.Status == toolStatusFailed {
		return false
	}
	if t.ExitCode != nil && *t.ExitCode != 0 {
		return false
	}
	return true
}

func cliErrorTurnReason(reason string) string {
	r := strings.TrimSpace(reason)
	if r == "" {
		return ""
	}
	switch strings.ToLower(r) {
	case "interrupted", "cancelled", "canceled", "user_cancelled", "user_canceled", "no end recorded", "new input after unfinished turn":
		return ""
	default:
		return r
	}
}

func approvalDecisionRecorded(d v1.ApprovalDecision) bool {
	return d != v1.ApprovalDecision_APPROVAL_DECISION_CANCELED && d != v1.ApprovalDecision_APPROVAL_DECISION_TERMINAL
}

func buildSharedFilesItem(
	files []*v1.SharedFile,
	localFiles map[string]*sharedfiles.LocalState,
	machineName, machineHome string,
	online, sessionExited bool,
	uiLocale string,
) *corev1.SessionSharedFilesItem {
	out := make([]*corev1.SessionSharedFileItem, 0, len(files))
	for _, f := range files {
		if f == nil {
			continue
		}
		var local *sharedfiles.LocalState
		if localFiles != nil {
			local = localFiles[f.FileId]
		}
		card := sharedfiles.BuildCard(sharedfiles.CardInput{
			File:          f,
			Local:         local,
			MachineName:   machineName,
			MachineHome:   machineHome,
			Online:        online,
			SessionExited: sessionExited,
			UILocale:      uiLocale,
		})
		if card != nil {
			out = append(out, card)
		}
	}
	return &corev1.SessionSharedFilesItem{Files: out}
}

func toolDisplayTitle(kind v1.ToolKind, title, itemType, cwd, home string) string {
	if kind == v1.ToolKind_TOOL_KIND_FILE_CHANGE && title != "" {
		return lists.ShortenPath(title, cwd, home)
	}
	if title != "" && title != "ToolCall" && title != "CommandExecution" {
		return lists.ShortenPaths(title, cwd, home)
	}
	if itemType != "" && itemType != "ToolCall" && itemType != "CommandExecution" {
		return itemType
	}
	return ""
}

func buildToolCallView(
	e *v1.Event,
	tc *v1.ToolCall,
	cwd, home string,
	noResult, wentBg bool,
	bg *corev1.BackgroundTool,
) *corev1.SessionToolCallItem {
	status := corev1.SessionToolStatus_SESSION_TOOL_STATUS_OK
	if !toolOk(tc) {
		status = corev1.SessionToolStatus_SESSION_TOOL_STATUS_ERROR
	} else if noResult {
		status = corev1.SessionToolStatus_SESSION_TOOL_STATUS_UNCONFIRMED
	}
	if wentBg {
		status = corev1.SessionToolStatus_SESSION_TOOL_STATUS_BACKGROUND
	}
	var exitCode int32
	var hasExit bool
	if tc.ExitCode != nil {
		exitCode = *tc.ExitCode
		hasExit = true
	}
	bgKind := tc.Background
	bgID := tc.BackgroundId
	if bg != nil {
		if bg.Background != v1.ToolBackground_TOOL_BACKGROUND_UNSPECIFIED {
			bgKind = bg.Background
		}
		if bg.BackgroundId != "" {
			bgID = bg.BackgroundId
		}
	}
	title := toolDisplayTitle(tc.Kind, tc.Title, tc.ItemType, cwd, home)
	var bgLabel *corev1.Text
	if wentBg {
		bgLabel = backgroundToolText(bgKind, bg != nil && bg.Returned, title)
	}
	return &corev1.SessionToolCallItem{
		CallId:          tc.CallId,
		ToolKind:        tc.Kind,
		ItemType:        tc.ItemType,
		Title:           title,
		RawInput:        tc.Input,
		Detail:          tc.Output,
		Status:          status,
		ExitCode:        exitCode,
		HasExitCode:     hasExit,
		ElapsedMs:       tc.DurationMs,
		IsBackground:    wentBg,
		BackgroundKind:  bgKind,
		BackgroundId:    bgID,
		BackgroundLabel: bgLabel,
		NoResult:        noResult,
		Unconfirmed:     noResult,
		ImageViews:      images.BuildImageViews(tc.Images),
		Seq:             e.Seq,
	}
}

func buildRunningToolView(e *v1.Event, rt *corev1.RunningTool, cwd, home string, unfinished bool, endedAtMs int64) *corev1.SessionToolCallItem {
	var title, callID, itemType, input string
	var kind v1.ToolKind
	var sinceMs int64
	if rt != nil {
		title = toolDisplayTitle(rt.Kind, rt.Title, rt.ItemType, cwd, home)
		callID = rt.CallId
		itemType = rt.ItemType
		kind = rt.Kind
		sinceMs = rt.SinceMs
		input = rt.Input
	}
	status := corev1.SessionToolStatus_SESSION_TOOL_STATUS_RUNNING
	var frozenAtMs int64
	if unfinished {
		status = corev1.SessionToolStatus_SESSION_TOOL_STATUS_UNCONFIRMED
		if endedAtMs > 0 {
			frozenAtMs = endedAtMs
		}
	}
	return &corev1.SessionToolCallItem{
		CallId:      callID,
		ToolKind:    kind,
		ItemType:    itemType,
		Title:       title,
		RawInput:    input,
		Status:      status,
		SinceMs:     sinceMs,
		FrozenAtMs:  frozenAtMs,
		Unconfirmed: unfinished,
		Seq:         e.Seq,
	}
}

func buildToolGroupItem(calls []*corev1.SessionToolCallItem) *corev1.SessionToolGroupItem {
	var cmdCount, editCount, otherCount, imgCount int32
	for _, c := range calls {
		switch c.ToolKind {
		case v1.ToolKind_TOOL_KIND_COMMAND:
			cmdCount++
		case v1.ToolKind_TOOL_KIND_FILE_CHANGE:
			editCount++
		default:
			otherCount++
		}
		imgCount += int32(len(c.ImageViews))
	}
	n := int64(len(calls))
	var summary *corev1.Text
	switch {
	case int(cmdCount) == len(calls):
		summary = text.Msg(text.KeyToolsCompletedCommands, text.Int("count", n))
	case int(editCount) == len(calls):
		summary = text.Msg(text.KeyToolsCompletedFiles, text.Int("count", n))
	default:
		summary = text.Msg(text.KeyToolsCompletedCalls, text.Int("count", n))
	}
	return &corev1.SessionToolGroupItem{
		Calls:        calls,
		Summary:      summary,
		CommandCount: cmdCount,
		EditCount:    editCount,
		OtherCount:   otherCount,
		ImageCount:   imgCount,
	}
}

func backgroundToolText(kind v1.ToolBackground, returned bool, title string) *corev1.Text {
	if returned {
		return text.Msg(text.KeyToolsBackgroundReturned)
	}
	switch kind {
	case v1.ToolBackground_TOOL_BACKGROUND_STILL_RUNNING:
		return text.Msg(text.KeyToolsBackgroundTurn)
	case v1.ToolBackground_TOOL_BACKGROUND_BY_USER:
		return text.Msg(text.KeyToolsBackgroundTerminal)
	case v1.ToolBackground_TOOL_BACKGROUND_TIMED_OUT:
		return text.Msg(text.KeyToolsBackgroundTimeout)
	case v1.ToolBackground_TOOL_BACKGROUND_REQUESTED:
		return text.Msg(text.KeyToolsBackgroundAgent)
	default:
		if title != "" {
			return text.Msg(text.KeyToolsAndroidBackgroundLabel, text.Str("name", title))
		}
		return text.Msg(text.KeyToolsBackground)
	}
}

func approvalDecisionText(
	d v1.ApprovalDecision,
	message string,
	origin *v1.Origin,
	st *State,
	devices map[string]string,
	thisDeviceID, machineName string,
) *corev1.Text {
	var base *corev1.Text
	switch d {
	case v1.ApprovalDecision_APPROVAL_DECISION_ALLOW:
		base = text.Msg(text.KeyApprovalAllowed)
	case v1.ApprovalDecision_APPROVAL_DECISION_ALLOW_ALWAYS:
		base = text.Msg(text.KeyApprovalAlwaysAllowed)
	case v1.ApprovalDecision_APPROVAL_DECISION_DENY:
		base = text.Msg(text.KeyApprovalDenied)
	case v1.ApprovalDecision_APPROVAL_DECISION_TIMEOUT:
		base = text.Msg(text.KeyApprovalTimedOut)
	}
	if message != "" {
		if d == v1.ApprovalDecision_APPROVAL_DECISION_ALLOW_ALWAYS {
			kind, val := lists.ParseApprovalAlways(message)
			if kind == lists.AlwaysDirectory {
				base = text.JoinLiteral(" · ", base, text.Msg(text.KeyApprovalAlwaysDirectory, text.Str("path", lists.ShortPath(val))))
			} else if kind == lists.AlwaysPrefix {
				base = text.JoinLiteral(" · ", base, text.Msg(text.KeyApprovalAlwaysPrefix, text.Str("command", val)))
			} else {
				base = text.JoinLiteral(" · ", base, text.Plain(message))
			}
		} else {
			base = text.JoinLiteral(" · ", base, text.Plain(message))
		}
	}
	if src := decisionSourceSuffix(origin, st, devices, thisDeviceID, machineName); src != nil {
		base = text.JoinLiteral("", base, src)
	}
	return base
}

func decisionSourceSuffix(origin *v1.Origin, st *State, devices map[string]string, thisDeviceID, machineName string) *corev1.Text {
	if origin != nil && origin.Kind == v1.OriginKind_ORIGIN_KIND_CLIENT && origin.DeviceId != "" && origin.DeviceId == thisDeviceID {
		return nil
	}
	if src := originLabelOnly(origin, devices, machineName); src != nil {
		return text.JoinLiteral("", text.Plain(" · "), src)
	}
	return text.JoinLiteral("", text.Plain(" · "), text.Msg(text.KeySessionOriginElsewhere))
}

func originLabelOnly(origin *v1.Origin, devices map[string]string, machineName string) *corev1.Text {
	if origin == nil {
		return nil
	}
	switch origin.Kind {
	case v1.OriginKind_ORIGIN_KIND_CLIENT:
		if name := devices[origin.DeviceId]; name != "" {
			return text.Plain(name)
		}
		return text.Msg(text.KeySessionOriginOther)
	case v1.OriginKind_ORIGIN_KIND_LOCAL:
		return text.Msg(text.KeySessionOriginTerminal, text.Str("name", machineName))
	default:
		return nil
	}
}

func userMessageSource(
	origin *v1.Origin,
	typedLocally bool,
	st *State,
	devices map[string]string,
	thisDeviceID, machineName string,
) *corev1.Text {
	if origin != nil && origin.Kind == v1.OriginKind_ORIGIN_KIND_CLIENT && origin.DeviceId != "" && origin.DeviceId == thisDeviceID {
		return nil
	}
	if src := originLabelOnly(origin, devices, machineName); src != nil {
		return src
	}
	if typedLocally {
		return text.Msg(text.KeySessionOriginTerminal, text.Str("name", machineName))
	}
	return nil
}

func interruptedNoteText(origin *v1.Origin, st *State, devices map[string]string, thisDeviceID, machineName string) *corev1.Text {
	if origin != nil && origin.Kind == v1.OriginKind_ORIGIN_KIND_CLIENT && origin.DeviceId == thisDeviceID {
		return text.Msg(text.KeySessionInterrupted)
	}
	if src := originLabelOnly(origin, devices, machineName); src != nil {
		return text.Msg(text.KeySessionAndroidInterruptedBy, text.ArgText("origin", src))
	}
	return text.Msg(text.KeySessionInterrupted)
}

func durationText(ms int64) *corev1.Text {
	s := ms / 1000
	switch {
	case s < 60:
		return text.Msg(text.KeyTimeDurationSeconds, text.Int("count", s))
	case s < minutesHourSec:
		return text.Msg(text.KeyTimeDurationMinutesSeconds, text.Int("minutes", s/60), text.Int("seconds", s%60))
	default:
		return text.Msg(text.KeyTimeDurationHoursMinutes, text.Int("hours", s/minutesHourSec), text.Int("minutes", (s%minutesHourSec)/60))
	}
}

func turnDurationText(ms, turnTokens int64) *corev1.Text {
	base := text.Msg(text.KeySessionTurnDuration, text.ArgText("duration", durationText(ms)))
	if turnTokens <= 0 {
		return base
	}
	var tokTxt *corev1.Text
	if turnTokens >= 1000 {
		tokTxt = text.Msg(text.KeySessionIosTurnKTokens, text.Int("count", turnTokens/1000))
	} else {
		tokTxt = text.Msg(text.KeySessionIosTurnTokens, text.Int("count", turnTokens))
	}
	return text.JoinLiteral(" · ", base, tokTxt)
}

func buildSettingsChangedNote(
	e *v1.Event,
	c *v1.SettingsChanged,
	comp *corev1.ContextCompaction,
	st *State,
	devices map[string]string,
	thisDeviceID, machineName string,
) *corev1.SessionNoteItem {
	var by *corev1.Text
	if e.Origin == nil || e.Origin.Kind != v1.OriginKind_ORIGIN_KIND_CLIENT || e.Origin.DeviceId != thisDeviceID {
		by = originLabelOnly(e.Origin, devices, machineName)
	}
	if c.Compacted {
		var txt *corev1.Text
		if comp != nil && comp.BeforePercent != nil && comp.AfterPercent != nil {
			txt = text.Msg(text.KeySessionAndroidCompactedUsage,
				text.Str("before", strconv.FormatInt(int64(*comp.BeforePercent), 10)),
				text.Str("after", strconv.FormatInt(int64(*comp.AfterPercent), 10)),
			)
		} else {
			txt = text.Msg(text.KeySessionCompacted)
		}
		return &corev1.SessionNoteItem{
			Kind: corev1.SessionNoteKind_SESSION_NOTE_KIND_COMPACTED,
			Tone: corev1.SessionNoteTone_SESSION_NOTE_TONE_NEUTRAL,
			Text: txt,
		}
	}
	if c.Model != "" {
		var txt *corev1.Text
		if by != nil {
			txt = text.Msg(text.KeySessionModelChangedBy, text.ArgText("origin", by), text.Str("value", c.Model))
		} else {
			txt = text.Msg(text.KeySessionModelChanged, text.Str("value", c.Model))
		}
		if c.Effort != "" {
			txt = text.JoinLiteral(" · ", txt, text.Plain(c.Effort))
		}
		return &corev1.SessionNoteItem{
			Kind: corev1.SessionNoteKind_SESSION_NOTE_KIND_MODEL_CHANGED,
			Tone: corev1.SessionNoteTone_SESSION_NOTE_TONE_NEUTRAL,
			Text: txt,
		}
	}
	mode := projection.ModeOf(c.Mode)
	if mode != v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED {
		mName := modeNameText(mode)
		var txt *corev1.Text
		if by != nil {
			txt = text.Msg(text.KeySessionModeChangedBy, text.ArgText("origin", by), text.ArgText("value", mName))
		} else {
			txt = text.Msg(text.KeySessionModeChanged, text.ArgText("value", mName))
		}
		return &corev1.SessionNoteItem{
			Kind:     corev1.SessionNoteKind_SESSION_NOTE_KIND_MODE_CHANGED,
			Tone:     corev1.SessionNoteTone_SESSION_NOTE_TONE_NEUTRAL,
			Text:     txt,
			Emphasis: mName,
		}
	}
	return nil
}

func terminalChangesText(changes []*corev1.TerminalChange, agentName string) *corev1.Text {
	var parts []*corev1.Text
	for _, ch := range changes {
		if ch == nil {
			continue
		}
		switch k := ch.Kind.(type) {
		case *corev1.TerminalChange_Mode:
			if k.Mode != nil {
				m := k.Mode.Mode
				label := sessionApprovalModeText(m, k.Mode.RawMode, agentName)
				parts = append(parts, text.Msg(text.KeySessionTerminalModeChange, text.ArgText("value", label)))
			}
		case *corev1.TerminalChange_Model:
			if k.Model != nil {
				val := k.Model.Model
				if k.Model.Effort != "" {
					val += " · " + k.Model.Effort
				}
				parts = append(parts, text.Msg(text.KeySessionTerminalModelChange, text.Str("value", val)))
			}
		}
	}
	return text.Join(text.KeyCommonNoteSeparator, parts...)
}

func forkSourceTitle(sourceID, fallbackTitle string, st *State, caps lists.AgentCapabilities) *corev1.Text {
	if st != nil && sourceID != "" {
		for _, s := range st.World.Sessions {
			if s != nil && s.ID == sourceID && s.Info != nil {
				plainTitle, hasCustom, _ := lists.ResolveTitle(s, st.Cal)
				pk := lists.ProjectKey(s)
				pn := ""
				if pk != "" {
					pn = lists.ProjectName(pk)
				}
				it := &lists.SessionItem{
					Session:        s,
					Agent:          caps.Agent,
					Title:          plainTitle,
					HasCustomTitle: hasCustom,
					ProjectKey:     pk,
					ProjectName:    pn,
					CreatedMs:      lists.CreatedMs(s),
				}
				return sessionTitleText(it)
			}
		}
	}
	if p := lists.Plain(fallbackTitle); p != "" {
		return text.Plain(p)
	}
	return text.Msg(text.KeySessionOtherSession)
}

func findPromptText(reqID string, p *corev1.ProjectionState, ui *SessionUIState) string {
	if p != nil {
		for _, ip := range p.Inputs {
			if ip != nil && ip.RequestId == reqID {
				if ip.Accepted != nil && ip.Accepted.Text != "" {
					return ip.Accepted.Text
				}
				if ip.Detail != nil && ip.Detail.Text != "" {
					return ip.Detail.Text
				}
			}
		}
	}
	if ui != nil {
		for _, og := range ui.Outgoing {
			if og != nil && og.RequestID == reqID {
				return og.Text
			}
		}
	}
	return ""
}

func buildInputStateTimelineItem(ip *corev1.InputProjection, seq uint64, ts int64, agent v1.Agent) *corev1.SessionTimelineItem {
	kind := corev1.SessionInputNoticeKind_SESSION_INPUT_NOTICE_KIND_FAILED
	var statusTxt, reasonTxt *corev1.Text
	if ip.Detail != nil && ip.Detail.Reason != "" {
		reasonTxt = text.Plain(ip.Detail.Reason)
	}
	switch ip.Status {
	case v1.InputStatus_INPUT_STATUS_CANCELED:
		if ip.Detail != nil && ip.Detail.Reason == "exited" {
			kind = corev1.SessionInputNoticeKind_SESSION_INPUT_NOTICE_KIND_UNSENT_ON_EXIT
			statusTxt = text.Msg(text.KeySessionEndedMessageFailed)
		} else {
			kind = corev1.SessionInputNoticeKind_SESSION_INPUT_NOTICE_KIND_CANCELED
			statusTxt = text.Msg(text.KeySessionEndedSendFailed)
		}
	case v1.InputStatus_INPUT_STATUS_NEEDS_TERMINAL:
		kind = corev1.SessionInputNoticeKind_SESSION_INPUT_NOTICE_KIND_NEEDS_TERMINAL
		statusTxt = text.Msg(text.KeyComposerNotWritten)
	case v1.InputStatus_INPUT_STATUS_UNCONFIRMED:
		kind = corev1.SessionInputNoticeKind_SESSION_INPUT_NOTICE_KIND_UNCONFIRMED
		statusTxt = text.Msg(text.KeyComposerUnconfirmed)
	default:
		kind = corev1.SessionInputNoticeKind_SESSION_INPUT_NOTICE_KIND_FAILED
		if reasonTxt != nil {
			statusTxt = text.Msg(text.KeyComposerSendFailure, text.ArgText("reason", reasonTxt))
		} else {
			statusTxt = text.Msg(text.KeySessionEndedMessageFailed)
		}
	}
	_ = agent
	var preview string
	if ip.Accepted != nil {
		preview = ip.Accepted.Text
	}
	if preview == "" && ip.Detail != nil {
		preview = ip.Detail.Text
	}
	return &corev1.SessionTimelineItem{
		Id:          "is:" + ip.RequestId,
		Seq:         seq,
		EndSeq:      seq,
		TimestampMs: ts,
		Kind: &corev1.SessionTimelineItem_InputState{
			InputState: &corev1.SessionInputStateItem{
				Kind:       kind,
				RequestId:  ip.RequestId,
				Preview:    preview,
				StatusText: statusTxt,
				ReasonText: reasonTxt,
			},
		},
	}
}

// withoutFileLines strips injected "File: ..." lines matching SentFile paths, matching Android Files.kt.
func withoutFileLines(msg string, files []*v1.SentFile) string {
	if len(files) == 0 || msg == "" {
		return msg
	}
	paths := make(map[string]bool, len(files))
	for _, f := range files {
		if f != nil && f.Path != "" {
			paths[f.Path] = true
		}
	}
	if len(paths) == 0 {
		return msg
	}
	lines := strings.Split(msg, "\n")
	hidden := make(map[int]bool)
	for i, line := range lines {
		cand := line
		if strings.HasPrefix(line, "File: @\"") && strings.HasSuffix(line, "\" (file)") && len(line) > 16 {
			cand = "@" + line[8:len(line)-8]
		} else if strings.HasPrefix(line, "File: ") {
			cand = line[6:]
		}
		if paths[cand] || (strings.HasPrefix(cand, "@") && paths[cand[1:]]) {
			hidden[i] = true
			if i > 0 && lines[i-1] == "" {
				hidden[i-1] = true
			}
		}
	}
	if len(hidden) == 0 {
		return msg
	}
	out := make([]string, 0, len(lines)-len(hidden))
	for i, l := range lines {
		if !hidden[i] {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// cleanImagePlaceholders removes auto-prepended "[Image #N]" prefixes echoed at the start of UserMessage.Text.
func cleanImagePlaceholders(msg string, refs []*v1.ImageRef) string {
	if len(refs) == 0 || msg == "" {
		return msg
	}
	known := make(map[int32]bool, len(refs))
	for _, r := range refs {
		if r != nil && r.CliIndex > 0 {
			known[r.CliIndex] = true
		}
	}
	if len(known) == 0 {
		return msg
	}
	t := msg
	stripped := false
	for {
		trim := strings.TrimLeft(t, " \t\r\n")
		if !strings.HasPrefix(trim, "[Image #") {
			break
		}
		end := strings.IndexByte(trim, ']')
		if end <= 8 {
			break
		}
		numStr := trim[8:end]
		var idx int32
		ok := true
		for _, ch := range numStr {
			if ch < '0' || ch > '9' {
				ok = false
				break
			}
			idx = idx*10 + int32(ch-'0')
		}
		if !ok || !known[idx] {
			break
		}
		t = trim[end+1:]
		stripped = true
	}
	if !stripped {
		return msg
	}
	return strings.TrimLeft(t, " \t\r\n")
}

type quietSpan struct {
	taskID  string
	runID   string
	fromSeq uint64
	endSeq  uint64
	timeMs  int64
}

func findQuietSpans(events []*v1.Event, p *corev1.ProjectionState) []quietSpan {
	var starts []int
	for i, e := range events {
		if e != nil && e.GetUserMessage() != nil {
			starts = append(starts, i)
		}
	}
	var out []quietSpan
	for idx, start := range starts {
		first := events[start]
		if first.Origin == nil || first.Origin.ScheduledTaskId == "" || first.Origin.ScheduledRunId == "" {
			continue
		}
		task := first.Origin.ScheduledTaskId
		run := first.Origin.ScheduledRunId
		end := len(events)
		if idx+1 < len(starts) {
			nextUser := starts[idx+1]
			end = nextUser
			beforeNext := events[start:nextUser]
			lastStarted, lastEnded := -1, -1
			for bi, be := range beforeNext {
				if be.GetTurnStarted() != nil {
					lastStarted = bi
				}
				if be.GetTurnEnded() != nil {
					lastEnded = bi
				}
			}
			if lastEnded >= 0 && lastStarted > lastEnded {
				end = start + lastStarted
			}
		}
		body := events[start:end]
		var ended []*v1.Event
		var lastStartedIdx, lastEndedIdx = -1, -1
		quiet := true
		for bi, be := range body {
			if projection.IsSkeleton(be) {
				quiet = false
				break
			}
			switch k := be.Kind.(type) {
			case *v1.Event_TurnStarted:
				lastStartedIdx = bi
			case *v1.Event_TurnEnded:
				lastEndedIdx = bi
				ended = append(ended, be)
			case *v1.Event_ToolCall:
				if !toolOk(k.ToolCall) || (k.ToolCall != nil && k.ToolCall.Background != v1.ToolBackground_TOOL_BACKGROUND_UNSPECIFIED) {
					quiet = false
				}
			case *v1.Event_ToolStarted:
				// handled after loop once ended seq is known
			case *v1.Event_TurnContext:
				if p.TerminalChanges[be.Seq] != nil && len(p.TerminalChanges[be.Seq].Changes) > 0 {
					quiet = false
				}
			case *v1.Event_SettingsChanged,
				*v1.Event_QuestionAnswered,
				*v1.Event_ImageGenerated,
				*v1.Event_SessionForked,
				*v1.Event_ScheduledNotification,
				*v1.Event_ApprovalRequested,
				*v1.Event_QuestionAsked,
				*v1.Event_PlanProposed:
				quiet = false
			}
			if !quiet {
				break
			}
		}
		if !quiet || len(ended) != 1 || lastStartedIdx > lastEndedIdx {
			continue
		}
		te := ended[0].GetTurnEnded()
		if te == nil ||
			te.ScheduledTaskId != task ||
			te.ScheduledRunId != run ||
			te.ScheduledNotified ||
			te.Outcome != v1.TurnOutcome_TURN_OUTCOME_COMPLETED ||
			cliErrorTurnReason(te.Reason) != "" {
			continue
		}
		endSeq := ended[0].Seq
		for _, be := range body {
			if be.GetToolStarted() != nil {
				compSeq, ok := p.CompletedAt[be.Seq]
				if !ok || compSeq > endSeq {
					quiet = false
					break
				}
			}
		}
		if !quiet {
			continue
		}
		for _, ae := range body[lastEndedIdx+1:] {
			if ae.GetAgentMessage() != nil || ae.GetReasoning() != nil || ae.GetToolStarted() != nil || ae.GetToolCall() != nil {
				quiet = false
				break
			}
		}
		if !quiet {
			continue
		}
		out = append(out, quietSpan{
			taskID:  task,
			runID:   run,
			fromSeq: first.Seq,
			endSeq:  endSeq,
			timeMs:  projection.EventTimeMs(first),
		})
	}
	return out
}

func foldQuietScheduledItemsWithSpans(
	items []*corev1.SessionTimelineItem,
	spans []quietSpan,
	cal lists.Calendar,
	win *corev1.SessionWindowRequest,
) []*corev1.SessionTimelineItem {
	if len(spans) < 2 {
		return items
	}
	spanByFrom := make(map[uint64]quietSpan, len(spans))
	for _, sp := range spans {
		spanByFrom[sp.fromSeq] = sp
	}

	expandedIDs := make(map[string]bool)
	var revealSeq uint64
	var revealRunID string
	if win != nil {
		revealSeq = win.RevealSeq
		revealRunID = win.RevealRunId
		for _, id := range win.ExpandedGroupIds {
			expandedIDs[id] = true
		}
	}

	out := make([]*corev1.SessionTimelineItem, 0, len(items))
	for at := 0; at < len(items); {
		it := items[at]
		sp, ok := spanByFrom[it.Seq]
		if !ok || it.Seq == 0 {
			out = append(out, it)
			at++
			continue
		}
		var covered []*corev1.SessionTimelineItem
		var times []int64
		var runIDs []string
		cur := sp
		next := at
		for {
			times = append(times, cur.timeMs)
			runIDs = append(runIDs, cur.runID)
			for next < len(items) && items[next].Seq <= cur.endSeq {
				covered = append(covered, items[next])
				next++
			}
			if next >= len(items) {
				break
			}
			following, hasNext := spanByFrom[items[next].Seq]
			if !hasNext || following.taskID != sp.taskID || !sameCalendarDay(sp.timeMs, following.timeMs, cal.UTCOffsetMinutes) {
				break
			}
			cur = following
		}
		if len(times) > 1 {
			groupID := "quiet-" + sp.runID
			expanded := expandedIDs[groupID] ||
				(revealSeq > 0 && revealSeq >= sp.fromSeq && revealSeq <= cur.endSeq) ||
				(revealRunID != "" && slices.Contains(runIDs, revealRunID))
			timeParts := make([]*corev1.Text, 0, len(times))
			for _, tms := range times {
				timeParts = append(timeParts, text.Time(tms, corev1.TimestampStyle_TIMESTAMP_STYLE_TIME))
			}
			title := text.Msg(text.KeySessionQuietChecks, text.ArgText("times", text.Join(text.KeyCommonListSeparator, timeParts...)))
			out = append(out, &corev1.SessionTimelineItem{
				Id:          groupID,
				Seq:         sp.fromSeq,
				EndSeq:      cur.endSeq,
				TimestampMs: sp.timeMs,
				Kind: &corev1.SessionTimelineItem_QuietScheduledGroup{
					QuietScheduledGroup: &corev1.SessionQuietScheduledGroupItem{
						GroupId:         groupID,
						ScheduledTaskId: sp.taskID,
						RunCount:        int32(len(times)),
						RunTimesMs:      times,
						RunIds:          runIDs,
						Title:           title,
						Expanded:        expanded,
						Items:           covered,
					},
				},
			})
		} else {
			out = append(out, covered...)
		}
		if next > at {
			at = next
		} else {
			at++
		}
	}
	return out
}

func sameCalendarDay(aMs, bMs int64, tzOffsetMinutes int32) bool {
	offsetMs := int64(tzOffsetMinutes) * 60_000
	dayA := (aMs + offsetMs) / 86_400_000
	dayB := (bMs + offsetMs) / 86_400_000
	return dayA == dayB
}

func applyTimelineWindow(out *corev1.SessionTimelineView, all []*corev1.SessionTimelineItem, win *corev1.SessionWindowRequest) {
	out.TotalItems = int32(len(all))
	if len(all) == 0 {
		return
	}

	limit := DefaultSessionWindowLimit
	var beforeSeq, aroundSeq, revealSeq uint64
	var revealRunID string
	if win != nil {
		if win.Limit > 0 {
			limit = int(win.Limit)
		}
		beforeSeq = win.BeforeSeq
		aroundSeq = win.AroundSeq
		revealSeq = win.RevealSeq
		revealRunID = win.RevealRunId
	}

	anchorIdx := -1
	if revealRunID != "" {
		for i, it := range all {
			if q := it.GetQuietScheduledGroup(); q != nil && slices.Contains(q.RunIds, revealRunID) {
				anchorIdx = i
				out.AnchorSeq = it.Seq
				out.AnchorItemId = it.Id
				break
			}
			if um := it.GetUserMessage(); um != nil && um.ScheduledRunId == revealRunID {
				anchorIdx = i
				out.AnchorSeq = it.Seq
				out.AnchorItemId = it.Id
				break
			}
			if nr := it.GetNotifyReceipt(); nr != nil && nr.ScheduledRunId == revealRunID {
				anchorIdx = i
				out.AnchorSeq = it.Seq
				out.AnchorItemId = it.Id
				break
			}
		}
	}
	if anchorIdx < 0 {
		targetSeq := revealSeq
		if targetSeq == 0 {
			targetSeq = aroundSeq
		}
		if targetSeq > 0 {
			for i, it := range all {
				if it.Seq == targetSeq || (it.EndSeq >= targetSeq && it.Seq <= targetSeq) {
					anchorIdx = i
					out.AnchorSeq = targetSeq
					out.AnchorItemId = it.Id
					break
				}
			}
		}
	}

	end := len(all)
	if beforeSeq > 0 {
		for end > 0 && all[end-1].Seq >= beforeSeq {
			end--
		}
	} else if anchorIdx >= 0 {
		half := limit / 2
		start := anchorIdx - half
		if start < 0 {
			start = 0
		}
		end = start + limit
		if end > len(all) {
			end = len(all)
			start = end - limit
			if start < 0 {
				start = 0
			}
		}
		sliceWindow(out, all, start, end)
		return
	}

	start := end - limit
	if start < 0 {
		start = 0
	}
	sliceWindow(out, all, start, end)
}

func sliceWindow(out *corev1.SessionTimelineView, all []*corev1.SessionTimelineItem, start, end int) {
	if start > end {
		start = end
	}
	window := all[start:end]
	out.Items = window
	out.HasEarlier = start > 0
	if start > 0 && len(window) > 0 {
		out.NextBeforeSeq = window[0].Seq
	}
	if len(window) > 0 {
		out.WindowFirstSeq = window[0].Seq
		last := window[len(window)-1]
		if last.EndSeq > last.Seq {
			out.WindowLastSeq = last.EndSeq
		} else {
			out.WindowLastSeq = last.Seq
		}
	}
	if out.HasEarlier {
		out.CanLoadEarlier = true
	}
}
