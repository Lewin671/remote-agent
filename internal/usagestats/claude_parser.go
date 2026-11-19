package usagestats

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

type claudeLine struct {
	Type       string    `json:"type"`
	Timestamp  time.Time `json:"timestamp"`
	UUID       string    `json:"uuid"`
	ParentUUID string    `json:"parentUuid"`
	RequestID  string    `json:"requestId"`
	Message    *struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
		} `json:"content"`
		Usage *struct {
			Input         int64 `json:"input_tokens"`
			CacheCreation int64 `json:"cache_creation_input_tokens"`
			CacheRead     int64 `json:"cache_read_input_tokens"`
			Output        int64 `json:"output_tokens"`
			OutputDetails *struct {
				ThinkingTokens int64 `json:"thinking_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	} `json:"message"`
}

func scanClaudeFile(path string, fromOffset int64) (samples []requestSample, newOffset int64, err error) {
	// Because api-request/parentUuid timestamps may precede fromOffset in an incremental
	// tail read, when fromOffset > 0 we still read the file from start (local JSONL files are bounded)
	// and rely on upsertSampleTx(dedup_key) to skip unchanged requests in SQLite, OR read all lines
	// to correlate parent/api-request timestamps while tracking file size/mtime in file_cursors.
	f, err := os.Open(path)
	if err != nil {
		return nil, fromOffset, err
	}
	defer f.Close()

	type agg struct {
		key       string
		model     string
		parentID  string
		uncached  uint64
		cacheRead uint64
		cacheWrt  uint64
		output    uint64
		reasoning uint64
		tReq      int64
		tFirst    int64
		tEnd      int64
	}

	uuidTS := map[string]int64{}
	var lastAPIReqTS int64
	requests := map[string]*agg{}
	var order []string

	rd := bufio.NewReaderSize(f, 64*1024)
	var offset int64
	for {
		line, readErr := rd.ReadBytes('\n')
		if len(line) > 0 {
			if len(line) > 0 && line[len(line)-1] != '\n' && readErr == io.EOF {
				// Partial trailing line still being written; stop before it.
				break
			}
			offset += int64(len(line))
			var rec claudeLine
			if json.Unmarshal(line, &rec) == nil {
				tsMs := rec.Timestamp.UnixMilli()
				if rec.Timestamp.IsZero() {
					tsMs = 0
				}
				if rec.UUID != "" && tsMs > 0 {
					uuidTS[rec.UUID] = tsMs
				}
				switch rec.Type {
				case "api-request":
					if tsMs > 0 {
						lastAPIReqTS = tsMs
					}
				case "assistant":
					if rec.Message == nil || rec.Message.Usage == nil || rec.Message.Model == "" {
						break
					}
					u := rec.Message.Usage
					id := rec.RequestID
					if id == "" {
						id = rec.Message.ID
					}
					if id == "" {
						break
					}
					dedupKey := "claude:" + id
					cur := requests[dedupKey]
					if cur == nil {
						tReq := int64(0)
						if rec.ParentUUID != "" {
							tReq = uuidTS[rec.ParentUUID]
						}
						if lastAPIReqTS > 0 && (tReq == 0 || (lastAPIReqTS >= tReq && (tsMs == 0 || lastAPIReqTS <= tsMs))) {
							tReq = lastAPIReqTS
						}
						lastAPIReqTS = 0
						cur = &agg{
							key:      dedupKey,
							model:    rec.Message.Model,
							parentID: rec.ParentUUID,
							tReq:     tReq,
						}
						requests[dedupKey] = cur
						order = append(order, dedupKey)
					}
					cur.uncached = max(cur.uncached, nonNegU64(u.Input))
					cur.cacheRead = max(cur.cacheRead, nonNegU64(u.CacheRead))
					cur.cacheWrt = max(cur.cacheWrt, nonNegU64(u.CacheCreation))
					cur.output = max(cur.output, nonNegU64(u.Output))
					if u.OutputDetails != nil {
						// Verified on Claude Code transcripts: output_tokens already includes thinking_tokens.
						th := nonNegU64(u.OutputDetails.ThinkingTokens)
						if th > cur.output {
							th = cur.output
						}
						cur.reasoning = max(cur.reasoning, th)
					}
					if tsMs > 0 {
						if cur.tFirst == 0 || tsMs < cur.tFirst {
							cur.tFirst = tsMs
						}
						if tsMs > cur.tEnd {
							cur.tEnd = tsMs
						}
					}
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, fromOffset, readErr
		}
	}

	out := make([]requestSample, 0, len(order))
	for _, k := range order {
		cur := requests[k]
		if cur.tEnd <= 0 || strings.HasPrefix(cur.model, "<") {
			continue
		}
		if cur.uncached == 0 && cur.cacheRead == 0 && cur.cacheWrt == 0 && cur.output == 0 {
			continue
		}
		rs := requestSample{
			dedupKey:            cur.key,
			sourcePath:          path,
			agent:               pb.Agent_AGENT_CLAUDE,
			model:               cur.model,
			tsMs:                cur.tEnd,
			uncachedInput:       cur.uncached,
			cacheRead:           cur.cacheRead,
			cacheWrite:          cur.cacheWrt,
			output:              cur.output,
			reasoningOutput:     min(cur.reasoning, cur.output),
			cacheWriteSupported: true,
		}
		if cur.output >= 16 && cur.tReq > 0 && cur.tFirst > 0 && cur.tEnd > cur.tFirst && cur.tReq <= cur.tFirst {
			genMs := cur.tEnd - cur.tFirst
			if genMs >= 200 {
				rs.speedValid = true
				rs.generationMs = genMs
				rs.speedTokPerSec = float64(cur.output) * 1000.0 / float64(genMs)
				rs.ttftMs = float64(cur.tFirst - cur.tReq)
			}
		}
		out = append(out, rs)
	}
	return out, offset, nil
}

func nonNegU64(v int64) uint64 {
	if v <= 0 {
		return 0
	}
	return uint64(v)
}
