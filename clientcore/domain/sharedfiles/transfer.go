package sharedfiles

import (
	"bytes"
	"math"
	"strings"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

const (
	// DefaultChunkBytes is the protocol default block size (768 KiB) for GetSharedFileChunk.
	DefaultChunkBytes int32 = 786_432
	// Sha256Len is the required byte length of a SHA-256 digest.
	Sha256Len = 32
	// ReconnectWaitMs is the maximum time (30s) Core waits for computer reconnection before failing.
	ReconnectWaitMs int64 = 30_000
	// ReconnectOnlineRetryDelayMs is the brief pause (1s) before auto-retrying a transient drop
	// when the computer still appears online.
	ReconnectOnlineRetryDelayMs int64 = 1_000
	// EtaDelayMs is the minimum active transfer duration (3s) before displaying an ETA.
	EtaDelayMs int64 = 3_000
)

// PartialMeta holds the persisted metadata of an incomplete download on disk / OPFS.
type PartialMeta struct {
	ExpectedSizeBytes int64
	ChunkBytes        int32
	CommittedOffset   int64
	BytesOnDisk       int64
	Sha256            []byte
}

// EffectiveChunkBytes returns the negotiated block size or DefaultChunkBytes when unset/non-positive.
func EffectiveChunkBytes(capChunkBytes int32) int32 {
	if capChunkBytes > 0 {
		return capChunkBytes
	}
	return DefaultChunkBytes
}

// EffectiveStorageFullKind returns the platform-configured storage-full failure kind,
// defaulting to PHONE_STORAGE_FULL when unspecified.
func EffectiveStorageFullKind(configured corev1.SharedFileFailureKind) corev1.SharedFileFailureKind {
	if configured == corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_BROWSER_STORAGE_FULL {
		return corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_BROWSER_STORAGE_FULL
	}
	return corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_PHONE_STORAGE_FULL
}

// HasValidSHA256 reports whether hash is a complete 32-byte SHA-256 digest.
// Shared-file downloads fail closed (CHECKSUM_MISMATCH) when SHA-256 is absent or malformed.
func HasValidSHA256(hash []byte) bool {
	return len(hash) == Sha256Len
}

// ValidateChunk verifies a GetSharedFileChunkResponse against protocol invariants:
//   - TotalBytes must be present and strictly equal expectedSize;
//   - len(Data) must equal chunkBytes (strictly rejecting 0-byte, truncated, or oversized blocks,
//     including for 0-byte files whose single response is a full block of zero padding);
//   - offset must be aligned to chunkBytes and within range.
//
// On success it returns the unpadded valid slice for this block and the new committed offset.
func ValidateChunk(
	resp *v1.GetSharedFileChunkResponse,
	expectedSize int64,
	chunkBytes int32,
	offset int64,
) (validData []byte, committedOffset int64, ok bool) {
	if resp == nil || resp.TotalBytes == nil || expectedSize < 0 || chunkBytes <= 0 || offset < 0 {
		return nil, 0, false
	}
	if *resp.TotalBytes != uint64(expectedSize) {
		return nil, 0, false
	}
	if len(resp.Data) != int(chunkBytes) {
		return nil, 0, false
	}
	step := int64(chunkBytes)
	if offset%step != 0 {
		return nil, 0, false
	}
	if expectedSize == 0 {
		if offset != 0 {
			return nil, 0, false
		}
		return resp.Data[:0], 0, true
	}
	if offset >= expectedSize {
		return nil, 0, false
	}
	validLen := expectedSize - offset
	if validLen > step {
		validLen = step
	}
	return resp.Data[:validLen], offset + validLen, true
}

// IsPartialWellFormed checks whether a persisted partial entry has internally consistent
// size, block alignment, 32-byte SHA-256, and on-disk byte count.
func IsPartialWellFormed(p PartialMeta) bool {
	if p.ExpectedSizeBytes <= 0 || p.ChunkBytes <= 0 || !HasValidSHA256(p.Sha256) || p.CommittedOffset <= 0 {
		return false
	}
	if p.CommittedOffset > p.ExpectedSizeBytes {
		return false
	}
	step := int64(p.ChunkBytes)
	if p.CommittedOffset < p.ExpectedSizeBytes {
		if p.CommittedOffset%step != 0 {
			return false
		}
		return p.BytesOnDisk >= p.CommittedOffset
	}
	return p.BytesOnDisk >= p.ExpectedSizeBytes
}

// EffectivePartialBytes returns the validated downloaded byte count for a well-formed partial.
func EffectivePartialBytes(p PartialMeta) int64 {
	if p.CommittedOffset <= 0 || p.ExpectedSizeBytes <= 0 || p.CommittedOffset > p.ExpectedSizeBytes {
		return 0
	}
	return p.CommittedOffset
}

// CanResumePartial checks whether a persisted partial matches the authoritative file descriptor
// and current negotiated chunk size. When all bytes are already on disk (crash right before
// verification), readyToVerify is true and resumeOffset equals file.SizeBytes.
func CanResumePartial(p PartialMeta, file *v1.SharedFile, chunkBytes int32) (resumeOffset int64, readyToVerify bool, ok bool) {
	if file == nil || file.SizeBytes <= 0 || chunkBytes <= 0 || !HasValidSHA256(file.Sha256) {
		return 0, false, false
	}
	if !IsPartialWellFormed(p) {
		return 0, false, false
	}
	if p.ExpectedSizeBytes != file.SizeBytes || p.ChunkBytes != chunkBytes {
		return 0, false, false
	}
	if !bytes.Equal(p.Sha256, file.Sha256) {
		return 0, false, false
	}
	if p.CommittedOffset == file.SizeBytes {
		return file.SizeBytes, true, true
	}
	return p.CommittedOffset, false, true
}

// ComputeEtaSeconds computes estimated remaining seconds once at least EtaDelayMs have elapsed
// with positive transfer throughput and remaining bytes > 0. Rounds to nearest second, minimum 1.
func ComputeEtaSeconds(gainedBytes, remainingBytes, elapsedMs int64) (int64, bool) {
	if elapsedMs < EtaDelayMs || gainedBytes <= 0 || remainingBytes <= 0 {
		return 0, false
	}
	ratePerSec := float64(gainedBytes) / (float64(elapsedMs) / 1000.0)
	if ratePerSec <= 0 {
		return 0, false
	}
	secs := int64(math.Round(float64(remainingBytes) / ratePerSec))
	if secs < 1 {
		secs = 1
	}
	return secs, true
}

// ClassifyIOFailure normalizes shell-reported SharedFileIOErrorKind into the unified
// SharedFileFailureKind using the shell-configured storageFullKind.
func ClassifyIOFailure(
	kind corev1.SharedFileIOErrorKind,
	detail string,
	storageFullKind corev1.SharedFileFailureKind,
) (corev1.SharedFileFailureKind, string) {
	switch kind {
	case corev1.SharedFileIOErrorKind_SHARED_FILE_IO_ERROR_KIND_STORAGE_FULL:
		return EffectiveStorageFullKind(storageFullKind), ""
	case corev1.SharedFileIOErrorKind_SHARED_FILE_IO_ERROR_KIND_CHECKSUM_MISMATCH:
		return corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_CHECKSUM_MISMATCH, ""
	default:
		return corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_OTHER, strings.TrimSpace(detail)
	}
}

// IsTransientDisconnectCode reports whether a non-daemon command failure code represents
// a transient network, relay, or computer-reachability interruption eligible for the 30s
// auto-reconnect wait.
func IsTransientDisconnectCode(code string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "unavailable",
		"network",
		"unknown",
		"unknown_outcome",
		"expired",
		"deadline_exceeded",
		"canceled",
		"unresolved_host":
		return true
	default:
		return false
	}
}
