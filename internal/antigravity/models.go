package antigravity

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"google.golang.org/protobuf/proto"
)

// Allowed effort levels for Antigravity models.
var efforts = []string{"low", "medium", "high"}

// EffortOrder maps effort names to sort order.
var effortOrder = map[string]int{
	"low":    1,
	"medium": 2,
	"high":   3,
}

// CheckEffort verifies that effort is one of the supported levels.
func CheckEffort(effort string) error {
	if effort == "" || slices.Contains(efforts, effort) {
		return nil
	}
	return fmt.Errorf("unknown effort %q", effort)
}

// spinnerLineRE matches terminal spinner output like "⠋ Fetching available models...".
var modelIDRE = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9.]+)+$`)

// ParseModels parses the stdout of `agy models` into grouped ModelInfo entries.
// Lines formatted as "<id> <DisplayName>" (e.g. "gemini-3.8-flash-low Gemini 3.8 Flash (Low)")
// are parsed and grouped by their base ID (e.g. "gemini-3.8-flash").
func ParseModels(output string, autoUnavailable bool) ([]*pb.ModelInfo, error) {
	type modelGroup struct {
		baseID      string
		displayName string
		efforts     []string
	}

	var order []string
	groups := make(map[string]*modelGroup)

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(agent.StripANSI(line))
		if line == "" || strings.Contains(line, "Fetching") || strings.Contains(line, "available models") {
			continue
		}
		// Split ID and display name by whitespace
		fields := strings.Fields(line)
		if len(fields) < 1 {
			continue
		}
		rawID := fields[0]
		if !modelIDRE.MatchString(rawID) {
			continue
		}
		rawName := ""
		if len(fields) > 1 {
			rawName = strings.TrimSpace(line[len(rawID):])
		}

		baseID, effort := splitModelEffort(rawID)
		displayName := cleanDisplayName(rawName, effort)
		if displayName == "" {
			displayName = formatDefaultName(baseID)
		}

		g, exists := groups[baseID]
		if !exists {
			g = &modelGroup{
				baseID:      baseID,
				displayName: displayName,
			}
			groups[baseID] = g
			order = append(order, baseID)
		}

		if effort != "" && !slices.Contains(g.efforts, effort) {
			g.efforts = append(g.efforts, effort)
		}
	}

	if len(order) == 0 {
		return nil, fmt.Errorf("no models found in output")
	}

	result := make([]*pb.ModelInfo, 0, len(order))
	for _, baseID := range order {
		g := groups[baseID]
		// Sort efforts canonically: low, medium, high
		sortEfforts(g.efforts)

		defaultEffort := ""
		if slices.Contains(g.efforts, "medium") {
			defaultEffort = "medium"
		} else if len(g.efforts) > 0 {
			defaultEffort = g.efforts[0]
		}

		info := &pb.ModelInfo{
			Slug:            baseID,
			DisplayName:     g.displayName,
			Efforts:         g.efforts,
			DefaultEffort:   defaultEffort,
			AutoUnavailable: autoUnavailable,
		}
		result = append(result, info)
	}

	return result, nil
}

func splitModelEffort(rawID string) (baseID, effort string) {
	for _, eff := range efforts {
		suffix := "-" + eff
		if strings.HasSuffix(rawID, suffix) {
			return strings.TrimSuffix(rawID, suffix), eff
		}
	}
	return rawID, ""
}

func cleanDisplayName(rawName, effort string) string {
	if rawName == "" {
		return ""
	}
	// Strip " (Low)", " (Medium)", " (High)" suffix if present
	for _, eff := range efforts {
		target := fmt.Sprintf("(%s)", strings.ToLower(eff))
		idx := strings.Index(strings.ToLower(rawName), target)
		if idx >= 0 {
			rawName = strings.TrimSpace(rawName[:idx])
			break
		}
	}
	return strings.TrimSpace(rawName)
}

func formatDefaultName(baseID string) string {
	parts := strings.Split(baseID, "-")
	for i, part := range parts {
		if strings.EqualFold(part, "gpt") {
			parts[i] = "GPT"
		} else if strings.EqualFold(part, "oss") {
			parts[i] = "OSS"
		} else if len(part) > 0 {
			parts[i] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, " ")
}

func sortEfforts(effs []string) {
	slices.SortFunc(effs, func(a, b string) int {
		return effortOrder[a] - effortOrder[b]
	})
}

// ModelCache caches `agy models` output in memory.
type ModelCache struct {
	mu        sync.Mutex
	cached    *pb.ListModelsResponse
	fetchedAt time.Time
	ttl       time.Duration
	bin       string
}

// NewModelCache creates a ModelCache with the given TTL.
func NewModelCache(ttl time.Duration) *ModelCache {
	return &ModelCache{ttl: ttl}
}

// Get returns the cached models response if fresh, or invokes fetch.
func (mc *ModelCache) Get(ctx context.Context, bin string, autoUnavailable bool, defaultModel string) (*pb.ListModelsResponse, error) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if mc.cached != nil && mc.bin == bin && time.Since(mc.fetchedAt) < mc.ttl {
		return modelResponse(mc.cached, autoUnavailable, defaultModel), nil
	}

	cmd := exec.CommandContext(ctx, bin, "models")
	out, err := cmd.Output()
	if err != nil {
		if mc.cached != nil && mc.bin == bin {
			return modelResponse(mc.cached, autoUnavailable, defaultModel), nil
		}
		return nil, fmt.Errorf("agy models: %w", err)
	}

	models, err := ParseModels(string(out), autoUnavailable)
	if err != nil {
		if mc.cached != nil && mc.bin == bin {
			return modelResponse(mc.cached, autoUnavailable, defaultModel), nil
		}
		return nil, err
	}

	resp := &pb.ListModelsResponse{
		Models: models,
	}

	mc.cached = resp
	mc.fetchedAt = time.Now()
	mc.bin = bin
	return modelResponse(resp, autoUnavailable, defaultModel), nil
}

func modelResponse(cached *pb.ListModelsResponse, unavailable bool, defaultModel string) *pb.ListModelsResponse {
	resp := proto.CloneOf(cached)
	baseDefault, _ := splitModelEffort(defaultModel)
	for _, m := range resp.Models {
		m.AutoUnavailable = unavailable
		if m.Slug == baseDefault || strings.EqualFold(m.DisplayName, cleanDisplayName(defaultModel, "")) {
			resp.DefaultModel = m.Slug
		}
	}
	return resp
}
