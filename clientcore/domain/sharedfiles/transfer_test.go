package sharedfiles

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

func uint64Ptr(v uint64) *uint64 { return &v }

func TestPlanBlocksSharedVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "shared-files", "blocks.json"))
	if err != nil {
		t.Fatalf("read blocks.json: %v", err)
	}
	var doc struct {
		BlockBytes string `json:"blockBytes"`
		Cases      []struct {
			Name               string   `json:"name"`
			SizeBytes          string   `json:"sizeBytes"`
			Offsets            []string `json:"offsets"`
			LastBlockFileBytes string   `json:"lastBlockFileBytes"`
			InvalidOffsets     []string `json:"invalidOffsets"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse blocks.json: %v", err)
	}
	blockBytes64, err := strconv.ParseInt(doc.BlockBytes, 10, 32)
	if err != nil {
		t.Fatalf("parse blockBytes: %v", err)
	}
	blockBytes := int32(blockBytes64)

	for _, tc := range doc.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			sizeBytes, err := strconv.ParseInt(tc.SizeBytes, 10, 64)
			if err != nil {
				t.Fatalf("parse sizeBytes: %v", err)
			}
			wantLast, err := strconv.ParseInt(tc.LastBlockFileBytes, 10, 32)
			if err != nil {
				t.Fatalf("parse lastBlockFileBytes: %v", err)
			}
			plan, ok := PlanBlocks(sizeBytes, blockBytes)
			if !ok {
				t.Fatalf("PlanBlocks(%d, %d) failed", sizeBytes, blockBytes)
			}
			if plan.LastBlockFileBytes != int32(wantLast) {
				t.Fatalf("lastBlockFileBytes = %d, want %d", plan.LastBlockFileBytes, wantLast)
			}
			if len(plan.Offsets) != len(tc.Offsets) {
				t.Fatalf("len(offsets) = %d, want %d", len(plan.Offsets), len(tc.Offsets))
			}
			for i, wantStr := range tc.Offsets {
				wantOff, _ := strconv.ParseInt(wantStr, 10, 64)
				if plan.Offsets[i] != wantOff {
					t.Fatalf("offsets[%d] = %d, want %d", i, plan.Offsets[i], wantOff)
				}
			}
			// Verify invalidOffsets are rejected by ValidateChunk.
			fullBlock := make([]byte, blockBytes)
			for _, badStr := range tc.InvalidOffsets {
				badOff, _ := strconv.ParseInt(badStr, 10, 64)
				resp := &v1.GetSharedFileChunkResponse{Data: fullBlock, TotalBytes: uint64Ptr(uint64(sizeBytes))}
				if _, _, ok := ValidateChunk(resp, sizeBytes, blockBytes, badOff); ok {
					t.Fatalf("ValidateChunk accepted invalid offset %d for size %d", badOff, sizeBytes)
				}
			}
		})
	}
}

func TestValidateChunk(t *testing.T) {
	const block = int32(16)
	full := bytes.Repeat([]byte{0xab}, int(block))
	zeros := make([]byte, block)

	cases := []struct {
		name         string
		resp         *v1.GetSharedFileChunkResponse
		expectedSize int64
		chunkBytes   int32
		offset       int64
		wantOK       bool
		wantLen      int
		wantCommit   int64
	}{
		{
			name:         "nil response rejected",
			resp:         nil,
			expectedSize: 10,
			chunkBytes:   block,
			offset:       0,
			wantOK:       false,
		},
		{
			name:         "missing total_bytes rejected",
			resp:         &v1.GetSharedFileChunkResponse{Data: full},
			expectedSize: 10,
			chunkBytes:   block,
			offset:       0,
			wantOK:       false,
		},
		{
			name:         "wrong total_bytes rejected",
			resp:         &v1.GetSharedFileChunkResponse{Data: full, TotalBytes: uint64Ptr(11)},
			expectedSize: 10,
			chunkBytes:   block,
			offset:       0,
			wantOK:       false,
		},
		{
			name:         "zero byte chunk rejected for non-empty file",
			resp:         &v1.GetSharedFileChunkResponse{Data: nil, TotalBytes: uint64Ptr(10)},
			expectedSize: 10,
			chunkBytes:   block,
			offset:       0,
			wantOK:       false,
		},
		{
			name:         "zero byte chunk rejected for empty file too",
			resp:         &v1.GetSharedFileChunkResponse{Data: nil, TotalBytes: uint64Ptr(0)},
			expectedSize: 0,
			chunkBytes:   block,
			offset:       0,
			wantOK:       false,
		},
		{
			name:         "short chunk rejected",
			resp:         &v1.GetSharedFileChunkResponse{Data: full[:10], TotalBytes: uint64Ptr(10)},
			expectedSize: 10,
			chunkBytes:   block,
			offset:       0,
			wantOK:       false,
		},
		{
			name:         "oversized chunk rejected",
			resp:         &v1.GetSharedFileChunkResponse{Data: append(full, 1), TotalBytes: uint64Ptr(10)},
			expectedSize: 10,
			chunkBytes:   block,
			offset:       0,
			wantOK:       false,
		},
		{
			name:         "empty file with full zero block accepted",
			resp:         &v1.GetSharedFileChunkResponse{Data: zeros, TotalBytes: uint64Ptr(0)},
			expectedSize: 0,
			chunkBytes:   block,
			offset:       0,
			wantOK:       true,
			wantLen:      0,
			wantCommit:   0,
		},
		{
			name:         "first full block of multi-block file",
			resp:         &v1.GetSharedFileChunkResponse{Data: full, TotalBytes: uint64Ptr(21)},
			expectedSize: 21,
			chunkBytes:   block,
			offset:       0,
			wantOK:       true,
			wantLen:      16,
			wantCommit:   16,
		},
		{
			name:         "tail block truncated to remaining 5 bytes",
			resp:         &v1.GetSharedFileChunkResponse{Data: full, TotalBytes: uint64Ptr(21)},
			expectedSize: 21,
			chunkBytes:   block,
			offset:       16,
			wantOK:       true,
			wantLen:      5,
			wantCommit:   21,
		},
		{
			name:         "unaligned offset rejected",
			resp:         &v1.GetSharedFileChunkResponse{Data: full, TotalBytes: uint64Ptr(21)},
			expectedSize: 21,
			chunkBytes:   block,
			offset:       8,
			wantOK:       false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, commit, ok := ValidateChunk(tc.resp, tc.expectedSize, tc.chunkBytes, tc.offset)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if len(data) != tc.wantLen || commit != tc.wantCommit {
				t.Fatalf("got (len=%d, commit=%d), want (len=%d, commit=%d)", len(data), commit, tc.wantLen, tc.wantCommit)
			}
		})
	}
}

func TestCanResumePartial(t *testing.T) {
	hashA := bytes.Repeat([]byte{0x11}, 32)
	hashB := bytes.Repeat([]byte{0x22}, 32)
	file := &v1.SharedFile{FileId: "f1", SizeBytes: 2000, Sha256: hashA}

	cases := []struct {
		name      string
		partial   PartialMeta
		file      *v1.SharedFile
		chunk     int32
		wantOff   int64
		wantReady bool
		wantOK    bool
	}{
		{
			name:    "valid mid-file partial resumes at aligned offset",
			partial: PartialMeta{ExpectedSizeBytes: 2000, ChunkBytes: 1024, CommittedOffset: 1024, BytesOnDisk: 1024, Sha256: hashA},
			file:    file,
			chunk:   1024,
			wantOff: 1024,
			wantOK:  true,
		},
		{
			name:      "all blocks written (exact size) jumps straight to verify",
			partial:   PartialMeta{ExpectedSizeBytes: 2000, ChunkBytes: 1024, CommittedOffset: 2000, BytesOnDisk: 2000, Sha256: hashA},
			file:      file,
			chunk:     1024,
			wantOff:   2000,
			wantReady: true,
			wantOK:    true,
		},
		{
			name:    "committed offset exceeding expected size rejected",
			partial: PartialMeta{ExpectedSizeBytes: 2000, ChunkBytes: 1024, CommittedOffset: 2048, BytesOnDisk: 2000, Sha256: hashA},
			file:    file,
			chunk:   1024,
			wantOK:  false,
		},
		{
			name:    "truncated part file on disk rejected",
			partial: PartialMeta{ExpectedSizeBytes: 2000, ChunkBytes: 1024, CommittedOffset: 1024, BytesOnDisk: 512, Sha256: hashA},
			file:    file,
			chunk:   1024,
			wantOK:  false,
		},
		{
			name:    "unaligned offset rejected",
			partial: PartialMeta{ExpectedSizeBytes: 2000, ChunkBytes: 1024, CommittedOffset: 500, BytesOnDisk: 500, Sha256: hashA},
			file:    file,
			chunk:   1024,
			wantOK:  false,
		},
		{
			name:    "chunk size changed rejected",
			partial: PartialMeta{ExpectedSizeBytes: 2000, ChunkBytes: 512, CommittedOffset: 512, BytesOnDisk: 512, Sha256: hashA},
			file:    file,
			chunk:   1024,
			wantOK:  false,
		},
		{
			name:    "sha256 mismatch rejected",
			partial: PartialMeta{ExpectedSizeBytes: 2000, ChunkBytes: 1024, CommittedOffset: 1024, BytesOnDisk: 1024, Sha256: hashB},
			file:    file,
			chunk:   1024,
			wantOK:  false,
		},
		{
			name:    "missing file sha256 rejected",
			partial: PartialMeta{ExpectedSizeBytes: 2000, ChunkBytes: 1024, CommittedOffset: 1024, BytesOnDisk: 1024, Sha256: hashA},
			file:    &v1.SharedFile{FileId: "f1", SizeBytes: 2000},
			chunk:   1024,
			wantOK:  false,
		},
		{
			name:    "zero byte file has no partial resume",
			partial: PartialMeta{ExpectedSizeBytes: 0, ChunkBytes: 1024, CommittedOffset: 0, BytesOnDisk: 0, Sha256: hashA},
			file:    &v1.SharedFile{FileId: "f1", SizeBytes: 0, Sha256: hashA},
			chunk:   1024,
			wantOK:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			off, ready, ok := CanResumePartial(tc.partial, tc.file, tc.chunk)
			if ok != tc.wantOK || off != tc.wantOff || ready != tc.wantReady {
				t.Fatalf("got (off=%d, ready=%v, ok=%v), want (off=%d, ready=%v, ok=%v)",
					off, ready, ok, tc.wantOff, tc.wantReady, tc.wantOK)
			}
		})
	}
}

func TestComputeEtaSeconds(t *testing.T) {
	cases := []struct {
		name      string
		gained    int64
		remaining int64
		elapsedMs int64
		wantSecs  int64
		wantOK    bool
	}{
		{"under 3s hides ETA", 1_000_000, 9_000_000, 2_999, 0, false},
		{"exactly 3s shows rounded ETA", 3_000_000, 9_000_000, 3_000, 9, true},
		{"rounds half-up", 1_000, 1_900, 3_000, 6, true},
		{"clamps sub-second ETA to 1s", 10_000_000, 100, 3_000, 1, true},
		{"zero gained hides ETA", 0, 1_000, 5_000, 0, false},
		{"zero remaining hides ETA", 1_000, 0, 5_000, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ComputeEtaSeconds(tc.gained, tc.remaining, tc.elapsedMs)
			if ok != tc.wantOK || got != tc.wantSecs {
				t.Fatalf("got (%d, %v), want (%d, %v)", got, ok, tc.wantSecs, tc.wantOK)
			}
		})
	}
}

func TestClassifyIOFailure(t *testing.T) {
	cases := []struct {
		name       string
		ioKind     corev1.SharedFileIOErrorKind
		detail     string
		cfgFull    corev1.SharedFileFailureKind
		wantKind   corev1.SharedFileFailureKind
		wantDetail string
	}{
		{
			name:     "storage full normalizes to phone by default",
			ioKind:   corev1.SharedFileIOErrorKind_SHARED_FILE_IO_ERROR_KIND_STORAGE_FULL,
			detail:   "ENOSPC",
			cfgFull:  corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_UNSPECIFIED,
			wantKind: corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_PHONE_STORAGE_FULL,
		},
		{
			name:     "storage full normalizes to browser when configured",
			ioKind:   corev1.SharedFileIOErrorKind_SHARED_FILE_IO_ERROR_KIND_STORAGE_FULL,
			detail:   "QuotaExceededError",
			cfgFull:  corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_BROWSER_STORAGE_FULL,
			wantKind: corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_BROWSER_STORAGE_FULL,
		},
		{
			name:     "raw ENOSPC detail under OTHER normalizes to storage full",
			ioKind:   corev1.SharedFileIOErrorKind_SHARED_FILE_IO_ERROR_KIND_OTHER,
			detail:   "write failed: ENOSPC (No space left on device)",
			cfgFull:  corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_PHONE_STORAGE_FULL,
			wantKind: corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_PHONE_STORAGE_FULL,
		},
		{
			name:     "raw QuotaExceededError detail normalizes to browser full",
			ioKind:   corev1.SharedFileIOErrorKind_SHARED_FILE_IO_ERROR_KIND_OTHER,
			detail:   "DOMException: QuotaExceededError",
			cfgFull:  corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_BROWSER_STORAGE_FULL,
			wantKind: corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_BROWSER_STORAGE_FULL,
		},
		{
			name:     "checksum mismatch stays checksum mismatch",
			ioKind:   corev1.SharedFileIOErrorKind_SHARED_FILE_IO_ERROR_KIND_CHECKSUM_MISMATCH,
			detail:   "digest mismatch",
			cfgFull:  corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_PHONE_STORAGE_FULL,
			wantKind: corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_CHECKSUM_MISMATCH,
		},
		{
			name:       "generic IO error preserves trimmed detail",
			ioKind:     corev1.SharedFileIOErrorKind_SHARED_FILE_IO_ERROR_KIND_OTHER,
			detail:     "  permission denied  ",
			cfgFull:    corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_PHONE_STORAGE_FULL,
			wantKind:   corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_OTHER,
			wantDetail: "permission denied",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotKind, gotDetail := ClassifyIOFailure(tc.ioKind, tc.detail, tc.cfgFull)
			if gotKind != tc.wantKind || gotDetail != tc.wantDetail {
				t.Fatalf("got (%v, %q), want (%v, %q)", gotKind, gotDetail, tc.wantKind, tc.wantDetail)
			}
		})
	}
}

func TestIsTransientDisconnectCode(t *testing.T) {
	cases := []struct {
		code string
		want bool
	}{
		{"unavailable", true},
		{"network", true},
		{"unknown", true},
		{"unknown_outcome", true},
		{"expired", true},
		{"deadline_exceeded", true},
		{"canceled", true},
		{"unresolved_host", true},
		{"", false},
		{"   ", false},
		{"aborted", false},
		{"machine_offline", false},
		{"not_found", false},
		{"failed_precondition", false},
	}
	for _, tc := range cases {
		if got := IsTransientDisconnectCode(tc.code); got != tc.want {
			t.Fatalf("IsTransientDisconnectCode(%q) = %v, want %v", tc.code, got, tc.want)
		}
	}
}
