package attachments

import (
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

func Test3AGapFormatters(t *testing.T) {
	if got := FormatOriginalTotalSize(0); got != nil {
		t.Fatalf("expected nil for 0 bytes, got %+v", got)
	}
	if got := FormatOriginalTotalSize(-5); got != nil {
		t.Fatalf("expected nil for negative bytes, got %+v", got)
	}
	sz := FormatOriginalTotalSize(2560 * 1024)
	if sz == nil || sz.GetKey() != text.KeyImageTotalSize || len(sz.Args) != 1 || sz.Args[0].GetStrValue() != "2.5 MB" {
		t.Fatalf("FormatOriginalTotalSize(2.5 MiB) = %+v", sz)
	}
	szKB := FormatOriginalTotalSize(512 * 1024)
	if szKB == nil || szKB.Args[0].GetStrValue() != "512.0 KB" {
		t.Fatalf("FormatOriginalTotalSize(512 KiB) = %+v", szKB)
	}

	if got := DraftViewerPositionText(0, 0); got != nil {
		t.Fatalf("expected nil for total=0, got %+v", got)
	}
	posCases := []struct {
		index, total int32
		want         string
	}{
		{0, 3, "1 / 3"},
		{2, 3, "3 / 3"},
		{-1, 3, "1 / 3"},
		{9, 3, "3 / 3"},
		{0, 1, "1 / 1"},
	}
	for _, tc := range posCases {
		got := DraftViewerPositionText(tc.index, tc.total)
		if got == nil || got.GetLiteral() != tc.want {
			t.Errorf("DraftViewerPositionText(%d, %d) = %+v, want %q", tc.index, tc.total, got, tc.want)
		}
	}
}

func TestUploadChunkProgressAndRetryRules(t *testing.T) {
	if got := ResolveUploadChunkBytes(0, 0); got != DefaultUploadChunkBytes {
		t.Fatalf("ResolveUploadChunkBytes(0,0) = %d", got)
	}
	if got := ResolveUploadChunkBytes(0, 256*1024); got != 256*1024 {
		t.Fatalf("ResolveUploadChunkBytes(0, 256K) = %d", got)
	}
	if got := ResolveUploadChunkBytes(512*1024, 1024*1024); got != 512*1024 {
		t.Fatalf("ResolveUploadChunkBytes(512K, 1M) = %d", got)
	}
	if got := ResolveUploadChunkBytes(2*1024*1024, 2*1024*1024); got != DefaultUploadChunkBytes {
		t.Fatalf("ResolveUploadChunkBytes(2M, 2M) = %d, want clamped %d", got, DefaultUploadChunkBytes)
	}

	if !ValidateUploadProgress(0, 0) || !ValidateUploadProgress(50, 100) || !ValidateUploadProgress(100, 100) {
		t.Fatalf("expected valid progress")
	}
	if ValidateUploadProgress(-1, 100) || ValidateUploadProgress(101, 100) || ValidateUploadProgress(0, -1) {
		t.Fatalf("expected invalid progress rejected")
	}
	if !ValidatePutAdvance(0, 768*1024, 768*1024, 1024*1024) {
		t.Fatalf("expected valid advance")
	}
	if ValidatePutAdvance(768*1024, 256*1024, 768*1024, 1024*1024) {
		t.Fatalf("expected stalled persisted_bytes rejected")
	}
	if ValidatePutAdvance(768*1024, 256*1024, 512*1024, 1024*1024) {
		t.Fatalf("expected backward persisted_bytes rejected")
	}

	r, pct := UploadProgressRatio(500, 1000, false)
	if r != 0.5 || pct != 50 {
		t.Fatalf("UploadProgressRatio(500,1000,false) = (%v, %d)", r, pct)
	}
	r, pct = UploadProgressRatio(0, 0, true)
	if r != 1.0 || pct != 100 {
		t.Fatalf("UploadProgressRatio(0,0,true) = (%v, %d)", r, pct)
	}

	if UploadPollBackoffMs(0) != 250 || UploadPollBackoffMs(1) != 500 || UploadPollBackoffMs(2) != 1000 {
		t.Fatalf("unexpected poll backoff schedule")
	}

	for _, code := range []string{"unavailable", "network", "unknown", "unknown_outcome", "expired", "deadline_exceeded", "canceled", "unresolved_host"} {
		if !IsUploadTransientCode(code) {
			t.Errorf("expected %q transient", code)
		}
	}
	for _, code := range []string{"", "not_found", "invalid_argument", "resource_exhausted", "permission_denied", "already_exists"} {
		if IsUploadTransientCode(code) {
			t.Errorf("expected %q non-transient", code)
		}
	}

	if !ShouldRestartUpload(true, "not_found") || !ShouldRestartUpload(true, "already_exists") || !ShouldRestartUpload(true, "generation_conflict") {
		t.Fatalf("expected daemon not_found/already_exists/generation_conflict to trigger restart")
	}
	if ShouldRestartUpload(false, "not_found") || ShouldRestartUpload(true, "resource_exhausted") {
		t.Fatalf("non-daemon or resource_exhausted must not trigger restart")
	}
}

func TestErrorClassifiersAndStatusText(t *testing.T) {
	if p := ClassifyIOErrorToImageProblem(corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_STORAGE_FULL, "", "", false); p.Kind != corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_STORAGE_FULL || !p.Retryable {
		t.Fatalf("unexpected storage full image problem: %+v", p)
	}
	if p := ClassifyIOErrorToImageProblem(corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_SOURCE_CHANGED, "", "", false); p.Kind != corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_SOURCE_MISSING {
		t.Fatalf("unexpected source changed image problem: %+v", p)
	}
	if got := ClassifyIOErrorToFileText(corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_SOURCE_CHANGED, DefaultMaxFileBytes, "a.pdf"); got.GetKey() != text.KeyAttachmentFileChanged {
		t.Fatalf("file changed text = %+v", got)
	}
	if got := ClassifyIOErrorToFileText(corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_STORAGE_FULL, DefaultMaxFileBytes, "a.pdf"); got.GetKey() != text.KeyAttachmentLocalStorageFull {
		t.Fatalf("local storage full text = %+v", got)
	}
	if got := ClassifyDaemonFileUploadError(true, "resource_exhausted", nil); got.GetKey() != text.KeyAttachmentFileStorageFull {
		t.Fatalf("daemon storage full text = %+v", got)
	}
	if got := ClassifyDaemonFileUploadError(true, "invalid_argument", nil); got.GetKey() != text.KeyAttachmentFileVerifyFailed {
		t.Fatalf("daemon verify failed text = %+v", got)
	}
	if got := ClassifyDaemonFileUploadError(true, "permission_denied", nil); got.GetKey() != text.KeyAttachmentFileUploadUnauthorized {
		t.Fatalf("daemon unauthorized text = %+v", got)
	}
	if got := DraftItemStatusText(corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_FILE, corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_PAUSED, 40, nil, nil); got.GetKey() != text.KeyAttachmentUploadPaused {
		t.Fatalf("paused status = %+v", got)
	}
	if got := DraftItemStatusText(corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_FILE, corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_WAITING_RECONNECT, 40, nil, nil); got.GetKey() != text.KeyAttachmentUploadWaitingReconnect {
		t.Fatalf("waiting reconnect status = %+v", got)
	}
	if OutboxRestartedReason().GetKey() != text.KeyAttachmentOutboxRestartedUnconfirmed ||
		RestoreWaitReason().GetKey() != text.KeyAttachmentRestoreWait ||
		UploadProgressInvalidText().GetKey() != text.KeyAttachmentUploadProgressInvalid ||
		UploadIncompleteText().GetKey() != text.KeyAttachmentUploadIncomplete ||
		UploadStillVerifyingText().GetKey() != text.KeyErrorsTheComputerIsStillVerifyingAttachmentsRetryShortly ||
		UploadMadeNoProgressText().GetKey() != text.KeyErrorsUploadMadeNoProgress ||
		UploadMissingIDText().GetKey() != text.KeyErrorsTheComputerDidNotReturnAnAttachmentId {
		t.Fatalf("unexpected helper text keys")
	}
}

func TestPersistenceStrictSchemaAndScopeHelpers(t *testing.T) {
	scope := &corev1.DraftScope{Kind: corev1.DraftScopeKind_DRAFT_SCOPE_KIND_SESSION, Id: "s-1"}
	if got := ScopeStorageKey(scope); got != "session:s-1" {
		t.Fatalf("ScopeStorageKey = %q", got)
	}
	parsed, ok := ParseScopeStorageKey("new:/Users/me/repo")
	if !ok || parsed.Kind != corev1.DraftScopeKind_DRAFT_SCOPE_KIND_NEW_SESSION || parsed.Id != "/Users/me/repo" {
		t.Fatalf("ParseScopeStorageKey = (%+v, %v)", parsed, ok)
	}
	if OutboxStorageKey("s-1", "req-1") != "out:s-1:req-1" {
		t.Fatalf("unexpected OutboxStorageKey")
	}
	if MergePutBackText("  hello  \n", "world") != "  hello\nworld" {
		t.Fatalf("MergePutBackText = %q", MergePutBackText("  hello  \n", "world"))
	}
	if MergePutBackText("   ", "world") != "world" {
		t.Fatalf("MergePutBackText blank existing = %q", MergePutBackText("   ", "world"))
	}
	if AppendDraftText("", "clean up") != "clean up" {
		t.Fatalf("AppendDraftText empty = %q", AppendDraftText("", "clean up"))
	}
	if AppendDraftText("  keep this  \n", "clean up") != "  keep this\n\nclean up" {
		t.Fatalf("AppendDraftText merge = %q", AppendDraftText("  keep this  \n", "clean up"))
	}
	if AppendDraftText("already has clean up here", "clean up") != "already has clean up here" {
		t.Fatalf("AppendDraftText dedup = %q", AppendDraftText("already has clean up here", "clean up"))
	}
	if AppendDraftText("keep", "   ") != "keep" {
		t.Fatalf("AppendDraftText blank addition = %q", AppendDraftText("keep", "   "))
	}

	rawDraft, err := EncodePersistedDraft(&corev1.PersistedDraftRecord{
		Scope:    scope,
		Text:     "draft body",
		Original: true,
		Items: []*corev1.PersistedAttachmentItem{
			{ItemId: "i1", Kind: corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE, SourceRef: "src-1"},
		},
	})
	if err != nil {
		t.Fatalf("EncodePersistedDraft: %v", err)
	}
	decDraft, ok := DecodePersistedDraft(rawDraft)
	if !ok || decDraft.Text != "draft body" || !decDraft.Original || len(decDraft.Items) != 1 {
		t.Fatalf("DecodePersistedDraft = (%+v, %v)", decDraft, ok)
	}
	// Legacy JSON or wrong schema version must be rejected strictly without migration.
	if _, ok := DecodePersistedDraft([]byte(`{"text":"old web json","original":false,"sources":[]}`)); ok {
		t.Fatalf("expected legacy JSON draft rejected")
	}
	badVer, _ := (&corev1.PersistedDraftRecord{SchemaVersion: 2, Scope: scope}).Marshal()
	if _, ok := DecodePersistedDraft(badVer); ok {
		t.Fatalf("expected schema_version=2 draft rejected")
	}

	rawOutbox, err := EncodePersistedOutbox(&corev1.PersistedOutboxRecord{
		SessionId: "s-1",
		RequestId: "req-1",
		MachineId: "m-1",
		Text:      "send me",
		Mode:      v1.SendMode_SEND_MODE_QUEUE,
		ImageIds:  []string{"img-1"},
	})
	if err != nil {
		t.Fatalf("EncodePersistedOutbox: %v", err)
	}
	decOutbox, ok := DecodePersistedOutbox(rawOutbox)
	if !ok || decOutbox.RequestId != "req-1" || decOutbox.Mode != v1.SendMode_SEND_MODE_QUEUE {
		t.Fatalf("DecodePersistedOutbox = (%+v, %v)", decOutbox, ok)
	}
	if _, ok := DecodePersistedOutbox([]byte("old\ttext\tformat")); ok {
		t.Fatalf("expected legacy outbox format rejected")
	}
}
