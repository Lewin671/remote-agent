package attachments

import (
	"math"
	"strconv"
	"strings"
	"unicode"

	"github.com/Lewin671/remote-agent/clientcore/domain/sharedfiles"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

const (
	// DefaultUploadChunkBytes is the standard 768 KiB upload chunk cap shared by images and files.
	DefaultUploadChunkBytes int32 = 768 * 1024
	// UploadStepMaxRetries is the per-step retry limit (3 retries = 4 total attempts) for Begin/Put/Complete.
	// Resets after every step/chunk that makes forward progress.
	UploadStepMaxRetries = 3
	// UploadCompleteMaxPolls is the maximum number of idempotent CompleteImageUpload poll retries
	// when the daemon still reports RECEIVING with persisted_bytes == total_bytes (1 initial + 3 polls = 4 total).
	UploadCompleteMaxPolls = 3
	// UploadReconnectWindowMs is the 30-second automatic reconnect wait window for an interrupted upload.
	UploadReconnectWindowMs int64 = 30_000
	// UploadOnlineJitterMs is the 1-second buffer before auto-resuming when the computer still appears online.
	UploadOnlineJitterMs int64 = 1_000
	// PersistedSchemaVersion is the only accepted schema_version for PersistedDraftRecord and PersistedOutboxRecord.
	PersistedSchemaVersion uint32 = 1
)

// FormatOriginalTotalSize formats the total byte size of draft images for the Original toggle
// (3A gap #1), returning nil when totalBytes <= 0. Uses Core's unified 1024-base size formatting.
func FormatOriginalTotalSize(totalBytes int64) *corev1.Text {
	if totalBytes <= 0 {
		return nil
	}
	return text.Msg(text.KeyImageTotalSize, text.Str("size", sharedfiles.FormatFileSize(totalBytes)))
}

// DraftViewerPositionText returns the formatted "N / M" position text for DraftImageViewer
// (3A gap #2), clamping index into [0, total-1], or nil when total <= 0.
func DraftViewerPositionText(index, total int32) *corev1.Text {
	if total <= 0 {
		return nil
	}
	if index < 0 {
		index = 0
	}
	if index >= total {
		index = total - 1
	}
	return text.Plain(strconv.Itoa(int(index)+1) + " / " + strconv.Itoa(int(total)))
}

// ResolveUploadChunkBytes computes the effective upload chunk size bounded by configCap,
// daemonMax (from ImageUploadStatus.max_chunk_bytes), and DefaultUploadChunkBytes (768 KiB).
func ResolveUploadChunkBytes(configCap, daemonMax int32) int32 {
	chunk := DefaultUploadChunkBytes
	if configCap > 0 && configCap < chunk {
		chunk = configCap
	}
	if daemonMax > 0 && daemonMax < chunk {
		chunk = daemonMax
	}
	if chunk <= 0 {
		return DefaultUploadChunkBytes
	}
	return chunk
}

// UploadPollBackoffMs returns the deterministic poll delay in ms for CompleteImageUpload RECEIVING retries.
func UploadPollBackoffMs(pollRetry int) int64 {
	switch pollRetry {
	case 0:
		return 250
	case 1:
		return 500
	default:
		return 1000
	}
}

// ValidateUploadProgress checks that daemon-reported persisted_bytes is within [0, total].
func ValidateUploadProgress(persisted, total int64) bool {
	return total >= 0 && persisted >= 0 && persisted <= total
}

// ValidatePutAdvance enforces that after uploading a non-empty chunk at prevOffset,
// daemon-reported nextPersisted stays within [0, total] and strictly advances past prevOffset.
func ValidatePutAdvance(prevOffset, chunkLen, nextPersisted, total int64) bool {
	if !ValidateUploadProgress(nextPersisted, total) {
		return false
	}
	if chunkLen > 0 && nextPersisted <= prevOffset {
		return false
	}
	return true
}

// UploadProgressRatio returns the normalized [0, 1] progress ratio and rounded integer percent (0..100).
// Calling code should keep progress monotonically non-decreasing within one upload generation.
func UploadProgressRatio(persisted, total int64, completed bool) (float64, int32) {
	if completed {
		return 1.0, 100
	}
	if total <= 0 || persisted <= 0 {
		return 0, 0
	}
	if persisted >= total {
		return 1.0, 100
	}
	ratio := float64(persisted) / float64(total)
	if ratio < 0 {
		ratio = 0
	} else if ratio > 1 {
		ratio = 1
	}
	pct := int32(math.Round(ratio * 100))
	if pct < 0 {
		pct = 0
	} else if pct > 100 {
		pct = 100
	}
	return ratio, pct
}

// IsUploadTransientCode reports whether a non-daemon transport/sync error code represents
// a transient disconnect/timeout eligible for step retry or reconnect wait.
func IsUploadTransientCode(code string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "unavailable", "network", "unknown", "unknown_outcome",
		"expired", "deadline_exceeded", "canceled", "unresolved_host":
		return true
	default:
		return false
	}
}

// ShouldRestartUpload reports whether a signed daemon failure on an existing transfer_id
// should trigger a single automatic restart with a fresh transfer_id.
func ShouldRestartUpload(fromDaemon bool, errCode string) bool {
	if !fromDaemon {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(errCode)) {
	case "not_found", "already_exists", "generation_conflict":
		return true
	default:
		return false
	}
}

// ClassifyIOErrorToImageProblem maps structured shell I/O & image-preparation errors into ImageProblemView.
// Enum kind takes strict precedence; no localized UI strings are inspected.
func ClassifyIOErrorToImageProblem(
	kind corev1.AttachmentIOErrorKind,
	errCode, rawDiag string,
	isReadingSource bool,
) *corev1.ImageProblemView {
	switch kind {
	case corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_ORIGINAL_TOO_LARGE:
		return BuildImageProblem(corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_ORIGINAL_TOO_LARGE)
	case corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_DIMENSIONS:
		return BuildImageProblem(corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_DIMENSIONS)
	case corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING,
		corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_SOURCE_CHANGED:
		return BuildImageProblem(corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_SOURCE_MISSING)
	case corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_DECODE,
		corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_DIRECTORY:
		return BuildImageProblem(corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_DECODE)
	case corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_STORAGE_FULL:
		return BuildImageProblem(corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_STORAGE_FULL)
	}
	return ClassifyImageProblem(&corev1.ImageProblemInput{
		ErrorCode:       errCode,
		RawDiagnostic:   rawDiag,
		IsReadingSource: isReadingSource,
	})
}

// ClassifyIOErrorToFileText maps structured shell local I/O errors for a file attachment to localized Text.
func ClassifyIOErrorToFileText(kind corev1.AttachmentIOErrorKind, maxFileBytes int64, displayName string) *corev1.Text {
	switch kind {
	case corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_STORAGE_FULL:
		return text.Msg(text.KeyAttachmentLocalStorageFull)
	case corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING:
		return text.Msg(text.KeyAttachmentFileMissing)
	case corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_SOURCE_CHANGED:
		return text.Msg(text.KeyAttachmentFileChanged)
	case corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_FILE_TOO_LARGE:
		maxMB := MaxFileMB(maxFileBytes)
		if displayName != "" {
			return text.Msg(text.KeyAttachmentNamedSizeLimit, text.Str("name", displayName), text.Int("limit", maxMB))
		}
		return text.Msg(text.KeyAttachmentSizeLimit, text.Int("limit", maxMB))
	case corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_DIRECTORY:
		return text.Msg(text.KeyAttachmentZipFolder)
	default:
		return text.Msg(text.KeyAttachmentReadFailed)
	}
}

// ClassifyDaemonFileUploadError maps a daemon or transport upload failure for a file attachment.
func ClassifyDaemonFileUploadError(fromDaemon bool, errCode string, errText *corev1.Text) *corev1.Text {
	c := strings.ToLower(strings.TrimSpace(errCode))
	switch c {
	case "resource_exhausted":
		return text.Msg(text.KeyAttachmentFileStorageFull)
	case "invalid_argument", "data_loss":
		return text.Msg(text.KeyAttachmentFileVerifyFailed)
	case "permission_denied", "unauthenticated":
		return text.Msg(text.KeyAttachmentFileUploadUnauthorized)
	case "already_exists":
		return text.Msg(text.KeyAttachmentFileChanged)
	case "not_found", "unavailable", "network", "unknown", "unknown_outcome",
		"deadline_exceeded", "expired", "canceled", "unresolved_host", "generation_conflict":
		return text.Msg(text.KeyAttachmentUploadFailed)
	}
	if fromDaemon && errText != nil {
		return errText
	}
	return text.Msg(text.KeyAttachmentUploadFailed)
}

// ClassifyStorageErrorText maps a local draft/outbox storage I/O error to a localized banner Text.
func ClassifyStorageErrorText(kind corev1.AttachmentIOErrorKind, isRead bool) *corev1.Text {
	if kind == corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_STORAGE_FULL {
		return text.Msg(text.KeyAttachmentLocalStorageFull)
	}
	if isRead {
		return text.Msg(text.KeyErrorsTheDraftCouldNotBeRestoredYouCanStill)
	}
	return text.Msg(text.KeyErrorsTheDraftCouldNotBeSavedLocallyKeepThe)
}

// DraftItemStatusText returns the concise localized status label for one draft attachment item.
func DraftItemStatusText(
	kind corev1.AttachmentItemKind,
	state corev1.AttachmentItemState,
	progressPct int32,
	problem *corev1.ImageProblemView,
	fileErr *corev1.Text,
) *corev1.Text {
	switch state {
	case corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_PREPARING:
		return text.Msg(text.KeyAttachmentPreparing)
	case corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_UPLOADING:
		if progressPct > 0 && progressPct < 100 {
			return text.Msg(text.KeyAttachmentUploadProgress, text.Int("percent", int64(progressPct)))
		}
		if kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE {
			return text.Msg(text.KeyImageUploading)
		}
		return text.Msg(text.KeyAttachmentUploading)
	case corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_PAUSED:
		return text.Msg(text.KeyAttachmentUploadPaused)
	case corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_WAITING_RECONNECT:
		return text.Msg(text.KeyAttachmentUploadWaitingReconnect)
	case corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_READY:
		if kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE {
			return text.Msg(text.KeyImageUploaded)
		}
		return nil
	case corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_FAILED:
		if kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE {
			if problem != nil && problem.Message != nil {
				return problem.Message
			}
			return text.Msg(text.KeyAttachmentUploadFailed)
		}
		if fileErr != nil {
			return fileErr
		}
		return text.Msg(text.KeyAttachmentUploadFailed)
	default:
		return nil
	}
}

// Shared localized status/error Text constructors used by the Core upload/draft/outbox engine.
func OutboxRestartedReason() *corev1.Text {
	return text.Msg(text.KeyAttachmentOutboxRestartedUnconfirmed)
}

func RestoreWaitReason() *corev1.Text {
	return text.Msg(text.KeyAttachmentRestoreWait)
}

func UploadProgressInvalidText() *corev1.Text {
	return text.Msg(text.KeyAttachmentUploadProgressInvalid)
}

func UploadIncompleteText() *corev1.Text {
	return text.Msg(text.KeyAttachmentUploadIncomplete)
}

func UploadStillVerifyingText() *corev1.Text {
	return text.Msg(text.KeyErrorsTheComputerIsStillVerifyingAttachmentsRetryShortly)
}

func UploadMadeNoProgressText() *corev1.Text {
	return text.Msg(text.KeyErrorsUploadMadeNoProgress)
}

func UploadMissingIDText() *corev1.Text {
	return text.Msg(text.KeyErrorsTheComputerDidNotReturnAnAttachmentId)
}

// ScopeStorageKey returns the canonical storage key for a DraftScope ("session:<id>" or "new:<id>").
func ScopeStorageKey(scope *corev1.DraftScope) string {
	if scope == nil || scope.Id == "" {
		return ""
	}
	switch scope.Kind {
	case corev1.DraftScopeKind_DRAFT_SCOPE_KIND_SESSION:
		return "session:" + scope.Id
	case corev1.DraftScopeKind_DRAFT_SCOPE_KIND_NEW_SESSION:
		return "new:" + scope.Id
	default:
		return ""
	}
}

// ParseScopeStorageKey parses a canonical draft storage key back into a DraftScope.
func ParseScopeStorageKey(key string) (*corev1.DraftScope, bool) {
	if strings.HasPrefix(key, "session:") && len(key) > len("session:") {
		return &corev1.DraftScope{
			Kind: corev1.DraftScopeKind_DRAFT_SCOPE_KIND_SESSION,
			Id:   key[len("session:"):],
		}, true
	}
	if strings.HasPrefix(key, "new:") && len(key) > len("new:") {
		return &corev1.DraftScope{
			Kind: corev1.DraftScopeKind_DRAFT_SCOPE_KIND_NEW_SESSION,
			Id:   key[len("new:"):],
		}, true
	}
	return nil, false
}

// OutboxStorageKey returns the canonical storage key for one outbox record ("out:<sid>:<reqID>").
func OutboxStorageKey(sessionID, requestID string) string {
	if sessionID == "" || requestID == "" {
		return ""
	}
	return "out:" + sessionID + ":" + requestID
}

// MergePutBackText merges restored message text into existing composer draft text without overwriting
// what the user already typed.
func MergePutBackText(existing, restored string) string {
	if restored == "" {
		return existing
	}
	if strings.TrimSpace(existing) == "" {
		return restored
	}
	return strings.TrimRightFunc(existing, unicode.IsSpace) + "\n" + restored
}

// EncodePersistedDraft serializes a draft record using Core's canonical schema_version = 1 format.
func EncodePersistedDraft(rec *corev1.PersistedDraftRecord) ([]byte, error) {
	if rec == nil {
		return nil, nil
	}
	rec.SchemaVersion = PersistedSchemaVersion
	return rec.Marshal()
}

// DecodePersistedDraft strictly decodes a persisted draft blob, rejecting legacy formats,
// unknown fields, corrupt bytes, or unsupported schema versions.
func DecodePersistedDraft(raw []byte) (*corev1.PersistedDraftRecord, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var rec corev1.PersistedDraftRecord
	if err := rec.Unmarshal(raw); err != nil {
		return nil, false
	}
	if rec.SchemaVersion != PersistedSchemaVersion || ScopeStorageKey(rec.Scope) == "" {
		return nil, false
	}
	return &rec, true
}

// EncodePersistedOutbox serializes an outbox record using Core's canonical schema_version = 1 format.
func EncodePersistedOutbox(rec *corev1.PersistedOutboxRecord) ([]byte, error) {
	if rec == nil {
		return nil, nil
	}
	rec.SchemaVersion = PersistedSchemaVersion
	return rec.Marshal()
}

// DecodePersistedOutbox strictly decodes a persisted outbox blob, rejecting legacy formats,
// unknown fields, corrupt bytes, or unsupported schema versions.
func DecodePersistedOutbox(raw []byte) (*corev1.PersistedOutboxRecord, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var rec corev1.PersistedOutboxRecord
	if err := rec.Unmarshal(raw); err != nil {
		return nil, false
	}
	if rec.SchemaVersion != PersistedSchemaVersion || rec.SessionId == "" || rec.RequestId == "" {
		return nil, false
	}
	return &rec, true
}
