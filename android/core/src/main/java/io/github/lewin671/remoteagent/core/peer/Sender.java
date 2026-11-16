package io.github.lewin671.remoteagent.core.peer;

import java.util.*;

/** Bounded peer signaling retransmission queue and trickle candidate batcher. */
public final class Sender {
    public static final class Pending {
        public final byte[] raw;
        public final byte[] hash;
        public long first;
        public long notBefore;
        public long expiry;
        public long binding;
        public int attempts;

        Pending(byte[] raw, byte[] hash, long first, long expiry, long binding) {
            this.raw = raw;
            this.hash = hash;
            this.first = first;
            this.expiry = expiry;
            this.binding = binding;
        }
    }

    public long leaseUntil;
    public boolean membersAuthorized = true;
    public final long deadline;
    public final Map<String, Pending> pending = new LinkedHashMap<>();
    public boolean timedOut;
    public boolean paused;
    public int frames;
    public int bytes;
    public int failures;
    public long retryAt;
    public long networkGeneration;
    public long binding = 1;
    public long clock;

    public Sender(long deadline) {
        this.deadline = deadline;
        this.leaseUntil = deadline;
    }

    public synchronized boolean enqueue(byte[] raw, long now, long expiry, long binding) {
        clock = Math.max(clock, now);
        now = clock;
        if (!authorized() || timedOut || binding != this.binding || now >= expiry || now >= deadline || pending.size() >= 32) {
            return false;
        }
        byte[] hash = Wire.hash(raw);
        String key = Wire.hex(hash);
        if (pending.containsKey(key)) {
            return true;
        }
        pending.put(key, new Pending(raw.clone(), hash, now, Math.min(expiry, deadline), binding));
        return true;
    }

    public synchronized List<byte[]> tick(long now) {
        clock = Math.max(clock, now);
        now = clock;
        if (now >= deadline) {
            pending.clear();
            timedOut = true;
            return Collections.emptyList();
        }
        if (paused) {
            return Collections.emptyList();
        }
        List<byte[]> out = new ArrayList<>();
        long[] delays = {0L, 500L, 1500L};
        for (String key : new ArrayList<>(pending.keySet())) {
            Pending p = pending.get(key);
            if (p == null) continue;
            if (!authorized() || now >= p.expiry || p.binding != binding) {
                pending.remove(key);
                continue;
            }
            if (p.attempts == 3) continue;
            long due = Math.max(p.first + delays[p.attempts], p.notBefore);
            if (now >= due) {
                Wire.check(Arrays.equals(Wire.hash(p.raw), p.hash));
                out.add(p.raw.clone());
                p.attempts++;
                frames++;
                bytes += p.raw.length;
            }
        }
        return out;
    }

    public synchronized boolean admit(byte[] raw, long now) {
        clock = Math.max(clock, now);
        Pending p = pending.get(Wire.hex(Wire.hash(raw)));
        return authorized() && p != null && !timedOut && p.binding == binding && clock < p.expiry && clock < deadline;
    }

    public synchronized boolean authorized() {
        return membersAuthorized && clock < leaseUntil;
    }

    public synchronized void setAuthorization(long until, boolean members, long revision) {
        leaseUntil = until;
        membersAuthorized = members;
        if (revision != binding) {
            rebind(revision);
        }
    }

    public synchronized boolean ack(byte[] hash) {
        return pending.remove(Wire.hex(hash)) != null;
    }

    public synchronized void limited(byte[] hash, long now, long delay) {
        Pending p = pending.get(Wire.hex(hash));
        if (p != null) {
            long target = delay > deadline - now ? deadline : now + delay;
            p.notBefore = Math.max(p.notBefore, target);
        }
    }

    public synchronized long backoff(long now, double fraction) {
        long cap = failures < 5 ? 1000L << failures : 30_000L;
        failures++;
        retryAt = now + (long) (Math.min(30_000L, cap) * fraction);
        return retryAt;
    }

    public synchronized void rebind(long value) {
        binding = value;
        pending.clear();
    }

    public synchronized void offline() {
        paused = true;
    }

    public synchronized void discovered() {
        paused = false;
    }

    public synchronized void networkChanged(long g) {
        networkGeneration = Math.max(networkGeneration, g);
    }

    public synchronized void advanceClock(long now) {
        clock = Math.max(clock, now);
    }

    public static final class CandidateBatcher {
        public final long generation;
        public final long deadline;
        public final long binding;
        public long due;
        public int count;
        public int batches;
        public boolean ended;
        public final List<byte[]> pending = new ArrayList<>();

        public CandidateBatcher(long g, long deadline, long binding) {
            this.generation = g;
            this.deadline = deadline;
            this.binding = binding;
        }

        public boolean add(byte[] raw, long g, long now, long binding) {
            if (now >= deadline) {
                ended = true;
                pending.clear();
                due = 0;
            }
            if (binding != this.binding || ended || g != generation || count >= 64 || raw.length > 1024) {
                return false;
            }
            if (pending.isEmpty()) {
                due = now + 50;
            }
            pending.add(raw.clone());
            count++;
            return true;
        }

        public List<List<byte[]>> flush(long now, boolean end, long g, long binding) {
            if (now >= deadline || g != generation || binding != this.binding) {
                ended = true;
                pending.clear();
                due = 0;
                return Collections.emptyList();
            }
            if (end) {
                ended = true;
            }
            if (!end && now < due) {
                return Collections.emptyList();
            }
            List<List<byte[]>> out = new ArrayList<>();
            while (pending.size() >= 16 || (end && !pending.isEmpty())) {
                int n = Math.min(16, pending.size());
                out.add(new ArrayList<>(pending.subList(0, n)));
                pending.subList(0, n).clear();
                Wire.check(++batches <= 4);
            }
            due = pending.isEmpty() ? 0 : now + 50;
            return out;
        }
    }
}
