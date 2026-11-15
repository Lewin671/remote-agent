#!/usr/bin/env python3
"""Design counterexample, NOT an SDK, crypto, wire, or product test.

Finite virtual-time event queue; no processes, sockets, polling, or wall sleeps.
Two fake paths carry Signal and authenticated ACK observations independently.
Authentication is an explicit premise, not implemented here. Payloads are
synthetic identity markers, not SDP. Byte counts would not measure wire size.
"""

import hashlib
import heapq
import json
import os
from dataclasses import dataclass
from pathlib import Path


@dataclass(frozen=True)
class Frame:
    generation: int
    seq: int
    kind: str
    payload: bytes


class Experiment:
    RETRIES = (0, 500, 1500)
    GAP_MS = 2500
    WINDOW = 32
    DEADLINE_MS = 10000

    def __init__(self, dropped_offers, *, replace=False, drop_ack=False,
                 drop_seq=1, frames=2, ack_delay=0, limited=()):
        self.dropped_offers = dropped_offers
        self.replace = replace
        self.drop_ack = drop_ack
        self.drop_seq = drop_seq
        self.frames = frames
        self.ack_delay = ack_delay
        self.limited = limited
        self.waiting = []
        self.outstanding = set()
        self.started = {}
        self.window_peak = 0
        self.buffer_peak = 0
        self.gaps = {}
        self.events = []
        self.order = 0
        self.now = 0
        self.trace = []
        self.current = 1
        self.retired = set()
        self.expected = {}
        self.buffer = {}
        self.seen = {}
        self.acked = set()
        self.signal_sends = 0
        self.ack_sends = 0
        self.sdk_calls = []  # Observer only; no actual SDK is invoked.
        self.ack_dropped = False

    def event(self, at, action):
        self.order += 1
        heapq.heappush(self.events, (at, self.order, action))

    def record(self, action, **fields):
        self.trace.append(dict(ms=self.now, action=action, **fields))

    def start_generation(self, generation):
        self.current = generation
        self.expected[generation] = 1
        self.buffer[generation] = {}
        self.record("start_generation", generation=generation)
        # Boot challenges, roles, head, lease, and authentication are premises.
        for seq in range(1, self.frames + 1):
            kind = "Offer" if seq == 1 else "Candidate16"
            frame = Frame(generation, seq, kind, f"synthetic/{generation}/{seq}".encode())
            delay = 0 if seq == 1 else 50
            self.event(self.now + delay, lambda f=frame: self.enqueue(f))

    def enqueue(self, frame):
        self.waiting.append(frame)
        self.pump()

    def pump(self):
        while self.waiting and len(self.outstanding) < self.WINDOW:
            frame = self.waiting.pop(0)
            if frame.generation in self.retired:
                continue
            key = (frame.generation, frame.seq)
            self.outstanding.add(key)
            self.started[key] = self.now
            self.window_peak = max(self.window_peak, len(self.outstanding))
            self.send(frame, 0)

    def send(self, frame, attempt):
        key = (frame.generation, frame.seq)
        if key in self.acked:
            return
        if self.replace and frame.generation in self.retired:
            return
        self.signal_sends += 1
        digest = hashlib.sha256(frame.payload).hexdigest()
        self.record("signal_send", generation=frame.generation, seq=frame.seq,
                    kind=frame.kind, attempt=attempt, sha256=digest)
        if frame.seq == self.drop_seq and attempt < len(self.limited):
            retry_after = self.limited[attempt]
            self.record("limited", generation=frame.generation, seq=frame.seq,
                        retry_after_ms=retry_after)
            self.schedule_retry(frame, attempt, self.now + retry_after)
            return
        self.schedule_retry(frame, attempt, self.now)
        if frame.seq == self.drop_seq and attempt < self.dropped_offers:
            self.record("signal_drop", generation=frame.generation, seq=frame.seq)
            return
        self.receive(frame)

    def schedule_retry(self, frame, attempt, not_before):
        if attempt == len(self.RETRIES) - 1:
            return
        origin = self.started[(frame.generation, frame.seq)]
        when = max(origin + self.RETRIES[attempt + 1], not_before)
        self.event(when, lambda: self.send(frame, attempt + 1))

    def acknowledge(self, frame):
        self.ack_sends += 1
        self.record("ack_send", generation=frame.generation, seq=frame.seq)
        if self.drop_ack and not self.ack_dropped:
            self.ack_dropped = True
            self.record("ack_drop", generation=frame.generation, seq=frame.seq)
            return
        # Independent Control fake path: no ACK-of-ACK.
        def arrive():
            key = (frame.generation, frame.seq)
            self.acked.add(key)
            self.outstanding.discard(key)
            self.pump()
        self.event(self.now + self.ack_delay, arrive)

    def receive(self, frame):
        gen = frame.generation
        key = (gen, frame.seq)
        if gen in self.retired:
            self.record("stale_generation", generation=gen, seq=frame.seq)
            return
        digest = hashlib.sha256(frame.payload).digest()
        if key in self.seen:
            if self.seen[key] != digest:
                raise AssertionError("conflicting retransmission")
            self.record("duplicate", generation=gen, seq=frame.seq)
            self.acknowledge(frame)
            return
        pending = self.buffer[gen]
        if frame.seq > self.expected[gen]:
            if frame.seq in pending:
                assert pending[frame.seq][0] == frame
                return  # Duplicate cannot extend the fixed gap deadline.
            assert len(pending) < 32
            pending[frame.seq] = (frame, self.now)
            self.buffer_peak = max(self.buffer_peak, len(pending))
            self.update_gap(gen)
            self.record("buffer_gap", generation=gen, seq=frame.seq,
                        gap_deadline_ms=self.gaps[gen][1])
            return
        assert frame.seq == self.expected[gen]
        self.apply(frame)
        while self.expected[gen] in pending:
            self.apply(pending.pop(self.expected[gen])[0])
        self.update_gap(gen)

    def apply(self, frame):
        key = (frame.generation, frame.seq)
        self.seen[key] = hashlib.sha256(frame.payload).digest()
        self.expected[frame.generation] += 1
        self.sdk_calls.append((frame.generation, frame.kind))
        self.record("would_call_sdk", generation=frame.generation, kind=frame.kind)
        self.acknowledge(frame)

    def update_gap(self, gen):
        pending = self.buffer[gen]
        if not pending:
            self.gaps.pop(gen, None)
            return
        gap = (self.expected[gen], min(at for _, at in pending.values()) + self.GAP_MS)
        if self.gaps.get(gen) == gap:
            return
        self.gaps[gen] = gap
        self.event(max(self.now, gap[1]), lambda: self.gap_timeout(gen, gap))

    def gap_timeout(self, generation, gap):
        if self.gaps.get(generation) != gap or generation in self.retired:
            return
        self.retired.add(generation)
        self.buffer[generation].clear()
        self.record("gap_timeout_retire", generation=generation)
        if self.replace:
            self.outstanding = {key for key in self.outstanding if key[0] != generation}
            # Favorable zero-cost replacement, not a real Restart handshake.
            self.start_generation(generation + 1)

    def run(self):
        self.start_generation(1)
        # Finite simulation, not a wait/poll loop. Every event is < 10 seconds.
        while self.events:
            at, _, action = heapq.heappop(self.events)
            if at >= self.DEADLINE_MS:
                break
            self.now = at
            action()
        return dict(signal_sends=self.signal_sends, ack_sends=self.ack_sends,
                    generations=self.current, retired=sorted(self.retired),
                    sdk_observer_calls=self.sdk_calls, window_peak=self.window_peak,
                    buffer_peak=self.buffer_peak, trace=self.trace)


def main():
    isolated = Path(os.environ.get("REMOTE_AGENT_HOME", "")).as_posix()
    if not isolated.startswith("/tmp/"):
        raise SystemExit("REMOTE_AGENT_HOME must be explicitly under /tmp")
    scenarios = {}
    cases = (
        ("no_loss", Experiment(0), 2),
        ("one_offer_loss", Experiment(1), 2),
        ("ack_loss", Experiment(0, drop_ack=True), 2),
        ("two_offer_losses", Experiment(2), 2),
        ("candidate_batch_gap", Experiment(2, drop_seq=2, frames=3), 3),
        ("limited_retry_after", Experiment(0, limited=(800, 900)), 2),
        ("limited_beyond_total", Experiment(0, limited=(12000,)), 0),
        ("two_losses_each_generation", Experiment(2, replace=True), 2),
        ("all_offer_copies_lost", Experiment(3, replace=True), 0),
        # Synthetic window test, not a claim that 64 candidate batches are valid.
        ("window_backpressure", Experiment(0, frames=64, ack_delay=600), 64),
    )
    for name, experiment, count in cases:
        result = experiment.run()
        assert len(result["sdk_observer_calls"]) == count, name
        assert result["window_peak"] <= 32 and result["buffer_peak"] <= 32
        scenarios[name] = result
    two = scenarios["two_offer_losses"]
    assert two["retired"] == []
    assert any(t["action"] == "would_call_sdk" and t["ms"] == 1500 for t in two["trace"])
    assert scenarios["two_losses_each_generation"]["generations"] == 1
    assert scenarios["all_offer_copies_lost"]["generations"] == 4
    assert scenarios["all_offer_copies_lost"]["retired"] == [1, 2, 3]
    assert scenarios["window_backpressure"]["window_peak"] == 32
    limited = scenarios["limited_retry_after"]
    assert [t["ms"] for t in limited["trace"] if t["action"] == "signal_send" and t["seq"] == 1] == [0, 800, 1700]
    assert scenarios["limited_beyond_total"]["signal_sends"] == 4
    # Check immutability across retries, independent of the receiver cache.
    for result in scenarios.values():
        hashes = {}
        for t in result["trace"]:
            if t["action"] == "signal_send":
                key = (t["generation"], t["seq"])
                assert hashes.setdefault(key, t["sha256"]) == t["sha256"]
    print(json.dumps(dict(evidence="virtual_time_model_not_sdk",
                          parameters=dict(retries_ms=Experiment.RETRIES,
                                          gap_ms=Experiment.GAP_MS,
                                          sender_window=Experiment.WINDOW,
                                          deadline_ms=Experiment.DEADLINE_MS),
                          scenarios=scenarios), indent=2))


if __name__ == "__main__":
    main()
