package usagestats

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

type codexUsageNumbers struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
}

type codexLine struct {
	Timestamp time.Time       `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

func scanCodexFile(path string) (samples []requestSample, offset int64, lastModel string, lastTotal int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, "", -1, err
	}
	defer f.Close()

	rd := bufio.NewReaderSize(f, 64*1024)
	var curModel string
	var tReq, tFirst int64
	var seenResponses = map[string]bool{}
	var seenTotalTokens = map[int64]bool{}
	var prevCumTotal int64 = -1

	resetTurn := func(tsMs int64) {
		if tsMs > 0 {
			tReq = tsMs
		}
		tFirst = 0
	}
	noteFirst := func(tsMs int64) {
		if tsMs > 0 && tReq > 0 && tFirst == 0 && tsMs >= tReq {
			tFirst = tsMs
		}
	}

	var out []requestSample
	var lineIdx int64

	for {
		line, readErr := rd.ReadBytes('\n')
		if len(line) > 0 {
			if len(line) > 0 && line[len(line)-1] != '\n' && readErr == io.EOF {
				break
			}
			lineIdx++
			offset += int64(len(line))

			var rec codexLine
			if json.Unmarshal(line, &rec) == nil {
				tsMs := int64(0)
				if !rec.Timestamp.IsZero() {
					tsMs = rec.Timestamp.UnixMilli()
				}
				switch rec.Type {
				case "turn_context":
					var pl struct {
						Model string `json:"model"`
					}
					if json.Unmarshal(rec.Payload, &pl) == nil && pl.Model != "" {
						curModel = pl.Model
					}
					if tReq == 0 && tsMs > 0 {
						tReq = tsMs
					}

				case "response_item":
					var pl struct {
						Type string `json:"type"`
						Role string `json:"role"`
					}
					if json.Unmarshal(rec.Payload, &pl) == nil {
						switch {
						case pl.Type == "custom_tool_call_output" || pl.Type == "function_call_output" || (pl.Type == "message" && pl.Role == "user"):
							resetTurn(tsMs)
						case pl.Type == "reasoning" || pl.Type == "custom_tool_call" || pl.Type == "function_call" || (pl.Type == "message" && pl.Role == "assistant"):
							noteFirst(tsMs)
						}
					}

				case "token_usage_record":
					var pl struct {
						ResponseID        string             `json:"response_id"`
						Usage             *codexUsageNumbers `json:"usage"`
						ThreadTokenUsage  *codexUsageNumbers `json:"thread_token_usage"`
					}
					if json.Unmarshal(rec.Payload, &pl) == nil && pl.Usage != nil && curModel != "" {
						dedupKey := ""
						if pl.ResponseID != "" {
							dedupKey = "codex:" + pl.ResponseID
						} else {
							dedupKey = fmt.Sprintf("codex:%s:%d", path, lineIdx)
						}
						if !seenResponses[dedupKey] {
							seenResponses[dedupKey] = true
							if pl.ThreadTokenUsage != nil && pl.ThreadTokenUsage.TotalTokens > 0 {
								seenTotalTokens[pl.ThreadTokenUsage.TotalTokens] = true
								prevCumTotal = pl.ThreadTokenUsage.TotalTokens
							}
							if s, ok := buildCodexSample(dedupKey, path, curModel, tsMs, tReq, tFirst, *pl.Usage); ok {
								out = append(out, s)
							}
							// Next call starts from completion of tool output or next prompt; avoid reusing stale tFirst.
							tFirst = 0
						}
					}

				case "event_msg":
					var head struct {
						Type string `json:"type"`
					}
					if json.Unmarshal(rec.Payload, &head) != nil {
						break
					}
					switch head.Type {
					case "task_started", "turn_started", "user_message":
						resetTurn(tsMs)
					case "agent_message", "agent_reasoning", "item_completed":
						noteFirst(tsMs)
					case "token_count":
						var tc struct {
							Info *struct {
								Total *codexUsageNumbers `json:"total_token_usage"`
								Last  *codexUsageNumbers `json:"last_token_usage"`
							} `json:"info"`
						}
						if json.Unmarshal(rec.Payload, &tc) == nil && tc.Info != nil && tc.Info.Last != nil && curModel != "" {
							cumTotal := int64(0)
							if tc.Info.Total != nil {
								cumTotal = tc.Info.Total.TotalTokens
							}
							if cumTotal > 0 {
								if seenTotalTokens[cumTotal] || cumTotal == prevCumTotal {
									break
								}
								seenTotalTokens[cumTotal] = true
								prevCumTotal = cumTotal
							} else {
								// Without monotonic total_token_usage, skip duplicate identical last snapshot at same timestamp.
								prevCumTotal = cumTotal
							}
							dedupKey := fmt.Sprintf("codex-tc:%s:%d", path, lineIdx)
							if cumTotal > 0 {
								dedupKey = fmt.Sprintf("codex-tot:%s:%d", path, cumTotal)
							}
							if s, ok := buildCodexSample(dedupKey, path, curModel, tsMs, tReq, tFirst, *tc.Info.Last); ok {
								out = append(out, s)
							}
							tFirst = 0
						}
					}
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, offset, curModel, prevCumTotal, readErr
		}
	}
	return out, offset, curModel, prevCumTotal, nil
}

func buildCodexSample(dedupKey, sourcePath, model string, tsMs, tReq, tFirst int64, u codexUsageNumbers) (requestSample, bool) {
	if tsMs <= 0 || strings.TrimSpace(model) == "" {
		return requestSample{}, false
	}
	// Verified against Codex rollouts: input_tokens == uncached + cached_input_tokens + cache_write_input_tokens,
	// output_tokens includes reasoning_output_tokens, and rare {in:0, out:0, total:>0} snapshots should be skipped.
	if u.InputTokens <= 0 && u.OutputTokens <= 0 {
		return requestSample{}, false
	}
	inTotal := nonNegU64(u.InputTokens)
	cacheRead := min(inTotal, nonNegU64(u.CachedInputTokens))
	remAfterRead := inTotal - cacheRead
	cacheWrite := min(remAfterRead, nonNegU64(u.CacheWriteInputTokens))
	uncached := remAfterRead - cacheWrite

	output := nonNegU64(u.OutputTokens)
	reasoning := min(output, nonNegU64(u.ReasoningOutputTokens))
	cwSupported := u.CacheWriteInputTokens > 0

	rs := requestSample{
		dedupKey:            dedupKey,
		sourcePath:          sourcePath,
		agent:               pb.Agent_AGENT_CODEX,
		model:               model,
		tsMs:                tsMs,
		uncachedInput:       uncached,
		cacheRead:           cacheRead,
		cacheWrite:          cacheWrite,
		output:              output,
		reasoningOutput:     reasoning,
		cacheWriteSupported: cwSupported,
	}
	if output >= 16 && tReq > 0 && tFirst > 0 && tsMs > tFirst && tReq <= tFirst {
		genMs := tsMs - tFirst
		if genMs >= 200 {
			rs.speedValid = true
			rs.generationMs = genMs
			rs.speedTokPerSec = float64(output) * 1000.0 / float64(genMs)
			rs.ttftMs = float64(tFirst - tReq)
		}
	}
	return rs, true
}
