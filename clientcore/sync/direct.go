package sync

import (
	"strconv"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/transport"
	"github.com/Lewin671/remote-agent/shared/peerwire"
)

func (e *Engine) ensureTransportCoord() *transport.Coordinator {
	if e.transportCoord == nil {
		e.transportCoord = transport.NewCoordinator(transport.CoordConfig{
			ProtocolVersion: uint64(PeerVersion),
			Entropy:         e.entropy,
		})
	}
	return e.transportCoord
}

// PlatformAgent returns the client platform identifier sent in TransportConfigEvent.
func (e *Engine) PlatformAgent() string {
	if e == nil {
		return ""
	}
	return e.platformAgent
}

// DirectMachineReport returns the live direct-path preference, daemon policy state,
// status summary, and retained diagnostic rounds for machineID.
func (e *Engine) DirectMachineReport(machineID string) (
	prefEnabled bool,
	daemonDisabled bool,
	status transport.Status,
	rounds []*transport.RoundData,
	blockedAt int64,
	blocked bool,
) {
	if e == nil || e.transportCoord == nil {
		return true, false, transport.Status{State: transport.StateUnsupported}, nil, 0, false
	}
	c := e.transportCoord
	prefEnabled = c.Enabled(machineID)
	status = c.Status(e.nowMs, machineID)
	daemonDisabled = status.Reason == transport.ReasonDisabled && prefEnabled
	rounds, blockedAt, blocked = c.Diagnostics(machineID)
	return prefEnabled, daemonDisabled, status, rounds, blockedAt, blocked
}

// SetDirectEnabled updates a computer's direct-path preference when transport is Core-owned.
func (e *Engine) SetDirectEnabled(ctx *StepContext, machineID string, enabled bool) {
	if machineID == "" {
		return
	}
	c := e.ensureTransportCoord()
	e.directPrefs[machineID] = enabled
	e.emitCoordEffects(ctx, c.SetEnabled(e.nowMs, machineID, enabled))
	ctx.Dirty.MarkDirect()
}

func (e *Engine) handleTransportConfig(ctx *StepContext, ev *corev1.TransportConfigEvent) {
	if ev == nil {
		return
	}
	c := e.ensureTransportCoord()
	e.platformAgent = ev.PlatformAgent
	pv := uint64(ev.ProtocolVersion)
	if pv == 0 {
		pv = uint64(PeerVersion)
	}
	effs := c.Configure(e.nowMs, transport.CoordConfig{
		Supported:       ev.Supported,
		Account:         ev.Account,
		ProtocolVersion: pv,
		ServerHost:      ev.ServerHost,
		ConfiguredStun:  ev.ConfiguredStun,
		Entropy:         e.entropy,
	})
	e.emitCoordEffects(ctx, effs)
	e.emitCoordEffects(ctx, c.SetLifecycle(e.nowMs, e.foreground))
	e.emitCoordEffects(ctx, c.SetMainLeg(e.nowMs, e.w.LivePushConnected))
	if len(e.directPrefs) > 0 {
		e.emitCoordEffects(ctx, c.LoadPreferences(e.nowMs, e.directPrefs))
	}
	e.syncDirectWatched(ctx)
	ctx.Dirty.MarkDirect()
}

func (e *Engine) handleDirectRandomBytes(ctx *StepContext, raw []byte) {
	if len(raw) == 0 {
		return
	}
	e.emitCoordEffects(ctx, e.ensureTransportCoord().ProvideRandomBytesWithShared(e.nowMs, raw, false))
}

func (e *Engine) handleDirectPreferencesLoaded(ctx *StepContext, ev *corev1.DirectPreferencesLoadedEvent) {
	if ev == nil {
		return
	}
	if e.directPrefs == nil {
		e.directPrefs = make(map[string]bool)
	}
	for _, entry := range ev.Entries {
		if entry != nil && entry.MachineId != "" {
			e.directPrefs[entry.MachineId] = entry.Enabled
		}
	}
	e.emitCoordEffects(ctx, e.ensureTransportCoord().LoadPreferences(e.nowMs, e.directPrefs))
	ctx.Dirty.MarkDirect()
}

func (e *Engine) handleDirectSignalReceived(ctx *StepContext, ev *corev1.DirectSignalReceivedEvent) {
	if ev == nil || len(ev.Raw) == 0 {
		return
	}
	e.emitCoordEffects(ctx, e.ensureTransportCoord().OnSignalReceived(e.nowMs, ev.Raw))
}

func (e *Engine) handleDirectSignalDelivery(ctx *StepContext, ev *corev1.DirectSignalDeliveryEvent) {
	if ev == nil || len(ev.FrameId) == 0 {
		return
	}
	e.emitCoordEffects(ctx, e.ensureTransportCoord().OnSignalDelivery(e.nowMs, ev.FrameId, ev.Status, ev.RetryAfterMs))
}

func (e *Engine) handleDirectPcCreated(ctx *StepContext, ev *corev1.DirectPcCreatedEvent) {
	if ev == nil {
		return
	}
	e.emitCoordEffects(ctx, e.ensureTransportCoord().OnPcCreated(e.nowMs, ev.PcId, ev.Ok))
}

func (e *Engine) handleDirectPcDescription(ctx *StepContext, ev *corev1.DirectPcDescriptionEvent) {
	if ev == nil {
		return
	}
	e.emitCoordEffects(ctx, e.ensureTransportCoord().OnPcDescription(e.nowMs, ev.PcId, ev.Sdp, ev.Ok))
}

func (e *Engine) handleDirectPcRemoteSet(ctx *StepContext, ev *corev1.DirectPcRemoteSetEvent) {
	if ev == nil {
		return
	}
	e.emitCoordEffects(ctx, e.ensureTransportCoord().OnPcRemoteSet(e.nowMs, ev.PcId, ev.Ok, ev.Error))
}

func (e *Engine) handleDirectPcCandidatesAdded(ctx *StepContext, ev *corev1.DirectPcCandidatesAddedEvent) {
	if ev == nil {
		return
	}
	e.emitCoordEffects(ctx, e.ensureTransportCoord().OnPcCandidatesAdded(e.nowMs, ev.PcId, ev.Ok, ev.Error))
}

func (e *Engine) handleDirectPcLocalCandidate(ctx *StepContext, ev *corev1.DirectPcLocalCandidateEvent) {
	if ev == nil {
		return
	}
	e.emitCoordEffects(ctx, e.ensureTransportCoord().OnPcLocalCandidate(e.nowMs, ev.PcId, ev.Candidate, ev.EndOfCandidates))
}

func (e *Engine) handleDirectPcState(ctx *StepContext, ev *corev1.DirectPcStateEvent) {
	if ev == nil {
		return
	}
	var kind transport.StateKind
	switch ev.Kind {
	case corev1.DirectStateKind_DIRECT_STATE_KIND_GATHER:
		kind = transport.StateGather
	case corev1.DirectStateKind_DIRECT_STATE_KIND_ICE:
		kind = transport.StateICE
	case corev1.DirectStateKind_DIRECT_STATE_KIND_CONN:
		kind = transport.StateConn
	case corev1.DirectStateKind_DIRECT_STATE_KIND_DTLS:
		kind = transport.StateDTLS
	case corev1.DirectStateKind_DIRECT_STATE_KIND_SCTP:
		kind = transport.StateSCTP
	case corev1.DirectStateKind_DIRECT_STATE_KIND_DC:
		kind = transport.StateDC
	default:
		return
	}
	e.emitCoordEffects(ctx, e.ensureTransportCoord().OnPcState(e.nowMs, ev.PcId, kind, ev.Value))
}

func (e *Engine) handleDirectPcStunError(ctx *StepContext, ev *corev1.DirectPcStunErrorEvent) {
	if ev == nil {
		return
	}
	e.emitCoordEffects(ctx, e.ensureTransportCoord().OnPcStunError(e.nowMs, ev.PcId, int(ev.Code), ev.Address))
}

func (e *Engine) handleDirectPcClosed(ctx *StepContext, ev *corev1.DirectPcClosedEvent) {
	if ev == nil {
		return
	}
	e.emitCoordEffects(ctx, e.ensureTransportCoord().OnPcClosed(e.nowMs, ev.PcId))
}

func (e *Engine) handleDirectDcState(ctx *StepContext, ev *corev1.DirectDcStateEvent) {
	if ev == nil {
		return
	}
	e.emitCoordEffects(ctx, e.ensureTransportCoord().OnDcState(e.nowMs, ev.PcId, ev.Open))
}

func (e *Engine) handleDirectDcStats(ctx *StepContext, ev *corev1.DirectDcStatsEvent) {
	if ev == nil {
		return
	}
	var report *transport.StatsReport
	if ev.Report != nil {
		report = convertDirectStatsReport(ev.Report)
	}
	e.emitCoordEffects(ctx, e.ensureTransportCoord().OnDcStats(e.nowMs, ev.PcId, report))
}

func (e *Engine) handleDirectPcProbeReceived(ctx *StepContext, ev *corev1.DirectPcProbeReceivedEvent) {
	if ev == nil || len(ev.Raw) == 0 {
		return
	}
	e.emitCoordEffects(ctx, e.ensureTransportCoord().OnPcProbeReceived(e.nowMs, ev.PcId, ev.Raw))
}

func convertDirectStatsReport(in *corev1.DirectStatsReport) *transport.StatsReport {
	out := &transport.StatsReport{
		DTLS: in.DtlsState,
		DC:   in.DcState,
	}
	if len(in.Pairs) == 0 {
		return out
	}
	out.Locals = make([]transport.StatsCandidate, 0, len(in.Pairs))
	out.Remotes = make([]transport.StatsCandidate, 0, len(in.Pairs))
	out.Pairs = make([]transport.StatsPair, 0, len(in.Pairs))
	for i, p := range in.Pairs {
		if p == nil {
			continue
		}
		lid := "l" + strconv.Itoa(i)
		rid := "r" + strconv.Itoa(i)
		out.Locals = append(out.Locals, transport.StatsCandidate{
			ID:      lid,
			Type:    statsCandTypeString(p.LocalType),
			Address: statsCandAddress(p.LocalFamily),
		})
		out.Remotes = append(out.Remotes, transport.StatsCandidate{
			ID:      rid,
			Type:    statsCandTypeString(p.RemoteType),
			Address: statsCandAddress(p.RemoteFamily),
		})
		out.Pairs = append(out.Pairs, transport.StatsPair{
			Local:      lid,
			Remote:     rid,
			State:      p.State,
			Nominated:  p.Nominated,
			RTTSeconds: p.CurrentRttSeconds,
			HasRTT:     p.HasCurrentRtt,
			Sent:       int(p.RequestsSent),
			Got:        int(p.ResponsesReceived),
		})
	}
	return out
}

func statsCandTypeString(t corev1.DirectCandType) string {
	switch t {
	case corev1.DirectCandType_DIRECT_CAND_TYPE_HOST:
		return "host"
	case corev1.DirectCandType_DIRECT_CAND_TYPE_SRFLX:
		return "srflx"
	case corev1.DirectCandType_DIRECT_CAND_TYPE_PRFLX:
		return "prflx"
	case corev1.DirectCandType_DIRECT_CAND_TYPE_RELAY:
		return "relay"
	default:
		return "other"
	}
}

func statsCandAddress(f corev1.DirectCandFamily) string {
	switch f {
	case corev1.DirectCandFamily_DIRECT_CAND_FAMILY_V4:
		return "198.51.100.1"
	case corev1.DirectCandFamily_DIRECT_CAND_FAMILY_V6:
		return "2001:db8::1"
	default:
		return ""
	}
}

func (e *Engine) handleDirectRetryNow(ctx *StepContext, act *corev1.DirectRetryNowAction) {
	if act == nil || act.MachineId == "" {
		return
	}
	e.emitCoordEffects(ctx, e.ensureTransportCoord().RetryNow(e.nowMs, act.MachineId))
}

func (e *Engine) handleDirectRefreshDiagnostics(ctx *StepContext, act *corev1.DirectRefreshDiagnosticsAction) {
	if act == nil || act.MachineId == "" {
		return
	}
	e.emitCoordEffects(ctx, e.ensureTransportCoord().RefreshDiagnostics(e.nowMs, act.MachineId))
}

func (e *Engine) handleDirectBulkTransferBegin(ctx *StepContext, ev *corev1.DirectBulkTransferBeginEvent) {
	if ev == nil {
		return
	}
	c := e.ensureTransportCoord()
	if ev.UploadTransferId != "" {
		c.BeginTransfer(ev.MachineId, ev.UploadTransferId, ev.UploadHasFilename)
	}
	var dec transport.RouteDecision
	if ev.InteractiveDual {
		dec, _ = c.PrepareInteractive(e.nowMs, ev.MachineId, ev.DeadlineMs)
	} else {
		var h transport.Hint
		wantRoute := false
		switch corev1.CommandRouteHint(ev.RouteHint) {
		case corev1.CommandRouteHint_COMMAND_ROUTE_HINT_READ:
			h = transport.Hint{Use: transport.UseRead}
			wantRoute = true
		case corev1.CommandRouteHint_COMMAND_ROUTE_HINT_BULK:
			upload := transport.UploadNone
			if ev.BulkUpload {
				if u, ok := c.TransferUploadKind(ev.MachineId, ev.BulkFileKey); ok {
					upload = u
				} else if ev.UploadTransferId != "" {
					if u, ok := c.TransferUploadKind(ev.MachineId, ev.UploadTransferId); ok {
						upload = u
					}
				}
			}
			h = transport.Hint{
				Use:    transport.UseBulk,
				File:   ev.BulkFileKey,
				Upload: upload,
				Bytes:  ev.BulkBytes,
			}
			wantRoute = !(ev.BulkUpload && h.Upload == transport.UploadNone)
		}
		if wantRoute {
			dec = c.PrepareAttempt(e.nowMs, ev.MachineId, h, ev.DeadlineMs, ev.ForceRelay)
		}
	}
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_DirectBulkRoute{
			DirectBulkRoute: &corev1.DirectBulkRouteEffect{
				CallId:    ev.CallId,
				UseDirect: dec.UseDirect,
				PcId:      dec.PCID,
				TicketId:  dec.TicketID,
				TimeoutMs: dec.TimeoutMs,
			},
		},
	})
	e.syncDirectTimers(ctx)
}

func (e *Engine) handleDirectBulkTransferDone(ctx *StepContext, ev *corev1.DirectBulkTransferDoneEvent) {
	if ev == nil {
		return
	}
	c := e.ensureTransportCoord()
	if ev.FinishUploadTransferId != "" && ev.MachineId != "" {
		c.EndTransfer(ev.MachineId, ev.FinishUploadTransferId)
	}
	if ev.TicketId != 0 {
		if ev.Ok {
			e.emitCoordEffects(ctx, c.ObserveTicket(e.nowMs, ev.TicketId, ev.RouteWasDirect, ev.ObservedBytes, ev.ElapsedMs))
		} else if ev.RouteWasDirect {
			e.emitCoordEffects(ctx, c.FailTicket(e.nowMs, ev.TicketId))
		} else {
			c.ReleaseTicket(ev.TicketId)
		}
	}
}

func (e *Engine) handleTerminalStream(ctx *StepContext, ev *corev1.TerminalStreamEvent) {
	if ev == nil || ev.TerminalId == "" {
		return
	}
	c := e.ensureTransportCoord()
	switch ev.Kind {
	case corev1.TerminalStreamEventKind_TERMINAL_STREAM_EVENT_KIND_LIVE:
		e.emitCoordEffects(ctx, c.TerminalLive(e.nowMs, ev.TerminalId))
	case corev1.TerminalStreamEventKind_TERMINAL_STREAM_EVENT_KIND_CLOSED:
		online := e.w.World.Connection == lists.ConnConnected
		e.emitCoordEffects(ctx, c.TerminalClosed(e.nowMs, ev.TerminalId, ev.Opened, online))
	case corev1.TerminalStreamEventKind_TERMINAL_STREAM_EVENT_KIND_MANUAL_RECONNECT:
		e.emitCoordEffects(ctx, c.TerminalManual(ev.TerminalId))
	case corev1.TerminalStreamEventKind_TERMINAL_STREAM_EVENT_KIND_DISPOSED:
		c.ClearTerminal(ev.TerminalId)
		e.syncDirectTimers(ctx)
	}
}

func (e *Engine) handleTerminalDirectOpen(ctx *StepContext, ev *corev1.TerminalDirectOpenEvent) {
	if ev == nil || ev.StreamId == "" {
		return
	}
	e.emitCoordEffects(ctx, e.ensureTransportCoord().TerminalDirectOpen(e.nowMs, ev.StreamId, ev.MachineId, ev.SessionId, ev.RelayLegId))
}

func (e *Engine) handleTerminalDirectClose(ctx *StepContext, ev *corev1.TerminalDirectCloseEvent) {
	if ev == nil || ev.StreamId == "" {
		return
	}
	e.emitCoordEffects(ctx, e.ensureTransportCoord().TerminalDirectClose(e.nowMs, ev.StreamId))
}

func (e *Engine) handleTerminalDirectLeg(ctx *StepContext, ev *corev1.TerminalDirectLegEvent) {
	if ev == nil || ev.StreamId == "" {
		return
	}
	e.emitCoordEffects(ctx, e.ensureTransportCoord().TerminalDirectLeg(e.nowMs, ev.StreamId, ev.LegId, ev.Direct, ev.Open, ev.PcId))
}

func (e *Engine) handleTerminalDirectInput(ctx *StepContext, ev *corev1.TerminalDirectInputEvent) {
	if ev == nil || ev.StreamId == "" {
		return
	}
	e.emitCoordEffects(ctx, e.ensureTransportCoord().TerminalDirectInput(e.nowMs, ev.StreamId, ev.Seq, ev.IsInput, ev.SealedEnvelope, ev.FrameHash))
}

func (e *Engine) handleTerminalDirectOutput(ctx *StepContext, ev *corev1.TerminalDirectOutputEvent) {
	if ev == nil || ev.StreamId == "" {
		return
	}
	e.emitCoordEffects(ctx, e.ensureTransportCoord().TerminalDirectOutput(e.nowMs, ev.StreamId, ev.Seq, ev.FrameHash, ev.Payload, ev.FromDirect, ev.PcId))
}

func (e *Engine) handleTerminalDirectConfirmRebuild(ctx *StepContext, ev *corev1.TerminalDirectConfirmRebuildAction) {
	if ev == nil || ev.StreamId == "" {
		return
	}
	e.emitCoordEffects(ctx, e.ensureTransportCoord().TerminalDirectConfirmRebuild(e.nowMs, ev.StreamId))
}

func (e *Engine) syncDirectWatched(ctx *StepContext) {
	if e.transportCoord == nil {
		return
	}
	byMachine := make(map[string][]string)
	for sid := range e.watchedSessions {
		s := e.sessions[sid]
		if s == nil || s.MachineID == "" {
			continue
		}
		list := byMachine[s.MachineID]
		if len(list) >= transport.WatchedPerPeer {
			continue
		}
		byMachine[s.MachineID] = append(list, sid)
	}
	e.emitCoordEffects(ctx, e.transportCoord.SetWatched(e.nowMs, byMachine))
}

func (e *Engine) emitCoordEffects(ctx *StepContext, effs []transport.CoordEffect) {
	c := e.transportCoord
	for _, raw := range effs {
		switch eff := raw.(type) {
		case transport.CoordNeedEntropy:
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_RequestEntropy{
					RequestEntropy: &corev1.RequestEntropyEffect{
						MinRandomBytes: int32(eff.MinRandomBytes),
					},
				},
			})

		case transport.CoordChanged:
			changedReady := false
			for id := range e.machines {
				nextReady := c != nil && c.Ready(e.nowMs, id, transport.Hint{Use: transport.UseRead})
				if e.directReady[id] != nextReady {
					e.directReady[id] = nextReady
					changedReady = true
				}
			}
			for id := range e.directReady {
				if e.machines[id] == nil && e.directReady[id] {
					delete(e.directReady, id)
					changedReady = true
				}
			}
			if changedReady {
				e.updateLivePollers(ctx)
				ctx.Dirty.Add(true, true, true)
			}
			ctx.Dirty.MarkDirect()

		case transport.CoordPersistEnabled:
			e.directPrefs[eff.MachineID] = eff.Enabled
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_DirectPreference{
					DirectPreference: &corev1.DirectPreferenceEffect{
						MachineId: eff.MachineID,
						Enabled:   eff.Enabled,
					},
				},
			})

		case transport.CoordAuthorize:
			e.requestSyncSecurity(ctx, true)

		case transport.CoordKeySign:
			if e.secEngine != nil {
				tag := e.allocSecOp(&pendingSecOp{kind: secOpCoordSign, opID: eff.OpID})
				e.secEngine.RequestKeySignTag(eff.Message, tag)
				e.emitRawEffects(ctx, e.secEngine.DrainEffects())
			}

		case transport.CoordKeyAgree:
			if e.secEngine != nil {
				var peerX [32]byte
				copy(peerX[:], eff.PeerXPub[:])
				tag := e.allocSecOp(&pendingSecOp{kind: secOpCoordAgree, opID: eff.OpID})
				e.secEngine.RequestKeyAgreeTag(peerX, true, tag)
				e.emitRawEffects(ctx, e.secEngine.DrainEffects())
			}

		case transport.CoordSignalSend:
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_DirectSignalSend{
					DirectSignalSend: &corev1.DirectSignalSendEffect{
						ToDevice:   eff.ToDevice,
						FrameClass: eff.FrameClass,
						FrameId:    eff.FrameID,
						Sealed:     eff.Sealed,
						DeadlineMs: eff.DeadlineMs,
						Critical:   eff.Critical,
					},
				},
			})

		case transport.CoordPcCreate:
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_DirectPcCreate{
					DirectPcCreate: &corev1.DirectPcCreateEffect{
						RoundId:     uint64(eff.RoundID),
						MachineId:   eff.MachineID,
						PcId:        eff.PCID,
						Use:         uint32(eff.Use),
						Generation:  eff.Generation,
						StunServers: eff.STUN,
					},
				},
			})

		case transport.CoordPcDescribe:
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_DirectPcDescribe{
					DirectPcDescribe: &corev1.DirectPcDescribeEffect{
						PcId:       eff.PCID,
						Offer:      eff.Offer,
						IceRestart: eff.ICERestart,
					},
				},
			})

		case transport.CoordPcSetRemote:
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_DirectPcSetRemote{
					DirectPcSetRemote: &corev1.DirectPcSetRemoteEffect{
						PcId:  eff.PCID,
						Sdp:   eff.SDP,
						Offer: eff.Offer,
					},
				},
			})

		case transport.CoordPcAddCandidates:
			cands := make([]*corev1.DirectIceCandidate, 0, len(eff.Candidates))
			for _, cand := range eff.Candidates {
				cands = append(cands, toProtoCandidate(cand))
			}
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_DirectPcAddCandidates{
					DirectPcAddCandidates: &corev1.DirectPcAddCandidatesEffect{
						PcId:            eff.PCID,
						Candidates:      cands,
						EndOfCandidates: eff.End,
					},
				},
			})

		case transport.CoordPcConfirm:
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_DirectPcConfirm{
					DirectPcConfirm: &corev1.DirectPcConfirmEffect{
						PcId:      eff.PCID,
						MachineId: eff.MachineID,
						Use:       uint32(eff.Use),
					},
				},
			})

		case transport.CoordPcClose:
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_DirectPcClose{
					DirectPcClose: &corev1.DirectPcCloseEffect{
						PcId:   eff.PCID,
						Reason: eff.Reason,
					},
				},
			})

		case transport.CoordDcReadStats:
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_DirectDcReadStats{
					DirectDcReadStats: &corev1.DirectDcReadStatsEffect{
						PcId: eff.PCID,
					},
				},
			})

		case transport.CoordPcSendProbe:
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_DirectPcSendProbe{
					DirectPcSendProbe: &corev1.DirectPcSendProbeEffect{
						PcId: eff.PCID,
						Raw:  eff.Raw,
					},
				},
			})

		case transport.CoordTerminalOpen:
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_TerminalReopen{
					TerminalReopen: &corev1.TerminalReopenEffect{
						TerminalId: eff.TerminalID,
					},
				},
			})

		case transport.CoordTerminalSend:
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_TerminalDirectSend{
					TerminalDirectSend: &corev1.TerminalDirectSendEffect{
						StreamId:       eff.StreamID,
						SendRelay:      eff.SendRelay,
						DirectPcId:     eff.DirectPCID,
						SealedEnvelope: eff.SealedEnvelope,
					},
				},
			})

		case transport.CoordTerminalDeliver:
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_TerminalDirectDeliver{
					TerminalDirectDeliver: &corev1.TerminalDirectDeliverEffect{
						StreamId: eff.StreamID,
						Payloads: eff.Payloads,
					},
				},
			})

		case transport.CoordTerminalAttachRelay:
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_TerminalDirectAttachRelay{
					TerminalDirectAttachRelay: &corev1.TerminalDirectAttachRelayEffect{
						StreamId:  eff.StreamID,
						MachineId: eff.MachineID,
						LegId:     eff.LegID,
					},
				},
			})

		case transport.CoordTerminalState:
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_TerminalDirectState{
					TerminalDirectState: &corev1.TerminalDirectStateEffect{
						StreamId:          eff.StreamID,
						State:             eff.State,
						Reason:            eff.Reason,
						NeedsConfirmation: eff.NeedsConfirmation,
					},
				},
			})

		case transport.CoordRetryNowResult:
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_DirectRetryNowResult{
					DirectRetryNowResult: &corev1.DirectRetryNowResultEffect{
						MachineId: eff.MachineID,
						Started:   eff.Started,
					},
				},
			})
		}
	}
	e.syncDirectTimers(ctx)
}

func toProtoCandidate(c peerwire.Candidate) *corev1.DirectIceCandidate {
	return &corev1.DirectIceCandidate{
		Line:       c.Original,
		Ufrag:      c.Ufrag,
		Mid:        c.Mid,
		MlineIndex: uint32(c.Index),
	}
}

func (e *Engine) pruneLateDirectTickets() {
	if len(e.lateDirectTickets) == 0 {
		return
	}
	for aid, tid := range e.lateDirectTickets {
		dl := e.lateDirectDeadline[aid]
		if e.nowMs >= dl+60_000 {
			delete(e.lateDirectTickets, aid)
			delete(e.lateDirectDeadline, aid)
			if e.transportCoord != nil && tid != 0 {
				e.transportCoord.ReleaseTicket(tid)
			}
		}
	}
}

func (e *Engine) syncOneDirectTimer(ctx *StepContext, kind corev1.TimerKind, dueOK bool, dueAt int64, scheduled *bool, curAt *int64) {
	if !dueOK {
		if *scheduled {
			*scheduled = false
			*curAt = 0
			e.cancelTimer(ctx, kind, "")
		}
		return
	}
	if *scheduled && *curAt == dueAt {
		return
	}
	*scheduled = true
	*curAt = dueAt
	delay := dueAt - e.nowMs
	if delay < 1 {
		delay = 1
	}
	e.scheduleTimer(ctx, kind, "", delay)
}

func (e *Engine) syncDirectTimers(ctx *StepContext) {
	e.pruneLateDirectTickets()
	if e.transportCoord == nil {
		return
	}
	engAt, engOK, sessAt, sessOK, termAt, termOK := e.transportCoord.NextTimers(e.nowMs)
	e.syncOneDirectTimer(ctx, corev1.TimerKind_TIMER_KIND_DIRECT_ENGINE, engOK, engAt, &e.directEngineScheduled, &e.directEngineAt)
	e.syncOneDirectTimer(ctx, corev1.TimerKind_TIMER_KIND_DIRECT_SESSION, sessOK, sessAt, &e.directSessionScheduled, &e.directSessionAt)
	e.syncOneDirectTimer(ctx, corev1.TimerKind_TIMER_KIND_TERMINAL_REOPEN, termOK, termAt, &e.directTermScheduled, &e.directTermAt)
}

func (e *Engine) clearDirectTimerTracking() {
	e.directEngineScheduled = false
	e.directEngineAt = 0
	e.directSessionScheduled = false
	e.directSessionAt = 0
	e.directTermScheduled = false
	e.directTermAt = 0
}

// isLegacyInteractiveCommand matches the 7 interactive commands accepted on
// Phase 1 daemons over direct use=1 before Phase 2 capability negotiation.
func isLegacyInteractiveCommand(prep *PreparedCommand) bool {
	if prep == nil || prep.Command == nil {
		return false
	}
	switch prep.Command.Kind.(type) {
	case *v1.Command_Send,
		*v1.Command_Interrupt,
		*v1.Command_ResolveApproval,
		*v1.Command_AnswerQuestion,
		*v1.Command_AnswerPlan,
		*v1.Command_CancelInput,
		*v1.Command_ResumeInput:
		return true
	}
	return false
}

func isDualSendCommand(prep *PreparedCommand) bool {
	if prep == nil || prep.Command == nil {
		return false
	}
	return prep.RouteHint == corev1.CommandRouteHint_COMMAND_ROUTE_HINT_NORMAL
}

func (e *Engine) trackCommandTransferLifecycle(cmd *v1.Command, machineID string) {
	if cmd == nil || e.transportCoord == nil {
		return
	}
	switch k := cmd.Kind.(type) {
	case *v1.Command_BeginImageUpload:
		if k.BeginImageUpload != nil && k.BeginImageUpload.TransferId != "" {
			e.transportCoord.BeginTransfer(machineID, k.BeginImageUpload.TransferId, k.BeginImageUpload.FileName != "")
		}
	case *v1.Command_CompleteImageUpload:
		if k.CompleteImageUpload != nil && k.CompleteImageUpload.TransferId != "" {
			e.transportCoord.EndTransfer(machineID, k.CompleteImageUpload.TransferId)
		}
	case *v1.Command_AbortImageUpload:
		if k.AbortImageUpload != nil && k.AbortImageUpload.TransferId != "" {
			e.transportCoord.EndTransfer(machineID, k.AbortImageUpload.TransferId)
		}
	}
}

func (e *Engine) commandTransportHint(prep *PreparedCommand) (transport.Hint, bool) {
	if prep == nil || prep.Command == nil || e.transportCoord == nil {
		return transport.Hint{}, false
	}
	switch k := prep.Command.Kind.(type) {
	case *v1.Command_GetSharedFileChunk:
		if k.GetSharedFileChunk == nil {
			return transport.Hint{}, false
		}
		fileKey := prep.BulkFileKey
		if fileKey == "" {
			fileKey = k.GetSharedFileChunk.SessionId + "/" + k.GetSharedFileChunk.FileId
		}
		return transport.Hint{
			Use:   transport.UseBulk,
			File:  fileKey,
			Bytes: prep.BulkBytes,
		}, true
	case *v1.Command_PutImageChunk:
		if k.PutImageChunk == nil {
			return transport.Hint{}, false
		}
		u, ok := e.transportCoord.TransferUploadKind(prep.MachineID, k.PutImageChunk.TransferId)
		if !ok {
			return transport.Hint{}, false
		}
		return transport.Hint{
			Use:    transport.UseBulk,
			File:   k.PutImageChunk.TransferId,
			Upload: u,
			Bytes:  int64(len(k.PutImageChunk.Data)),
		}, true
	default:
		if prep.RouteHint == corev1.CommandRouteHint_COMMAND_ROUTE_HINT_READ {
			return transport.Hint{Use: transport.UseRead}, true
		}
		return transport.Hint{}, false
	}
}
