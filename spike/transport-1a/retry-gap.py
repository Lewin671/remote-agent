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
    GAP_MS = 1000
    DEADLINE_MS = 10000

    def __init__(self, dropped_offers, *, replace=False, drop_ack=False):
        self.dropped_offers = dropped_offers
        self.replace = replace
        self.drop_ack = drop_ack
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
        # Both boot challenges, roles, head, and lease are already valid.
        # A full batch of 16 candidates is available at the first 50 ms tick.
        # Sending it before the SDP ACK is not prohibited by the design.
        for seq, kind, delay in ((1, "Offer", 0), (2, "Candidate16", 50)):
            frame = Frame(generation, seq, kind, f"synthetic/{generation}/{seq}".encode())
            for attempt, offset in enumerate(self.RETRIES):
                self.event(self.now + delay + offset,
                           lambda f=frame, a=attempt: self.send(f, a))

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
        if frame.kind == "Offer" and attempt < self.dropped_offers:
            self.record("signal_drop", generation=frame.generation, seq=frame.seq)
            return
        # Signal fake path: zero propagation delay after injected loss.
        self.receive(frame)

    def acknowledge(self, frame):
        self.ack_sends += 1
        self.record("ack_send", generation=frame.generation, seq=frame.seq)
        if self.drop_ack and not self.ack_dropped:
            self.ack_dropped = True
            self.record("ack_drop", generation=frame.generation, seq=frame.seq)
            return
        # Independent Control fake path: no ACK-of-ACK.
        self.acked.add((frame.generation, frame.seq))

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
                assert pending[frame.seq] == frame
                return  # Duplicate cannot extend the fixed gap deadline.
            if not pending:
                self.event(self.now + self.GAP_MS, lambda g=gen: self.gap_timeout(g))
            assert len(pending) < 32
            pending[frame.seq] = frame
            self.record("buffer_gap", generation=gen, seq=frame.seq,
                        gap_deadline_ms=self.now + self.GAP_MS)
            return
        assert frame.seq == self.expected[gen]
        self.apply(frame)
        while self.expected[gen] in pending:
            self.apply(pending.pop(self.expected[gen]))

    def apply(self, frame):
        key = (frame.generation, frame.seq)
        self.seen[key] = hashlib.sha256(frame.payload).digest()
        self.expected[frame.generation] += 1
        self.sdk_calls.append((frame.generation, frame.kind))
        self.record("would_call_sdk", generation=frame.generation, kind=frame.kind)
        self.acknowledge(frame)

    def gap_timeout(self, generation):
        if not self.buffer[generation] or generation in self.retired:
            return
        self.retired.add(generation)
        self.buffer[generation].clear()
        self.record("gap_timeout_retire", generation=generation)
        if self.replace:
            # Most favorable possible replacement: zero Restart/ACK overhead,
            # no backoff, no relay/boot/ICE cost. Not a wire implementation.
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
                    sdk_observer_calls=self.sdk_calls, trace=self.trace)


def main():
    isolated = Path(os.environ.get("REMOTE_AGENT_HOME", "")).as_posix()
    if not isolated.startswith("/tmp/"):
        raise SystemExit("REMOTE_AGENT_HOME must be explicitly under /tmp")
    scenarios = {}
    for name, experiment in (
        ("no_loss", Experiment(0)),
        ("one_offer_loss", Experiment(1)),
        ("ack_loss", Experiment(0, drop_ack=True)),
        ("two_offer_losses", Experiment(2)),
        ("two_losses_each_generation", Experiment(2, replace=True)),
    ):
        scenarios[name] = experiment.run()

    assert len(scenarios["no_loss"]["sdk_observer_calls"]) == 2
    assert len(scenarios["one_offer_loss"]["sdk_observer_calls"]) == 2
    assert len(scenarios["ack_loss"]["sdk_observer_calls"]) == 2
    two = scenarios["two_offer_losses"]
    assert two["sdk_observer_calls"] == []
    assert any(t["action"] == "gap_timeout_retire" and t["ms"] == 1050 for t in two["trace"])
    assert any(t["action"] == "stale_generation" and t["ms"] == 1500 for t in two["trace"])
    churn = scenarios["two_losses_each_generation"]
    assert churn["sdk_observer_calls"] == []
    assert churn["generations"] == 10 and len(churn["retired"]) == 9
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
                                          deadline_ms=Experiment.DEADLINE_MS),
                          scenarios=scenarios), indent=2))


if __name__ == "__main__":
    main()
