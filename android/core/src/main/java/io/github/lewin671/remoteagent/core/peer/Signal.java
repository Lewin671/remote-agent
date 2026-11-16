package io.github.lewin671.remoteagent.core.peer;

import java.util.*;
import java.util.function.BooleanSupplier;

/** Authenticated protocol 16 signaling state machine for one PC use. */
public final class Signal {
    public static final class Action {
        public final long cls;
        public final long type;
        public final byte[] payload;
        public final long generation;
        public final byte[] original;

        Action(long cls, long type, byte[] payload, long generation, byte[] original) {
            this.cls = cls;
            this.type = type;
            this.payload = payload;
            this.generation = generation;
            this.original = original;
        }
    }

    public final Wire codec;
    public final Wire.Identity self;
    public Wire.Identity peer;
    public final byte[] connection;
    public final long use;
    public BooleanSupplier current;

    public long leaseUntil;
    public boolean membersAuthorized = true;
    public long authorizationRevision = 1;
    public long version = 0;
    public long generation = 1;
    public long next = 1;
    public long gapDeadline = 0;
    public boolean bootReady;
    public boolean localSDP;
    public boolean remoteSDP;
    public boolean dtls;
    public boolean confirmed;
    public boolean closed;
    public boolean retired;
    public boolean ended;
    public Wire.Opened challenge;
    public Wire.Opened probe;
    public Wire.Opened receivedProbe;
    public long clock;
    public long roundDeadline;
    public int dispatches;
    public final Map<String, byte[]> allowed = new HashMap<>();
    public final Map<String, byte[]> dispatched = new HashMap<>();
    public byte[] localHash = {};
    public byte[] remoteHash = {};
    public byte[] localFingerprint = {};
    public byte[] remoteFingerprint = {};
    public byte[] remoteUfrag = {};
    public byte[] remoteMid = {};
    public int candidates;
    public int batches;
    public int sdkActions;
    public int duplicates;
    public int conflicts;
    public int rejected;
    public int unsupported;
    public int staleChallenges;
    public final Map<String, byte[]> seen = new HashMap<>();
    public final Map<String, Long> cacheUntil = new HashMap<>();
    public final Map<Long, Wire.Opened> pending = new HashMap<>();
    public final Map<Long, Long> pendingAt = new HashMap<>();

    public Signal(Wire codec, Wire.Identity self, Wire.Identity peer, byte[] connection, long use, long now) {
        this.codec = codec;
        this.self = self;
        this.peer = Wire.member(peer);
        this.connection = connection.clone();
        this.use = use;
        this.clock = now;
        this.roundDeadline = now + 10_000L;
        this.leaseUntil = this.roundDeadline;
    }

    private Wire.Opened freeze(Wire.Opened o) {
        Wire.Opened copy = new Wire.Opened();
        copy.ctx = codec.context(o.ctx.encode(codec));
        copy.type = o.type;
        copy.hash = o.hash.clone();
        copy.raw = o.raw.clone();
        copy.payload = o.payload.clone();
        copy.frameId = o.frameId != null ? o.frameId.clone() : null;
        return copy;
    }

    public synchronized void rememberProbe(Wire.Opened sent) throws Exception {
        long ver = version;
        Wire.check(!closed && authorized());
        sent = codec.openSent(sent.raw, self, peer, clock);
        preflight(sent);
        Wire.Context c = sent.ctx;
        Wire.check(sent.type == 1 && c.cls == 1
            && Arrays.equals(c.domain, Wire.utf8("bootstrap"))
            && c.generation == 0 && c.use == 0
            && clock < c.deadline
            && Arrays.equals(c.from, self.id)
            && Arrays.equals(c.to, peer.id)
            && Arrays.equals(c.connection, connection)
            && Arrays.equals(c.senderBoot, self.boot)
            && Arrays.equals(c.senderInstance, self.instance));
        Wire.check(ver == version && authorized() && !closed);
        probe = freeze(sent);
    }

    public synchronized void rememberChallenge(Wire.Opened sent) throws Exception {
        long ver = version;
        Wire.check(!closed && authorized());
        sent = codec.openSent(sent.raw, self, peer, clock);
        preflight(sent);
        Wire.Context c = sent.ctx;
        Map<Integer, Wire.Field> f = Schema.fields(sent.payload, "Challenge");
        Wire.need(f, "bbbbub");
        Wire.check(receivedProbe != null && clock < receivedProbe.ctx.deadline
            && Arrays.equals(f.get(6).b, receivedProbe.hash)
            && sent.type == 2 && c.recipientUnknown == 0 && c.cls == 1
            && Arrays.equals(c.domain, Wire.utf8("bootstrap"))
            && c.generation == 0 && c.use == 0
            && clock < c.deadline && sent.hash.length == 32
            && Arrays.equals(c.from, self.id) && Arrays.equals(c.to, peer.id)
            && Arrays.equals(c.connection, connection)
            && Arrays.equals(c.senderBoot, self.boot) && Arrays.equals(c.receiverBoot, peer.boot)
            && Arrays.equals(c.senderInstance, self.instance) && Arrays.equals(c.receiverInstance, peer.instance));
        Wire.check(ver == version && authorized() && !closed);
        challenge = freeze(sent);
    }

    public synchronized void acceptACK(Wire.Opened ack) {
        byte[] raw = ack.payload;
        Map<Integer, Wire.Field> f = Schema.fields(raw, "Ack");
        Wire.check(f.size() == 7 && f.get(1).b.length == 0 && f.get(2).u == 0 && !f.containsKey(3));
        for (int n : new int[]{4, 5, 7, 8}) {
            Wire.check(f.containsKey(n) && !f.get(n).bytes);
        }
        Wire.check(f.containsKey(6) && f.get(6).bytes && f.get(6).b.length == 32
            && f.get(4).u >= 1 && f.get(4).u <= 2 && f.get(5).u != 0
            && Long.compareUnsigned(f.get(8).u, 2) <= 0);
        Wire.Opened ch = challenge;
        if (ch == null) {
            Wire.check(f.get(7).u != 0 || (f.get(4).u == 1 && f.get(8).u == 0));
            return;
        }
        if (f.get(7).u == 0 || Arrays.equals(f.get(6).b, ch.hash)) {
            Wire.check(clock < ch.ctx.deadline);
            Wire.Context c = ch.ctx, a = ack.ctx;
            Wire.check(Arrays.equals(a.from, c.to) && Arrays.equals(a.to, c.from)
                && Arrays.equals(a.connection, c.connection)
                && Arrays.equals(a.senderBoot, c.receiverBoot)
                && Arrays.equals(a.receiverBoot, c.senderBoot)
                && Arrays.equals(a.senderInstance, c.receiverInstance)
                && Arrays.equals(a.receiverInstance, c.senderInstance));
            Wire.check(f.get(4).u == c.cls && f.get(5).u == c.seq
                && Arrays.equals(f.get(6).b, ch.hash)
                && f.get(7).u == c.generation && f.get(8).u == c.use);
            bootReady = true;
        } else {
            Wire.check(bootReady);
        }
    }

    private void preflight(Wire.Opened o) {
        o.payload = Schema.known(o.ctx.cls, o.type, o.payload);
        if (o.ctx.cls != 1) return;
        byte[] raw = o.payload;
        if (o.type == 1 || o.type == 2) {
            Map<Integer, Wire.Field> f = Schema.fields(raw, o.type == 1 ? "Probe" : "Challenge");
            Wire.Context c = o.ctx;
            Wire.check(Arrays.equals(f.get(1).b, c.connection)
                && Arrays.equals(f.get(2).b, c.senderInstance)
                && Arrays.equals(f.get(3).b, c.senderBoot)
                && (o.type != 2 || Arrays.equals(f.get(4).b, c.receiverBoot)));
        }
        if (o.type == 3 || o.type == 4) {
            Sdp.readSDPBody(raw);
        }
        if (o.type == 5) {
            Sdp.CandidateBatch batch = Sdp.readCandidates(raw);
            Wire.check(!batch.list.isEmpty());
            for (Sdp.CandidateItem item : batch.list) {
                Wire.check(item.generation == o.ctx.generation
                    && item.ufrag.length >= 4 && item.ufrag.length <= 256
                    && item.mid.length > 0 && item.index == 0);
            }
        }
        if (o.type == 6) {
            Map<Integer, Wire.Field> f = Schema.fields(raw, "EndCandidates");
            Wire.check(f.get(1).b.length >= 4 && Long.compareUnsigned(f.get(2).u, 64) <= 0 && Long.compareUnsigned(f.get(3).u, 4) <= 0);
        }
        if (o.type == 7) {
            for (Wire.Field field : Schema.fields(raw, "Confirm").values()) {
                Wire.check(field.b.length == 32);
            }
        }
        if (o.type == 8) {
            Map<Long, Long> targets = Schema.restart(raw);
            Wire.check(offerer() && o.ctx.use == use && o.ctx.generation == generation
                && targets.size() == 1 && Objects.equals(targets.get(use), generation));
        }
    }

    public synchronized void setAuthorization(long until, boolean members, long revision) {
        leaseUntil = until;
        membersAuthorized = members;
        authorizationRevision = revision;
        version++;
    }

    public synchronized boolean authorized() {
        return clock < leaseUntil && membersAuthorized && authorizationRevision == 1 && (current == null || current.getAsBoolean());
    }

    public synchronized boolean replyAllowed(byte[] raw, long now) {
        tick(now);
        long rev = authorizationRevision;
        if (!authorized()) return false;
        try {
            Wire.Opened o = codec.open(raw.clone(), peer, self, clock);
            Schema.known(o.ctx.cls, o.type, o.payload);
            Wire.Context c = o.ctx;
            return rev == authorizationRevision && authorized() && clock < c.deadline
                && Arrays.equals(c.connection, connection)
                && Arrays.equals(seen.get(Wire.cacheKey(c)), o.hash)
                && Arrays.equals(c.senderInstance, peer.instance)
                && Arrays.equals(c.senderBoot, peer.boot)
                && !(c.cls == 2 && (o.type == 4 || o.type == 10 || o.type == 11 || o.type == 12 || o.type == 13));
        } catch (Exception e) {
            return false;
        }
    }

    public synchronized boolean offerer() {
        return Wire.hex(self.id).compareTo(Wire.hex(peer.id)) < 0;
    }

    public synchronized boolean ready() {
        return !closed && localSDP && remoteSDP && dtls && confirmed;
    }

    public synchronized void setLocal(byte[] raw, boolean offer, long gen) {
        long ver = version;
        Wire.check(authorized() && clock < roundDeadline && gen == generation && !closed
            && bootReady && offer == offerer() && (!offer || !localSDP) && (offer || remoteSDP));
        Sdp.Info info = Sdp.inspectSDP(raw);
        byte[] digest = Wire.hash(raw);
        Wire.check(ver == version && authorized() && !closed && clock < roundDeadline && gen == generation);
        localSDP = true;
        localHash = digest;
        localFingerprint = info.fingerprint;
    }

    public synchronized void nextGeneration(long gen) {
        Wire.check(clock < roundDeadline && (!closed || retired) && gen == generation + 1 && gen != 0);
        version++;
        generation = gen;
        next = 1;
        closed = retired = false;
        localSDP = remoteSDP = dtls = confirmed = ended = false;
        pending.clear();
        pendingAt.clear();
        allowed.clear();
        dispatched.clear();
        gapDeadline = 0;
        candidates = batches = 0;
    }

    private void remember(Wire.Opened o) {
        String k = Wire.cacheKey(o.ctx);
        seen.put(k, o.hash);
        cacheUntil.put(k, o.ctx.deadline + 300_000L);
    }

    public synchronized List<Action> receive(byte[] raw, long now) throws Exception {
        try {
            tick(now);
            return accept(codec.open(raw.clone(), peer, self, clock), clock);
        } catch (Exception e) {
            rejected++;
            throw e;
        }
    }

    private List<Action> accept(Wire.Opened o, long now) throws Exception {
        tick(now);
        long ver = version;
        o = codec.open(o.raw, peer, self, clock);
        Wire.check(authorized());
        Wire.Context c = o.ctx;
        Wire.check(Arrays.equals(c.connection, connection) && Arrays.equals(c.from, peer.id));
        o.payload = Schema.known(c.cls, o.type, o.payload);
        Wire.check(ver == version && authorized() && clock < c.deadline);
        String k = Wire.cacheKey(c);
        if (seen.containsKey(k)) {
            if (!Arrays.equals(seen.get(k), o.hash)) {
                conflicts++;
                throw new IllegalArgumentException("conflict");
            }
            duplicates++;
            return Collections.emptyList();
        }
        preflight(o);
        Wire.check(ver == version && authorized() && clock < c.deadline);
        Wire.check(clock < roundDeadline && (!closed || retired) && seen.size() < 2048);

        if (c.cls == 2) {
            if (o.type == 4) acceptACK(o);
            remember(o);
            if (o.type != 4 && o.type != 10) {
                unsupported++;
                return List.of(new Action(2, 10, Wire.pb(Wire.u(1, c.cls), Wire.u(2, o.type), Wire.u(3, c.seq), Wire.b(4, o.hash)), c.generation, o.raw));
            }
            return List.of(new Action(2, o.type, o.payload, c.generation, o.raw));
        }

        if (o.type == 1 || o.type == 2) {
            Wire.check(Arrays.equals(c.domain, Wire.utf8("bootstrap")) && c.generation == 0 && c.use == 0);
            Map<Integer, Wire.Field> f = Schema.fields(o.payload, o.type == 1 ? "Probe" : "Challenge");
            Wire.need(f, o.type == 1 ? "bbb" : "bbbbub");
            Wire.check(Arrays.equals(f.get(1).b, c.connection)
                && Arrays.equals(f.get(2).b, c.senderInstance)
                && Arrays.equals(f.get(3).b, c.senderBoot));
            if (o.type == 2 && probe != null) {
                codec.openSent(probe.raw, self, peer, clock);
            }
            if (o.type == 2) {
                Wire.check(Long.compareUnsigned(f.get(5).u, 1) <= 0
                    && f.get(6).b.length == 32
                    && probe != null && clock < probe.ctx.deadline
                    && Arrays.equals(f.get(6).b, probe.hash)
                    && Arrays.equals(f.get(4).b, self.boot));
            }
            Wire.check(ver == version && authorized() && !closed && clock < c.deadline);
            if (peer.boot.length > 0 && (!Arrays.equals(peer.boot, c.senderBoot) || !Arrays.equals(peer.instance, c.senderInstance))) {
                retire(false);
                throw new IllegalArgumentException("new peer needs new connection");
            }
            if (o.type == 1) receivedProbe = freeze(o);
            Wire.Identity nextPeer = new Wire.Identity(peer);
            nextPeer.instance = c.senderInstance.clone();
            nextPeer.boot = c.senderBoot.clone();
            peer = nextPeer;
            remember(o);
            if (o.type == 2) {
                bootReady = true;
                if (f.get(5).u == 1) staleChallenges++;
            }
            return List.of(new Action(1, o.type, o.payload, 0, o.raw));
        }

        if (!bootReady && !closed && !offerer() && o.type == 3 && c.cls == 1
            && Arrays.equals(c.domain, Wire.utf8("signal"))
            && c.use == use && c.generation == generation && c.seq == 1
            && Arrays.equals(c.receiverBoot, self.boot) && Arrays.equals(c.senderBoot, peer.boot)
            && Arrays.equals(c.receiverInstance, self.instance) && Arrays.equals(c.senderInstance, peer.instance)) {
            Sdp.readSDPBody(o.payload);
            Wire.check(ver == version && authorized() && clock < c.deadline);
            bootReady = true;
        }

        Wire.check((!closed || (retired && c.generation == generation + 1))
            && bootReady && c.use == use
            && Long.compareUnsigned(c.generation, generation) >= 0
            && Long.compareUnsigned(c.generation, generation + 1) <= 0);
        if (c.generation == generation + 1) {
            Wire.check(!offerer());
            nextGeneration(c.generation);
        }
        Wire.check(Long.compareUnsigned(c.seq, next) >= 0);
        if (Long.compareUnsigned(c.seq, next) > 0) {
            Wire.check(pending.size() < 32);
            remember(o);
            pending.put(c.seq, o);
            pendingAt.put(c.seq, now);
            refreshGap();
            return Collections.emptyList();
        }
        List<Action> actions = new ArrayList<>();
        for (;;) {
            long deliveryVer = version;
            o = codec.open(o.raw, peer, self, clock);
            if (closed || clock >= o.ctx.deadline || o.ctx.generation != generation || o.ctx.use != use || !Arrays.equals(o.ctx.connection, connection)) {
                retire(true);
                throw new IllegalArgumentException("stale buffered signal");
            }
            preflight(o);
            Wire.check(deliveryVer == version && authorized() && !closed && clock < o.ctx.deadline && o.ctx.generation == generation);
            Action action = apply(o);
            remember(o);
            next++;
            if (action.type != 0) {
                actions.add(action);
                if (action.cls == 1 && action.type >= 3 && action.type <= 6) {
                    allowed.put(Wire.cacheKey(o.ctx), o.hash.clone());
                }
            }
            if (!pending.containsKey(next)) break;
            o = pending.remove(next);
            pendingAt.remove(next);
        }
        refreshGap();
        return actions;
    }

    private Action apply(Wire.Opened o) {
        long ver = version;
        Action action = new Action(1, o.type, o.payload, o.ctx.generation, o.raw);
        switch ((int) o.type) {
            case 3:
            case 4: {
                Wire.check(o.type == 3 ? (!offerer() && !remoteSDP) : (offerer() && localSDP && !remoteSDP));
                Sdp.Info info = Sdp.readSDPBody(o.payload);
                byte[] digest = Wire.hash(info.raw);
                Wire.check(ver == version && authorized() && !closed && clock < o.ctx.deadline);
                remoteSDP = true;
                remoteHash = digest;
                remoteFingerprint = info.fingerprint;
                remoteUfrag = info.ufrag;
                remoteMid = info.mid;
                sdkActions++;
                return new Action(1, o.type, info.raw, o.ctx.generation, o.raw);
            }
            case 5: {
                Wire.check(remoteSDP && !ended);
                Sdp.CandidateBatch batch = Sdp.readCandidates(o.payload);
                Wire.check(batch.index == batches && !batch.list.isEmpty() && batches < 4 && candidates + batch.list.size() <= 64);
                for (Sdp.CandidateItem item : batch.list) {
                    Wire.check(item.generation == generation
                        && Arrays.equals(item.ufrag, remoteUfrag)
                        && Arrays.equals(item.mid, remoteMid)
                        && item.index == 0);
                }
                candidates += batch.list.size();
                batches++;
                sdkActions += batch.list.size();
                return action;
            }
            case 6: {
                Wire.check(remoteSDP && !ended);
                Map<Integer, Wire.Field> f = Schema.fields(o.payload, "EndCandidates");
                Wire.need(f, "buu");
                Wire.check(Arrays.equals(f.get(1).b, remoteUfrag) && f.get(2).u == candidates && f.get(3).u == batches);
                sdkActions++;
                ended = true;
                return action;
            }
            case 7:
                Wire.check(localSDP && remoteSDP && Arrays.equals(o.payload, confirmBody()));
                confirmed = true;
                return action;
            case 8:
                Wire.check(offerer());
                return action;
            case 9:
                closed = true;
                pending.clear();
                pendingAt.clear();
                allowed.clear();
                dispatched.clear();
                gapDeadline = 0;
                return action;
            default:
                unsupported++;
                return new Action(2, 10, Wire.pb(Wire.u(1, o.ctx.cls), Wire.u(2, o.type), Wire.u(3, o.ctx.seq), Wire.b(4, o.hash)), o.ctx.generation, o.raw);
        }
    }

    public synchronized byte[] confirmBody() {
        byte[] oh = offerer() ? localHash : remoteHash;
        byte[] ah = offerer() ? remoteHash : localHash;
        byte[] of = offerer() ? localFingerprint : remoteFingerprint;
        byte[] af = offerer() ? remoteFingerprint : localFingerprint;
        return Wire.pb(Wire.b(1, oh), Wire.b(2, ah), Wire.b(3, of), Wire.b(4, af));
    }

    public synchronized void retire(boolean retry) {
        version++;
        closed = true;
        retired = retry;
        pending.clear();
        pendingAt.clear();
        allowed.clear();
        dispatched.clear();
        gapDeadline = 0;
    }

    public synchronized void forgetPeer() {
        peer = Wire.member(peer);
        retire(false);
    }

    public synchronized boolean dispatch(byte[] raw, long now) {
        tick(now);
        long ver = version;
        if (closed) return false;
        try {
            Wire.Opened o = codec.open(raw.clone(), peer, self, clock);
            Wire.Context c = o.ctx;
            String k = Wire.cacheKey(c);
            if (!authorized() || ver != version || closed || clock >= c.deadline
                || c.generation != generation || c.use != use
                || !Arrays.equals(c.connection, connection)
                || !Arrays.equals(allowed.get(k), o.hash)
                || dispatched.containsKey(k)) {
                return false;
            }
            dispatched.put(k, o.hash.clone());
            dispatches++;
            return true;
        } catch (Exception e) {
            return false;
        }
    }

    private void refreshGap() {
        gapDeadline = 0;
        for (Map.Entry<Long, Long> entry : pendingAt.entrySet()) {
            long due = Math.min(entry.getValue() + 2500L, Math.min(pending.get(entry.getKey()).ctx.deadline, roundDeadline));
            if (gapDeadline == 0 || Long.compareUnsigned(due, gapDeadline) < 0) {
                gapDeadline = due;
            }
        }
    }

    public synchronized boolean tick(long now) {
        clock = Math.max(clock, now);
        now = clock;
        if (now >= roundDeadline) {
            retire(false);
            return true;
        }
        for (String k : new ArrayList<>(cacheUntil.keySet())) {
            if (now > cacheUntil.get(k)) {
                cacheUntil.remove(k);
                seen.remove(k);
            }
        }
        if (gapDeadline != 0 && now >= gapDeadline) {
            closed = retired = true;
            pending.clear();
            pendingAt.clear();
            allowed.clear();
            dispatched.clear();
            gapDeadline = 0;
            return true;
        }
        return false;
    }
}
