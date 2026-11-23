package usagestats

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

var agyEffortSuffixes = []string{"-low", "-medium", "-high", "-xhigh", "-max"}

type agyTranscriptLine struct {
	StepIndex       *int64 `json:"step_index"`
	Type            string `json:"type"`
	Status          string `json:"status"`
	CreatedAt       string `json:"created_at"`
	Model           string `json:"model"`
	InputTokens     int64  `json:"input_tokens"`
	CacheReadTokens int64  `json:"cache_read_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
}

func discoverAntigravityLogs(agyHome string, cutoff time.Time) ([]logCandidate, error) {
	var out []logCandidate
	dbConvIDs := map[string]bool{}

	convDir := filepath.Join(agyHome, "conversations")
	convEntries, err := os.ReadDir(convDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, d := range convEntries {
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".db") {
			continue
		}
		p := filepath.Join(convDir, d.Name())
		info, statErr := os.Stat(p)
		if statErr != nil || info.IsDir() {
			continue
		}
		convID := strings.TrimSuffix(d.Name(), ".db")
		dbConvIDs[convID] = true

		size := info.Size()
		mtime := info.ModTime()
		if walInfo, walErr := os.Stat(p + "-wal"); walErr == nil && !walInfo.IsDir() {
			size += walInfo.Size()
			if walInfo.ModTime().After(mtime) {
				mtime = walInfo.ModTime()
			}
		}
		if mtime.Before(cutoff) {
			continue
		}
		out = append(out, logCandidate{
			agent: pb.Agent_AGENT_ANTIGRAVITY,
			path:  p,
			size:  size,
			mtime: mtime,
		})
	}

	brainDir := filepath.Join(agyHome, "brain")
	entries, err := os.ReadDir(brainDir)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, d := range entries {
		if !d.IsDir() || dbConvIDs[d.Name()] {
			continue
		}
		p := filepath.Join(brainDir, d.Name(), ".system_generated", "logs", "transcript.jsonl")
		info, err := os.Stat(p)
		if err != nil || info.IsDir() || info.ModTime().Before(cutoff) {
			continue
		}
		out = append(out, logCandidate{
			agent: pb.Agent_AGENT_ANTIGRAVITY,
			path:  p,
			size:  info.Size(),
			mtime: info.ModTime(),
		})
	}
	return out, nil
}

// normalizeAntigravityModel strips effort suffixes (-low/-medium/-high/-xhigh/-max)
// so telemetry aggregates under the same base model slug shown in sessions.
func normalizeAntigravityModel(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	for _, suffix := range agyEffortSuffixes {
		if len(s) > len(suffix) && strings.HasSuffix(s, suffix) {
			return strings.TrimSuffix(s, suffix)
		}
	}
	return s
}

func scanAntigravityFile(path string) (samples []requestSample, offset int64, err error) {
	if strings.HasSuffix(path, ".db") {
		return scanAntigravityDB(path)
	}
	return scanAntigravityTranscriptFile(path)
}

func scanAntigravityTranscriptFile(path string) (samples []requestSample, offset int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	convID := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(path))))
	if convID == "" || convID == "." || convID == "/" {
		convID = path
	}

	rd := bufio.NewReaderSize(f, 64*1024)
	seen := map[string]bool{}
	var out []requestSample
	var lineIdx int64

	for {
		line, readErr := rd.ReadBytes('\n')
		if len(line) > 0 {
			if line[len(line)-1] != '\n' && readErr == io.EOF {
				break
			}
			lineIdx++
			offset += int64(len(line))

			var rec agyTranscriptLine
			if json.Unmarshal(line, &rec) == nil {
				if sample, ok := buildAntigravitySample(convID, path, lineIdx, rec); ok && !seen[sample.dedupKey] {
					seen[sample.dedupKey] = true
					out = append(out, sample)
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, offset, readErr
		}
	}
	return out, offset, nil
}

func buildAntigravitySample(convID, sourcePath string, lineIdx int64, rec agyTranscriptLine) (requestSample, bool) {
	if (rec.Type != "PLANNER_RESPONSE" && rec.Type != "CHECKPOINT") || strings.EqualFold(strings.TrimSpace(rec.Status), "RUNNING") {
		return requestSample{}, false
	}
	model := normalizeAntigravityModel(rec.Model)
	if model == "" {
		return requestSample{}, false
	}
	ts, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(rec.CreatedAt))
	if err != nil || ts.IsZero() {
		return requestSample{}, false
	}
	tsMs := ts.UnixMilli()
	if tsMs <= 0 {
		return requestSample{}, false
	}

	// Verified against agy transcript.jsonl: input_tokens is uncached input
	// (mutually exclusive with cache_read_tokens); cache_write & completion timing are absent.
	uncached := nonNegU64(rec.InputTokens)
	cacheRead := nonNegU64(rec.CacheReadTokens)
	output := nonNegU64(rec.OutputTokens)
	if uncached == 0 && cacheRead == 0 && output == 0 {
		return requestSample{}, false
	}

	dedupKey := fmt.Sprintf("agy-line:%s:%d", sourcePath, lineIdx)
	if rec.StepIndex != nil {
		dedupKey = fmt.Sprintf("agy:%s:%d", convID, *rec.StepIndex)
	}

	return requestSample{
		dedupKey:            dedupKey,
		sourcePath:          sourcePath,
		agent:               pb.Agent_AGENT_ANTIGRAVITY,
		model:               model,
		tsMs:                tsMs,
		uncachedInput:       uncached,
		cacheRead:           cacheRead,
		cacheWrite:          0,
		output:              output,
		reasoningOutput:     0,
		cacheWriteSupported: false,
		speedValid:          false,
	}, true
}
