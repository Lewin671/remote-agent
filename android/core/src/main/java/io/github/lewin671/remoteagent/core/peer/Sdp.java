package io.github.lewin671.remoteagent.core.peer;

import java.io.ByteArrayOutputStream;
import java.nio.ByteBuffer;
import java.nio.charset.CodingErrorAction;
import java.nio.charset.StandardCharsets;
import java.util.*;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/** Strict trickle-only SDP and candidate codec (protocol 16). */
public final class Sdp {
    public static final class Info {
        public final byte[] raw;
        public final byte[] ufrag;
        public final byte[] mid;
        public final byte[] fingerprint;

        Info(byte[] raw, byte[] ufrag, byte[] mid, byte[] fingerprint) {
            this.raw = raw;
            this.ufrag = ufrag;
            this.mid = mid;
            this.fingerprint = fingerprint;
        }
    }

    public static final class CandidateItem {
        public final byte[] original;
        public final byte[] ufrag;
        public final byte[] mid;
        public final long index;
        public final long generation;

        public CandidateItem(byte[] original, byte[] ufrag, byte[] mid, long index, long generation) {
            this.original = original.clone();
            this.ufrag = ufrag.clone();
            this.mid = mid.clone();
            this.index = index;
            this.generation = generation;
        }
    }

    public static final class CandidateBatch {
        public final List<CandidateItem> list;
        public final int index;

        CandidateBatch(List<CandidateItem> list, int index) {
            this.list = list;
            this.index = index;
        }
    }

    private static final Set<String> ALLOWED_ATTRS = Set.of(
        "group", "msid-semantic", "fingerprint", "ice-ufrag", "ice-pwd", "ice-options",
        "setup", "mid", "sctp-port", "max-message-size", "end-of-candidates",
        "extmap-allow-mixed", "sendrecv"
    );
    private static final Pattern MEDIA_LINE = Pattern.compile("^m=application [1-9][0-9]* UDP/DTLS/SCTP webrtc-datachannel$");
    private static final Pattern ATTR_LINE = Pattern.compile("^a=([^:]+)(?::(.*))?$");
    private static final Pattern HEADER_LINE = Pattern.compile("^[vostcm]=.*$");
    private static final Pattern FINGERPRINT_VALUE = Pattern.compile("^sha-256 (?:[0-9A-Fa-f]{2}:){31}[0-9A-Fa-f]{2}$");
    private static final Pattern UFRAG_VALUE = Pattern.compile("^[A-Za-z0-9+/]{4,256}$");
    private static final Pattern PWD_VALUE = Pattern.compile("^[A-Za-z0-9+/]{22,256}$");
    private static final Pattern DIGITS = Pattern.compile("^[0-9]+$");
    private static final Pattern BAD_LINE_SEPS = Pattern.compile("(?s).*[\\x0b\\x0c\\u0085\\u2028\\u2029].*");

    public static Info inspectSDP(byte[] bytes) {
        if (bytes.length > 60 * 1024 || (bytes.length >= 3 && (bytes[0] & 0xff) == 0xef && (bytes[1] & 0xff) == 0xbb && (bytes[2] & 0xff) == 0xbf)) {
            throw new IllegalArgumentException("SDP size/BOM");
        }
        String raw;
        try {
            raw = StandardCharsets.UTF_8.newDecoder()
                .onMalformedInput(CodingErrorAction.REPORT)
                .onUnmappableCharacter(CodingErrorAction.REPORT)
                .decode(ByteBuffer.wrap(bytes))
                .toString();
        } catch (Exception e) {
            throw new IllegalArgumentException("SDP utf8", e);
        }
        if (!raw.endsWith("\r\n") || raw.contains("\0") || BAD_LINE_SEPS.matcher(raw).matches()) {
            throw new IllegalArgumentException("SDP lines");
        }
        String stripped = raw.replace("\r\n", "");
        if (stripped.contains("\r") || stripped.contains("\n")) {
            throw new IllegalArgumentException("SDP bare newline");
        }
        List<Map<String, String>> scopes = List.of(new HashMap<>(), new HashMap<>());
        int section = 0;
        for (String line : raw.substring(0, raw.length() - 2).split("\r\n", -1)) {
            if (line.startsWith("m=")) {
                if (++section != 1 || !MEDIA_LINE.matcher(line).matches()) {
                    throw new IllegalArgumentException("SDP media");
                }
            }
            if (line.equals("a=ice-lite")) {
                throw new IllegalArgumentException("SDP ice-lite");
            }
            if (line.startsWith("a=")) {
                Matcher m = ATTR_LINE.matcher(line);
                if (!m.matches()) {
                    throw new IllegalArgumentException("SDP attribute syntax");
                }
                String name = m.group(1);
                String value = m.group(2) != null ? m.group(2) : "";
                if (!ALLOWED_ATTRS.contains(name) || scopes.get(section).containsKey(name)) {
                    throw new IllegalArgumentException("SDP attribute " + name);
                }
                scopes.get(section).put(name, value);
            } else if (!HEADER_LINE.matcher(line).matches()) {
                throw new IllegalArgumentException("SDP line");
            }
        }
        if (section != 1) {
            throw new IllegalArgumentException("SDP section");
        }
        String fp = null;
        for (Map<String, String> scope : scopes) {
            String v = scope.get("fingerprint");
            if (v != null) {
                if (!FINGERPRINT_VALUE.matcher(v).matches()) {
                    throw new IllegalArgumentException("SDP fingerprint");
                }
                if (fp != null && !fp.equalsIgnoreCase(v)) {
                    throw new IllegalArgumentException("SDP fingerprint conflict");
                }
                fp = v;
            }
        }
        if (fp == null) {
            throw new IllegalArgumentException("SDP fingerprint missing");
        }
        Map<String, String> values = new HashMap<>();
        for (String attr : List.of("ice-ufrag", "ice-pwd", "setup")) {
            String chosen = null;
            for (Map<String, String> scope : scopes) {
                String v = scope.get(attr);
                if (v != null) {
                    if (chosen != null && !chosen.equals(v)) {
                        throw new IllegalArgumentException("SDP ice inheritance");
                    }
                    chosen = v;
                }
            }
            if (chosen == null) {
                throw new IllegalArgumentException("SDP missing " + attr);
            }
            values.put(attr, chosen);
        }
        String ufrag = values.get("ice-ufrag");
        String pwd = values.get("ice-pwd");
        String setup = values.get("setup");
        if (!UFRAG_VALUE.matcher(ufrag).matches() || !PWD_VALUE.matcher(pwd).matches()) {
            throw new IllegalArgumentException("SDP credentials");
        }
        if (!setup.equals("active") && !setup.equals("passive") && !setup.equals("actpass")) {
            throw new IllegalArgumentException("SDP setup");
        }
        String sctpPort = scopes.get(1).getOrDefault("sctp-port", "");
        if (!DIGITS.matcher(sctpPort).matches() || Long.parseLong(sctpPort) > 65535L) {
            throw new IllegalArgumentException("SDP sctp-port");
        }
        byte[] fpBytes = Wire.unhex(fp.substring(8).replace(":", ""));
        byte[] midBytes = Wire.utf8(scopes.get(1).getOrDefault("mid", ""));
        return new Info(bytes.clone(), Wire.utf8(ufrag), midBytes, fpBytes);
    }

    public static byte[] sdpBody(byte[] raw) {
        return Wire.pb(Wire.b(1, raw), Wire.b(2, Wire.hash(raw)));
    }

    public static Info readSDPBody(byte[] raw) {
        Map<Integer, Wire.Field> f = Schema.fields(raw, "SDP");
        Wire.need(f, "bb");
        byte[] sdp = f.get(1).b;
        Wire.check(Arrays.equals(Wire.hash(sdp), f.get(2).b));
        return inspectSDP(sdp);
    }

    public static byte[] candidateBody(List<CandidateItem> list, int batchIndex) {
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        for (CandidateItem c : list) {
            byte[] encoded = Wire.pb(
                Wire.b(1, c.original),
                Wire.b(2, c.ufrag),
                Wire.b(3, c.mid),
                Wire.u(4, c.index),
                Wire.u(5, c.generation)
            );
            out.write(10);
            byte[] len = Wire.vint(encoded.length);
            out.write(len, 0, len.length);
            out.write(encoded, 0, encoded.length);
        }
        byte[] tail = Wire.pb(Wire.u(2, batchIndex));
        out.write(tail, 0, tail.length);
        return out.toByteArray();
    }

    public static CandidateBatch readCandidates(byte[] bytes) {
        bytes = Schema.normalize(bytes, "Candidates");
        Wire.Reader r = new Wire.Reader(bytes);
        List<CandidateItem> list = new ArrayList<>();
        while (r.at < bytes.length && bytes[r.at] == 10) {
            r.at++;
            long size = r.uint();
            Wire.check(size >= 0 && size <= 1024 && size <= bytes.length - r.at && list.size() < 16);
            Map<Integer, Wire.Field> f = Schema.fields(Arrays.copyOfRange(bytes, r.at, r.at + (int) size), "Candidate");
            r.at += (int) size;
            Wire.need(f, "bbbuu");
            byte[] origBytes = f.get(1).b;
            String original = Wire.text(origBytes);
            Wire.check(original.startsWith("candidate:") && !original.contains("\0") && !original.contains("\r") && !original.contains("\n"));
            for (byte b : origBytes) {
                int ub = b & 0xff;
                Wire.check(ub >= 32 && ub <= 126);
            }
            String expectedUfrag = Wire.text(f.get(2).b);
            String[] tokens = original.trim().split(" +");
            int ufrags = 0;
            for (int i = 0; i < tokens.length; i++) {
                if (tokens[i].equalsIgnoreCase("ufrag")) {
                    Wire.check(tokens[i].equals("ufrag") && ++ufrags == 1 && i + 1 < tokens.length && tokens[i + 1].equals(expectedUfrag));
                }
            }
            list.add(new CandidateItem(origBytes, f.get(2).b, f.get(3).b, f.get(4).u, f.get(5).u));
        }
        Map<Integer, Wire.Field> tail = Wire.parse(Arrays.copyOfRange(bytes, r.at, bytes.length), 1);
        Wire.check(tail.size() == 1 && tail.containsKey(2) && !tail.get(2).bytes && Long.compareUnsigned(tail.get(2).u, 3L) <= 0);
        return new CandidateBatch(list, (int) tail.get(2).u);
    }

    public static byte[] endBody(byte[] ufrag, int count, int batches) {
        return Wire.pb(Wire.b(1, ufrag), Wire.u(2, count), Wire.u(3, batches));
    }
}
