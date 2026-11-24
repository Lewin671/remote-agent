package linkcore

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
)

func mkTermFrame(seq uint64, payload string, deadline uint64, isInput bool) TermFrame {
	b := []byte(payload)
	h := sha256.Sum256(b)
	return TermFrame{
		Seq:         seq,
		PayloadHash: h,
		DeadlineMs:  deadline,
		RawBytes:    len(b) + 64,
		IsInput:     isInput,
		InputBytes:  uint32(len(b)),
		PlainBytes:  b,
		RawEnvelope: append([]byte("env:"), b...),
	}
}

// TestTerminalTwoFakePathsFaultInjection exercises the full Phase 3 terminal state machine
// over two fault-injectable fake paths (direct + relay): reorder before Accept, duplicate ACK
// without closing, conflict rejection, fail-stop non-bypassability, Resize failure non-fail-stop,
// 3% packet loss + path switching with 0 non-daemon fail-stops, output replay bounds,
// hot-standby output mirror threshold, bounded input cache, AttachPath, InputStatus frozen
// bidirectional snapshot pagination, Barrier serialization, and Registry caps.
func TestTerminalTwoFakePathsFaultInjection(t *testing.T) {
	const base uint64 = 1_700_000_000_000

	t.Run("reorder_before_accept_and_duplicate_conflict", func(t *testing.T) {
		s := NewTerminalStream("stream-0000000000000001", "sess-1", "peer-1", "inst-a", base)
		if err := s.AttachLeg("relay", "leg-r1", "inst-a", 1); err != nil {
			t.Fatal(err)
		}
		if err := s.AttachLeg("direct", "leg-d1", "inst-a", 1); err != nil {
			t.Fatal(err)
		}
		f1 := mkTermFrame(1, "ls\n", base+60_000, true)
		f2 := mkTermFrame(2, "pwd\n", base+60_000, true)

		// Frame 2 arrives first on direct: buffered, NOT run.
		act, run, err := s.ReceiveInput(base+10, f2)
		if err != nil || act != TermInputBuffered || run != nil {
			t.Fatalf("expected buffered for seq 2, got act=%d err=%v", act, err)
		}
		if s.InputGapDue() != base+10+TermGapTimeoutMs {
			t.Fatalf("expected input gap due at %d, got %d", base+10+TermGapTimeoutMs, s.InputGapDue())
		}
		// Duplicate seq 2 on relay with same hash does not refresh gap or double-charge.
		act, _, err = s.ReceiveInput(base+2000, f2)
		if err != nil || act != TermInputBuffered {
			t.Fatalf("expected buffered duplicate seq 2, got act=%d err=%v", act, err)
		}
		if s.InputGapDue() != base+10+TermGapTimeoutMs {
			t.Fatalf("duplicate must not refresh gap deadline: got %d", s.InputGapDue())
		}
		// Conflicting seq 2 rejected.
		bad2 := mkTermFrame(2, "rm -rf /\n", base+60_000, true)
		if act, _, err := s.ReceiveInput(base+2001, bad2); act != TermInputReject || !errors.Is(err, ErrTermConflict) {
			t.Fatalf("expected conflict on seq 2, got act=%d err=%v", act, err)
		}

		// Frame 1 arrives on relay -> runs seq 1, then CompleteInput pops seq 2.
		act, run, err = s.ReceiveInput(base+3000, f1)
		if err != nil || act != TermInputRun || run == nil || run.Seq != 1 {
			t.Fatalf("expected run seq 1, got act=%d err=%v", act, err)
		}
		next, _, _, _, _ := s.CompleteInput(base+3005, 1, TermOutcomeApplied, 3, 3, 0)
		if next == nil || next.Seq != 2 {
			t.Fatalf("expected seq 2 popped after seq 1, got %+v", next)
		}
		next2, _, _, _, _ := s.CompleteInput(base+3010, 2, TermOutcomeApplied, 4, 4, 0)
		if next2 != nil || s.Processed() != 2 || s.InputSuccess() != 2 || s.InputGapDue() != 0 {
			t.Fatalf("unexpected state after seq 2: proc=%d ok=%d gap=%d", s.Processed(), s.InputSuccess(), s.InputGapDue())
		}

		// Duplicate seq 1 after completion -> ACK without closing stream!
		act, _, err = s.ReceiveInput(base+3020, f1)
		if err != nil || act != TermInputDuplicateACK {
			t.Fatalf("expected duplicate ACK for seq 1, got act=%d err=%v", act, err)
		}
		// Conflicting seq 1 after completion -> rejected.
		if act, _, err := s.ReceiveInput(base+3025, mkTermFrame(1, "other", base+60_000, true)); act != TermInputReject || !errors.Is(err, ErrTermConflict) {
			t.Fatalf("expected conflict for completed seq 1, got act=%d err=%v", act, err)
		}
	})

	t.Run("fail_stop_non_bypassable_and_resize_non_fail_stop", func(t *testing.T) {
		s := NewTerminalStream("stream-0000000000000002", "sess-1", "peer-1", "inst-a", base)
		// Resize failure (seq 1) advances processed without failing input or advancing inputSuccess.
		r1 := mkTermFrame(1, "resize", base+60_000, false)
		act, _, err := s.ReceiveInput(base, r1)
		if err != nil || act != TermInputRun {
			t.Fatalf("expected run resize, got %d %v", act, err)
		}
		_, _, fs, _, _ := s.CompleteInput(base+5, 1, TermOutcomeNonInputFailed, 0, 0, 1)
		if fs != nil || s.FailedInputSeq() != 0 || s.Processed() != 1 || s.InputSuccess() != 0 {
			t.Fatalf("resize failure must not trigger fail-stop: fs=%v failed=%d proc=%d ok=%d", fs, s.FailedInputSeq(), s.Processed(), s.InputSuccess())
		}

		// Input seq 2 partially injects 256/600 bytes -> fail-stop!
		big := string(bytes.Repeat([]byte("a"), 600))
		i2 := mkTermFrame(2, big, base+60_000, true)
		if act, _, err := s.ReceiveInput(base+10, i2); err != nil || act != TermInputRun {
			t.Fatalf("expected run input 2: %d %v", act, err)
		}
		_, emitACK, fs, _, _ := s.CompleteInput(base+15, 2, TermOutcomeInputPartialUnknown, 256, 512, 2)
		if !emitACK || fs == nil || s.FailedInputSeq() != 2 || fs.DefinitelyInjected != 256 || fs.PossiblyInjected != 512 {
			t.Fatalf("expected immediate fail-stop on seq 2, got ack=%v fs=%+v", emitACK, fs)
		}

		// Subsequent input seq 3 arriving on ANY leg is marked blocked and never injected!
		i3 := mkTermFrame(3, "next\n", base+60_000, true)
		if act, _, err := s.ReceiveInput(base+20, i3); err != nil || act != TermInputRun {
			t.Fatalf("expected run (for blocking) seq 3: %d %v", act, err)
		}
		if !s.IsInputBlocked(i3) {
			t.Fatal("seq 3 must be reported as blocked after fail-stop")
		}
		_, _, _, _, _ = s.CompleteInput(base+21, 3, TermOutcomeApplied, 5, 5, 0)
		snap := s.FreezeSnapshot(base+22, false)
		if snap.State.ToDaemon.Details[2].Outcome != TermOutcomeInputBlocked {
			t.Fatalf("expected seq 3 forced to INPUT_BLOCKED, got %d", snap.State.ToDaemon.Details[2].Outcome)
		}
	})

	t.Run("three_percent_loss_and_single_path_switch_zero_spurious_fail_stops", func(t *testing.T) {
		daemon := NewTerminalStream("stream-0000000000000003", "sess-1", "peer-1", "inst-a", base)
		client := NewTerminalStream("stream-0000000000000003", "sess-1", "peer-1", "inst-a", base)
		_ = daemon.AttachLeg("relay", "leg-r1", "inst-a", 1)
		_ = daemon.AttachLeg("direct", "leg-d1", "inst-a", 1)

		// Deterministic pseudo-random 3% drop per path with direct dropping completely at seq 250..350 and re-attaching at 351.
		state := uint64(0x9e3779b97f4a7c15)
		drop3Pct := func() bool {
			state ^= state << 13
			state ^= state >> 7
			state ^= state << 17
			return (state % 100) < 3
		}

		now := base
		injected := 0
		for seq := uint64(1); seq <= 500; seq++ {
			now += 10
			f := mkTermFrame(seq, "k", now+60_000, true)
			if err := client.CacheInput(now, f); err != nil {
				t.Fatalf("cache seq %d: %v", seq, err)
			}
			if seq == 250 {
				daemon.CloseLeg("direct", 1)
			}
			if seq == 351 {
				if err := daemon.AttachLeg("direct", "leg-d2", "inst-a", 2); err != nil {
					t.Fatal(err)
				}
			}
			// Hot-standby dual-send over both active legs.
			delivered := false
			for _, leg := range []string{"direct", "relay"} {
				if !daemon.HasLeg(leg) || drop3Pct() {
					continue
				}
				delivered = true
				act, run, err := daemon.ReceiveInput(now, f)
				if err != nil {
					t.Fatalf("seq %d on %s unexpected err: %v", seq, leg, err)
				}
				if act == TermInputRun && run != nil {
					for run != nil {
						injected++
						next, _, fs, _, _ := daemon.CompleteInput(now, run.Seq, TermOutcomeApplied, run.InputBytes, run.InputBytes, 0)
						if fs != nil {
							t.Fatalf("spurious fail-stop at seq %d", run.Seq)
						}
						run = next
					}
				}
			}
			// If both legs dropped this packet, recover via InputStatus + resend on old stream!
			if !delivered {
				snap, ok := daemon.QueryStatus(now+5, "q", now+5000)
				if !ok {
					t.Fatal("expected idle status")
				}
				resend, needBarrier, needConfirm := client.ReconcileStatus(now+5, snap)
				if needBarrier || needConfirm {
					t.Fatalf("unexpected barrier=%v confirm=%v", needBarrier, needConfirm)
				}
				for _, rf := range resend {
					act, run, err := daemon.ReceiveInput(now+6, rf)
					if err != nil {
						t.Fatalf("resend seq %d err: %v", rf.Seq, err)
					}
					if act == TermInputRun && run != nil {
						for run != nil {
							injected++
							next, _, _, _, _ := daemon.CompleteInput(now+6, run.Seq, TermOutcomeApplied, run.InputBytes, run.InputBytes, 0)
							run = next
						}
					}
				}
			}
			if client.ApplyInputAck(daemon.CurrentInputWatermark()) {
				t.Fatalf("unexpected fail-stop ACK at seq %d", seq)
			}
			// Once client confirmed the ACK, daemon may reclaim confirmed applied details on a healthy stream.
			daemon.PruneConfirmedDetails(daemon.InputSuccess())
		}
		if daemon.FailedInputSeq() != 0 || client.FailedInputSeq() != 0 || injected != 500 || daemon.InputSuccess() != 500 {
			t.Fatalf("expected 500 clean inputs with 0 fail-stops, got injected=%d ok=%d failed=%d", injected, daemon.InputSuccess(), daemon.FailedInputSeq())
		}
	})

	t.Run("output_reorder_gap_replay_and_mirror_threshold", func(t *testing.T) {
		s := NewTerminalStream("stream-0000000000000004", "sess-1", "peer-1", "inst-a", base)
		// Hot-standby output mirror: <= 4 KiB allowed up to 64 KiB burst; > 4 KiB never mirrored.
		if s.ShouldMirrorOutput(int64(base), TermHotStandbyMaxBytes+1) {
			t.Fatal(">4 KiB output must not mirror")
		}
		mirrored := 0
		for i := 0; i < 20; i++ {
			if s.ShouldMirrorOutput(int64(base), TermHotStandbyMaxBytes) {
				mirrored++
			}
		}
		if mirrored != 16 { // 64 KiB / 4 KiB = 16 frames burst
			t.Fatalf("expected 16 burst mirrored frames, got %d", mirrored)
		}
		// 1 second later, 32 KiB (8 x 4 KiB) refills.
		refilled := 0
		for i := 0; i < 10; i++ {
			if s.ShouldMirrorOutput(int64(base+1000), TermHotStandbyMaxBytes) {
				refilled++
			}
		}
		if refilled != 8 {
			t.Fatalf("expected 8 refilled frames after 1s, got %d", refilled)
		}

		// Output gap: seq 2 arrives before seq 1 -> gap expires at +10s -> network recovery, NOT fail-stop.
		o2 := mkTermFrame(2, "out-2", base+60_000, false)
		act, ready, err := s.ReceiveOutput(base, o2)
		if err != nil || act != TermOutputBuffered || ready != nil {
			t.Fatalf("expected buffered output seq 2: %d %v", act, err)
		}
		s.Advance(base + TermGapTimeoutMs)
		if !s.NetworkRecovery || s.FailedInputSeq() != 0 {
			t.Fatalf("expected network recovery without input fail-stop, rec=%v failed=%d", s.NetworkRecovery, s.FailedInputSeq())
		}

		// Replay window overflow sets network recovery, NOT input fail-stop.
		for seq := uint64(1); seq <= TermOutputReplayMaxFrames+10; seq++ {
			s.RecordOutputReplay(base+seq, mkTermFrame(seq, "x", base+60_000, false))
		}
		if _, err := s.ReplayOutput(base+1000, 1, 10, 0, 0); !errors.Is(err, ErrTermReplayWindow) {
			t.Fatalf("expected ErrTermReplayWindow for evicted seq 1, got %v", err)
		}
		if s.FailedInputSeq() != 0 {
			t.Fatal("output replay overflow must never cause Input fail-stop")
		}
	})

	t.Run("frozen_bidirectional_snapshot_barrier_and_registry", func(t *testing.T) {
		s := NewTerminalStream("stream-0000000000000005", "sess-1", "peer-1", "inst-a", base)
		f1 := mkTermFrame(1, "cmd1", base+60_000, true)
		f2 := mkTermFrame(2, "cmd2", base+60_000, true)
		_, _, _ = s.ReceiveInput(base, f1)
		// Query and barrier while seq 1 is in-flight must wait!
		if _, immediate := s.QueryStatus(base+1, "q1", base+10_000); immediate {
			t.Fatal("query must serialize behind inflight input")
		}
		if _, immediate := s.RequestBarrier(base+2, "b1", true); immediate {
			t.Fatal("barrier must serialize behind inflight input")
		}
		s.NoteClientOutputAck(7)
		_, _, _, queries, barrier := s.CompleteInput(base+5, 1, TermOutcomeApplied, 4, 4, 0)
		if len(queries) != 1 || barrier == nil || !barrier.Snapshot.Final || !s.Stopped {
			t.Fatalf("expected serialized query & final barrier, q=%d b=%v stopped=%v", len(queries), barrier, s.Stopped)
		}
		if barrier.Snapshot.State.ToClient.Processed != 7 || !VerifySnapshotToken(barrier.Snapshot.State, barrier.Snapshot.Token[:]) {
			t.Fatalf("unexpected frozen bidirectional snapshot: %+v", barrier.Snapshot)
		}
		// Paginate and verify wire round-trip + immutable token across pages.
		pages := PaginateInputStatusReply(s.StreamID, []byte("0123456789abcdef"), queries[0].Snapshot, 1)
		if len(pages) != 1 || !pages[0].Complete || !bytes.Equal(pages[0].SnapshotToken, queries[0].Snapshot.Token[:]) {
			t.Fatalf("unexpected pages: %+v", pages)
		}
		rawPage := EncodeInputStatusReply(pages[0])
		decPage, err := DecodeInputStatusReply(rawPage)
		if err != nil || !decPage.Available || decPage.State.ToClient.Processed != 7 {
			t.Fatalf("decode page failed: %v %+v", err, decPage)
		}
		// Late arrival on barrier-stopped stream is rejected.
		if act, _, err := s.ReceiveInput(base+10, f2); act != TermInputReject || !errors.Is(err, ErrTermStopped) {
			t.Fatalf("expected ErrTermStopped after barrier, got %d %v", act, err)
		}

		// Registry caps: 16 active, 16 retired, 30s retention.
		reg := NewTerminalRegistry(16, 16)
		for i := 0; i < 16; i++ {
			id := "stream-reg-00000" + string(rune('a'+i))
			st := NewTerminalStream(id, "sess-1", "peer-1", "inst-a", base)
			if err := reg.Add(st); err != nil {
				t.Fatalf("add %d: %v", i, err)
			}
		}
		if err := reg.Add(NewTerminalStream("stream-reg-over0", "sess-1", "peer-1", "inst-a", base)); !errors.Is(err, ErrTermRegistryFull) {
			t.Fatalf("expected active registry full, got %v", err)
		}
		for i := 0; i < 16; i++ {
			id := "stream-reg-00000" + string(rune('a'+i))
			st := reg.GetActive(id)
			st.RequestBarrier(base+100, "b", true)
			if err := reg.Retire(id, base+100); err != nil {
				t.Fatalf("retire %d: %v", i, err)
			}
		}
		reg.Prune(base + 100 + TermRetiredRetainMs - 1)
		if reg.RetiredCount() != 16 {
			t.Fatalf("must retain retired streams before 30s, got %d", reg.RetiredCount())
		}
		reg.Prune(base + 100 + TermRetiredRetainMs)
		if reg.RetiredCount() != 0 {
			t.Fatalf("expected retired streams pruned at 30s, got %d", reg.RetiredCount())
		}
	})

	t.Run("wire_roundtrip_all_terminal_controls", func(t *testing.T) {
		fs := TermFlowState{
			ToDaemon: TermWatermark{Processed: 3, InputSuccess: 2, FailedInputSeq: 3, Details: []TermFrameStatus{{
				Seq: 3, PayloadHash: sha256.Sum256([]byte("x")), Outcome: TermOutcomeInputPartialUnknown, DefinitelyInjected: 256, PossiblyInjected: 512, InputBytes: 600, FailureReason: 1,
			}}},
			ToClient: TermWatermark{Processed: 9},
		}
		const (
			streamID = "stream-0000000000000009"
			sessID   = "session-0000000000000001"
			senderID = "0123456789abcdef0123456789abcdef" // 32 bytes
			legID1   = "0123456789abcdef"                 // 16 bytes
			legID2   = "fedcba9876543210"                 // 16 bytes
		)
		att, err := DecodeAttachPath(EncodeAttachPath(AttachPathMsg{Stream: streamID, Session: sessID, Sender: senderID, LegID: legID1, PathGeneration: 2, State: fs}))
		if err != nil || att.PathGeneration != 2 || att.State.ToClient.Processed != 9 {
			t.Fatalf("attach roundtrip: %v %+v", err, att)
		}
		isReq, err := DecodeInputStatus(EncodeInputStatus(InputStatusMsg{Stream: streamID, QueryID: bytes.Repeat([]byte{1}, 16), FromSeq: 1, Limit: 32}))
		if err != nil || isReq.Limit != 32 {
			t.Fatalf("input status roundtrip: %v %+v", err, isReq)
		}
		ack, isStream, err := DecodeTermAck(EncodeTermAck(TermAckMsg{Stream: streamID, Direction: TermDirToDaemon, State: fs.ToDaemon}))
		if err != nil || !isStream || ack.State.FailedInputSeq != 3 {
			t.Fatalf("ack roundtrip: %v %v %+v", err, isStream, ack)
		}
		rep, err := DecodeReplay(EncodeReplay(ReplayMsg{Stream: streamID, Direction: TermDirToClient, FromSeq: 1, ThroughSeq: 5, MaxBytes: 65536, MaxFrames: 16}))
		if err != nil || rep.ThroughSeq != 5 {
			t.Fatalf("replay roundtrip: %v %+v", err, rep)
		}
		bar, err := DecodeBarrier(EncodeBarrier(BarrierMsg{Stream: streamID, BarrierID: bytes.Repeat([]byte{2}, 16), Requested: fs, StopAdmission: true, NewLegID: legID2, PathGeneration: 3}))
		if err != nil || !bar.StopAdmission || bar.PathGeneration != 3 {
			t.Fatalf("barrier roundtrip: %v %+v", err, bar)
		}
		barRep, err := DecodeBarrierReply(EncodeBarrierReply(BarrierReplyMsg{Stream: streamID, BarrierID: bytes.Repeat([]byte{2}, 16), State: fs, Final: true, Accepted: true}))
		if err != nil || !barRep.Final || !barRep.Accepted {
			t.Fatalf("barrier reply roundtrip: %v %+v", err, barRep)
		}
		sf, err := DecodeStreamFailed(EncodeStreamFailed(StreamFailedMsg{Stream: streamID, InputFailure: fs.ToDaemon.Details[0], State: fs}))
		if err != nil || sf.InputFailure.DefinitelyInjected != 256 {
			t.Fatalf("stream failed roundtrip: %v %+v", err, sf)
		}
		cp, err := DecodeClosePath(EncodeClosePath(ClosePathMsg{Stream: streamID, LegID: legID1, PathGeneration: 2, Reason: 1}))
		if err != nil || cp.PathGeneration != 2 {
			t.Fatalf("close path roundtrip: %v %+v", err, cp)
		}
	})

	t.Run("h2_ha_resize_cached_for_reconcile_without_30s_pause_and_evicted_on_processed", func(t *testing.T) {
		s := NewTerminalStream("stream-h2-000000000000001", "sess-1", "peer-1", "inst-a", base)
		resize := TermFrame{Seq: 1, PayloadHash: sha256.Sum256([]byte("resize")), RawBytes: 16, IsInput: false, RawEnvelope: []byte("resize")}
		if err := s.CacheInput(base, resize); err != nil {
			t.Fatalf("CacheInput resize: %v", err)
		}
		if s.InputCacheLen() != 1 {
			t.Fatalf("H-A: non-input frame must enter cache for full-disconnect reconciliation, got len=%d", s.InputCacheLen())
		}
		// Even 31s WITHOUT ACK, non-input frame must NEVER pause the stream (H2).
		s.Advance(base + 31_000)
		if s.Paused {
			t.Fatal("stream paused after 31s idle following unACKed Resize")
		}
		// Processed watermark evicts non-input frame cleanly.
		s.ApplyInputAck(TermWatermark{Processed: 1, InputSuccess: 0})
		if s.InputCacheLen() != 0 {
			t.Fatalf("expected Resize evicted on Processed=1, got len=%d", s.InputCacheLen())
		}
		// H-A: Resize (seq=2) + Input (seq=3) sent during disconnect -> ReconcileStatus resends BOTH in order!
		resize2 := TermFrame{Seq: 2, PayloadHash: sha256.Sum256([]byte("resize2")), RawBytes: 16, IsInput: false, RawEnvelope: []byte("resize2")}
		key3 := TermFrame{Seq: 3, PayloadHash: sha256.Sum256([]byte("ls\n")), RawBytes: 16, IsInput: true, InputBytes: 3, RawEnvelope: []byte("ls\n")}
		if err := s.CacheInput(base+31_000, resize2); err != nil {
			t.Fatalf("CacheInput resize2: %v", err)
		}
		if err := s.CacheInput(base+31_010, key3); err != nil {
			t.Fatalf("CacheInput key3: %v", err)
		}
		resend, needBar, needConfirm := s.ReconcileStatus(base+32_000, TermStatusSnapshot{
			Available: true,
			State:     TermFlowState{ToDaemon: TermWatermark{Processed: 1, InputSuccess: 0}},
		})
		if needBar || needConfirm || len(resend) != 2 || resend[0].Seq != 2 || resend[1].Seq != 3 {
			t.Fatalf("H-A: expected resend [seq=2, seq=3], got resend=%+v bar=%v confirm=%v", resend, needBar, needConfirm)
		}
	})

	t.Run("h3_force_retire_reclaims_active_across_24_streams_within_30s", func(t *testing.T) {
		reg := NewTerminalRegistry(16, 16)
		for i := 0; i < 24; i++ {
			id := fmt.Sprintf("stream-h3-%010d", i)
			st := NewTerminalStream(id, "sess-1", "peer-1", "inst-a", base+uint64(i)*100)
			if err := reg.Add(st); err != nil {
				t.Fatalf("add stream %d failed: %v", i, err)
			}
			reg.ForceRetire(id, base+uint64(i)*100+50)
			if reg.ActiveCount() != 0 {
				t.Fatalf("active count after ForceRetire %d = %d, want 0", i, reg.ActiveCount())
			}
			if reg.RetiredCount() > 16 {
				t.Fatalf("retired count exceeded cap 16: %d", reg.RetiredCount())
			}
		}
		if reg.RetiredCount() != 16 {
			t.Fatalf("expected retired capped at 16, got %d", reg.RetiredCount())
		}
	})

	t.Run("m8_stale_seq_after_prune_returns_duplicate_ack_and_sliding_details_exceeds_128", func(t *testing.T) {
		s := NewTerminalStream("stream-m8-000000000000001", "sess-1", "peer-1", "inst-a", base)
		// Send 200 sequential inputs without PruneConfirmedDetails -> must never hit ErrTermBudget.
		for seq := uint64(1); seq <= 200; seq++ {
			f := TermFrame{Seq: seq, PayloadHash: sha256.Sum256([]byte(fmt.Sprintf("in-%d", seq))), RawBytes: 16, IsInput: true, InputBytes: 4}
			act, ready, err := s.ReceiveInput(base+seq, f)
			if err != nil || act != TermInputRun || ready == nil {
				t.Fatalf("seq %d ReceiveInput failed: act=%d err=%v", seq, act, err)
			}
			s.CompleteInput(base+seq, seq, TermOutcomeApplied, 4, 4, 0)
		}
		if s.Processed() != 200 {
			t.Fatalf("expected processed=200, got %d", s.Processed())
		}
		// Late duplicate of already-pruned seq=2 (even with different hash once detail evicted) -> DuplicateACK, NOT Buffered!
		stale := TermFrame{Seq: 2, PayloadHash: sha256.Sum256([]byte("other")), RawBytes: 16, IsInput: true, InputBytes: 4}
		act, _, err := s.ReceiveInput(base+300, stale)
		if err != nil || act != TermInputDuplicateACK {
			t.Fatalf("expected TermInputDuplicateACK for stale pruned seq=2, got act=%d err=%v", act, err)
		}
		if len(s.inputPending) != 0 {
			t.Fatalf("stale seq leaked into inputPending: len=%d", len(s.inputPending))
		}
	})
}
