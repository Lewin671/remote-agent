package usagestats

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/encoding/protowire"
)

type agyProtoUsage struct {
	modelEnum       uint64
	inputTokens     int64
	outputTokens    int64
	cacheReadTokens int64
	botID           string
	thinkingTokens  int64
	responseTokens  int64
	respID          string
}

type agyGenMeta struct {
	modelEnum uint64
	model     string
	cascadeID string
	ttftMs    int64
	hasTTFT   bool
	genMs     int64
	hasGenDur bool
	usage     agyProtoUsage
}

type agyStepMeta struct {
	createdAtMs    int64
	firstTokenAtMs int64
	completedAtMs  int64
	usage          agyProtoUsage
}

func scanAntigravityDB(path string) ([]requestSample, int64, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, 0, err
	}
	u := &url.URL{
		Scheme:   "file",
		Path:     filepath.ToSlash(path),
		RawQuery: "mode=ro&_pragma=busy_timeout(2000)&_pragma=query_only(ON)",
	}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, 0, err
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	convID := strings.TrimSuffix(filepath.Base(path), ".db")
	if convID == "" || convID == "." || convID == "/" {
		convID = path
	}

	byBot := map[string]agyGenMeta{}
	byResp := map[string]agyGenMeta{}
	byEnum := map[uint64]string{}

	rows, err := db.Query(`SELECT data FROM gen_metadata`)
	if err != nil {
		if isMissingTableErr(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	for rows.Next() {
		var data []byte
		if scanErr := rows.Scan(&data); scanErr != nil {
			_ = rows.Close()
			return nil, 0, scanErr
		}
		gm, ok := parseAgyGenMetadata(data)
		if !ok {
			continue
		}
		if gm.usage.botID != "" {
			byBot[gm.usage.botID] = gm
		}
		if gm.usage.respID != "" {
			byResp[gm.usage.respID] = gm
		}
		if gm.model != "" {
			if gm.modelEnum != 0 {
				byEnum[gm.modelEnum] = gm.model
			}
			if gm.usage.modelEnum != 0 {
				byEnum[gm.usage.modelEnum] = gm.model
			}
		}
	}
	if rowErr := rows.Err(); rowErr != nil {
		_ = rows.Close()
		return nil, 0, rowErr
	}
	_ = rows.Close()

	stepRows, err := db.Query(`SELECT idx, step_type, metadata FROM steps WHERE step_type IN (15, 23) ORDER BY idx`)
	if err != nil {
		if isMissingTableErr(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	defer stepRows.Close()

	var out []requestSample
	for stepRows.Next() {
		var stepIdx int64
		var stepType int64
		var meta []byte
		if scanErr := stepRows.Scan(&stepIdx, &stepType, &meta); scanErr != nil {
			return nil, 0, scanErr
		}
		if len(meta) == 0 {
			continue
		}
		sm := parseAgyStepMetadata(meta)
		if sample, ok := buildAntigravityDBSample(convID, path, stepIdx, stepType, sm, byBot, byResp, byEnum); ok {
			out = append(out, sample)
		}
	}
	if rowErr := stepRows.Err(); rowErr != nil {
		return nil, 0, rowErr
	}
	return out, 0, nil
}

func isAgySpeedPlausible(speedTokPerSec float64, genMs, ttftMs int64) bool {
	if speedTokPerSec <= 0 || speedTokPerSec > 1000 {
		return false
	}
	// Reject buffered tool-call bursts where generation time was absorbed into TTFT,
	// leaving a tiny trailing genMs that inflates tok/s and TTFT simultaneously.
	if ttftMs > 4*genMs && genMs < 1000 && speedTokPerSec > 500 {
		return false
	}
	return true
}

func buildAntigravityDBSample(
	convID, sourcePath string,
	stepIdx, stepType int64,
	sm agyStepMeta,
	byBot, byResp map[string]agyGenMeta,
	byEnum map[uint64]string,
) (requestSample, bool) {
	var gm agyGenMeta
	var hasGM bool
	if sm.usage.botID != "" {
		gm, hasGM = byBot[sm.usage.botID]
	}
	if !hasGM && sm.usage.respID != "" {
		gm, hasGM = byResp[sm.usage.respID]
	}

	rawModel := ""
	if hasGM && gm.model != "" {
		rawModel = gm.model
	} else if sm.usage.modelEnum != 0 {
		rawModel = byEnum[sm.usage.modelEnum]
	}
	model := normalizeAntigravityModel(rawModel)
	if model == "" {
		return requestSample{}, false
	}

	tsMs := sm.completedAtMs
	if tsMs <= 0 {
		tsMs = sm.createdAtMs
	}
	if tsMs <= 0 {
		return requestSample{}, false
	}

	uncached := nonNegU64(sm.usage.inputTokens)
	cacheRead := nonNegU64(sm.usage.cacheReadTokens)
	output := nonNegU64(sm.usage.outputTokens)
	if uncached == 0 && cacheRead == 0 && output == 0 {
		return requestSample{}, false
	}
	reasoning := min(output, nonNegU64(sm.usage.thinkingTokens))

	dedupConvID := convID
	if hasGM && gm.cascadeID != "" {
		dedupConvID = gm.cascadeID
	}

	rs := requestSample{
		dedupKey:            fmt.Sprintf("agy:%s:%d", dedupConvID, stepIdx),
		sourcePath:          sourcePath,
		agent:               pb.Agent_AGENT_ANTIGRAVITY,
		model:               model,
		tsMs:                tsMs,
		uncachedInput:       uncached,
		cacheRead:           cacheRead,
		cacheWrite:          0,
		output:              output,
		reasoningOutput:     reasoning,
		cacheWriteSupported: false,
	}

	// Only PLANNER_RESPONSE (step_type=15) carries interactive generation timing;
	// CHECKPOINT (step_type=23, compaction) is counted for token/cache totals only.
	if stepType != 15 {
		return rs, true
	}

	genMs := int64(0)
	if hasGM && gm.hasGenDur && gm.genMs > 0 {
		genMs = gm.genMs
	} else if sm.firstTokenAtMs > 0 && sm.completedAtMs > sm.firstTokenAtMs {
		genMs = sm.completedAtMs - sm.firstTokenAtMs
	}

	ttftMs := int64(-1)
	if hasGM && gm.hasTTFT && gm.ttftMs >= 0 {
		ttftMs = gm.ttftMs
	} else if sm.createdAtMs > 0 && sm.firstTokenAtMs >= sm.createdAtMs {
		ttftMs = sm.firstTokenAtMs - sm.createdAtMs
	}

	if output >= 16 && genMs >= 200 && ttftMs >= 0 {
		speed := float64(output) * 1000.0 / float64(genMs)
		if isAgySpeedPlausible(speed, genMs, ttftMs) {
			rs.speedValid = true
			rs.generationMs = genMs
			rs.speedTokPerSec = speed
			rs.ttftMs = float64(ttftMs)
		}
	}
	return rs, true
}

func isMissingTableErr(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "no such table")
}

func parseProtoTimestampMs(b []byte) int64 {
	var sec, nanos uint64
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return 0
		}
		b = b[n:]
		switch {
		case num == 1 && typ == protowire.VarintType:
			v, vn := protowire.ConsumeVarint(b)
			if vn < 0 {
				return 0
			}
			sec = v
			b = b[vn:]
		case num == 2 && typ == protowire.VarintType:
			v, vn := protowire.ConsumeVarint(b)
			if vn < 0 {
				return 0
			}
			nanos = v
			b = b[vn:]
		default:
			vn := protowire.ConsumeFieldValue(num, typ, b)
			if vn < 0 {
				return 0
			}
			b = b[vn:]
		}
	}
	if sec == 0 && nanos == 0 {
		return 0
	}
	return int64(sec)*1000 + int64(nanos)/1_000_000
}

func parseProtoDurationMs(b []byte) (int64, bool) {
	if len(b) == 0 {
		return 0, true
	}
	var sec, nanos uint64
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return 0, false
		}
		b = b[n:]
		switch {
		case num == 1 && typ == protowire.VarintType:
			v, vn := protowire.ConsumeVarint(b)
			if vn < 0 {
				return 0, false
			}
			sec = v
			b = b[vn:]
		case num == 2 && typ == protowire.VarintType:
			v, vn := protowire.ConsumeVarint(b)
			if vn < 0 {
				return 0, false
			}
			nanos = v
			b = b[vn:]
		default:
			vn := protowire.ConsumeFieldValue(num, typ, b)
			if vn < 0 {
				return 0, false
			}
			b = b[vn:]
		}
	}
	return int64(sec)*1000 + int64(nanos)/1_000_000, true
}

func parseAgyProtoUsage(b []byte) agyProtoUsage {
	var u agyProtoUsage
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return u
		}
		b = b[n:]
		switch {
		case typ == protowire.VarintType:
			v, vn := protowire.ConsumeVarint(b)
			if vn < 0 {
				return u
			}
			b = b[vn:]
			switch num {
			case 1:
				u.modelEnum = v
			case 2:
				u.inputTokens = int64(v)
			case 3:
				u.outputTokens = int64(v)
			case 5:
				u.cacheReadTokens = int64(v)
			case 9:
				u.thinkingTokens = int64(v)
			case 10:
				u.responseTokens = int64(v)
			}
		case typ == protowire.BytesType:
			v, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				return u
			}
			b = b[vn:]
			switch num {
			case 7:
				u.botID = string(v)
			case 11:
				u.respID = string(v)
			}
		default:
			vn := protowire.ConsumeFieldValue(num, typ, b)
			if vn < 0 {
				return u
			}
			b = b[vn:]
		}
	}
	return u
}

func parseAgyGenMetadata(b []byte) (agyGenMeta, bool) {
	var outer []byte
	var foundOuter bool
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return agyGenMeta{}, false
		}
		b = b[n:]
		if num == 1 && typ == protowire.BytesType {
			v, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				return agyGenMeta{}, false
			}
			outer = v
			foundOuter = true
			b = b[vn:]
			continue
		}
		vn := protowire.ConsumeFieldValue(num, typ, b)
		if vn < 0 {
			return agyGenMeta{}, false
		}
		b = b[vn:]
	}
	if !foundOuter {
		return agyGenMeta{}, false
	}

	var gm agyGenMeta
	for len(outer) > 0 {
		num, typ, n := protowire.ConsumeTag(outer)
		if n < 0 {
			return gm, gm.model != "" || gm.usage.botID != "" || gm.usage.respID != ""
		}
		outer = outer[n:]
		switch {
		case num == 3 && typ == protowire.VarintType:
			v, vn := protowire.ConsumeVarint(outer)
			if vn < 0 {
				return gm, false
			}
			gm.modelEnum = v
			outer = outer[vn:]
		case num == 4 && typ == protowire.BytesType:
			v, vn := protowire.ConsumeBytes(outer)
			if vn < 0 {
				return gm, false
			}
			gm.usage = parseAgyProtoUsage(v)
			outer = outer[vn:]
		case num == 11 && typ == protowire.BytesType:
			v, vn := protowire.ConsumeBytes(outer)
			if vn < 0 {
				return gm, false
			}
			if ms, ok := parseProtoDurationMs(v); ok {
				gm.ttftMs = ms
				gm.hasTTFT = true
			}
			outer = outer[vn:]
		case num == 12 && typ == protowire.BytesType:
			v, vn := protowire.ConsumeBytes(outer)
			if vn < 0 {
				return gm, false
			}
			if ms, ok := parseProtoDurationMs(v); ok {
				gm.genMs = ms
				gm.hasGenDur = true
			}
			outer = outer[vn:]
		case num == 19 && typ == protowire.BytesType:
			v, vn := protowire.ConsumeBytes(outer)
			if vn < 0 {
				return gm, false
			}
			gm.model = string(v)
			outer = outer[vn:]
		case num == 20 && typ == protowire.BytesType:
			v, vn := protowire.ConsumeBytes(outer)
			if vn < 0 {
				return gm, false
			}
			if k, val := parseProtoStringEntry(v); k == "cascade_id" && val != "" {
				gm.cascadeID = val
			}
			outer = outer[vn:]
		default:
			vn := protowire.ConsumeFieldValue(num, typ, outer)
			if vn < 0 {
				return gm, gm.model != "" || gm.usage.botID != "" || gm.usage.respID != ""
			}
			outer = outer[vn:]
		}
	}
	return gm, gm.model != "" || gm.usage.botID != "" || gm.usage.respID != ""
}

func parseProtoStringEntry(b []byte) (string, string) {
	var k, val string
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return k, val
		}
		b = b[n:]
		if typ == protowire.BytesType {
			v, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				return k, val
			}
			b = b[vn:]
			switch num {
			case 1:
				k = string(v)
			case 2:
				val = string(v)
			}
			continue
		}
		vn := protowire.ConsumeFieldValue(num, typ, b)
		if vn < 0 {
			return k, val
		}
		b = b[vn:]
	}
	return k, val
}

func parseAgyStepMetadata(b []byte) agyStepMeta {
	var sm agyStepMeta
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return sm
		}
		b = b[n:]
		if typ == protowire.BytesType {
			v, vn := protowire.ConsumeBytes(b)
			if vn < 0 {
				return sm
			}
			b = b[vn:]
			switch num {
			case 1:
				sm.createdAtMs = parseProtoTimestampMs(v)
			case 6:
				sm.firstTokenAtMs = parseProtoTimestampMs(v)
			case 7:
				sm.completedAtMs = parseProtoTimestampMs(v)
			case 9:
				sm.usage = parseAgyProtoUsage(v)
			}
			continue
		}
		vn := protowire.ConsumeFieldValue(num, typ, b)
		if vn < 0 {
			return sm
		}
		b = b[vn:]
	}
	return sm
}
