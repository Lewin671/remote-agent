package attachments

import (
	"strings"
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

func TestLimitsClampingAndVideoHelpers(t *testing.T) {
	if MaxImages(nil) != 6 || MaxFiles(nil) != 6 || MaxFileBytes(nil) != DefaultMaxFileBytes {
		t.Fatalf("unexpected defaults for nil capabilities")
	}
	over := &v1.MachineCapabilities{
		MaxImagesPerMessage: 12,
		MaxFilesPerMessage:  20,
		MaxFileUploadBytes:  500 * 1024 * 1024,
	}
	if MaxImages(over) != 6 {
		t.Fatalf("MaxImages(over) = %d, want 6 (clamped per docs/session-images.md)", MaxImages(over))
	}
	if MaxFiles(over) != 6 {
		t.Fatalf("MaxFiles(over) = %d, want 6 (clamped)", MaxFiles(over))
	}
	if MaxFileBytes(over) != DefaultMaxFileBytes {
		t.Fatalf("MaxFileBytes(over) = %d, want %d (clamped)", MaxFileBytes(over), DefaultMaxFileBytes)
	}
	under := &v1.MachineCapabilities{
		MaxImagesPerMessage: 4,
		MaxFilesPerMessage:  3,
		MaxFileUploadBytes:  25 * 1024 * 1024,
	}
	if MaxImages(under) != 4 || MaxFiles(under) != 3 || MaxFileBytes(under) != 25*1024*1024 {
		t.Fatalf("unexpected under-clamp values: %d / %d / %d", MaxImages(under), MaxFiles(under), MaxFileBytes(under))
	}

	if GalleryAcceptMime(true) != "image/*,video/*" || GalleryAcceptMime(false) != "image/*" {
		t.Fatalf("unexpected GalleryAcceptMime")
	}
	if LibraryRoom(2, 6, 1, 6, true, true) != 9 {
		t.Fatalf("LibraryRoom = %d, want 9", LibraryRoom(2, 6, 1, 6, true, true))
	}
	if LibraryRoom(2, 6, 1, 6, false, true) != 5 {
		t.Fatalf("LibraryRoom(images blocked, videos allowed) = %d, want 5", LibraryRoom(2, 6, 1, 6, false, true))
	}
	if LibraryRoom(6, 6, 6, 6, true, true) != 0 {
		t.Fatalf("LibraryRoom(full) = %d, want 0", LibraryRoom(6, 6, 6, 6, true, true))
	}

	videoCases := []struct {
		mime, name string
		want       bool
	}{
		{"video/mp4", "clip.bin", true},
		{"VIDEO/QUICKTIME", "a.dat", true},
		{"image/jpeg", "fake.mp4", false},
		{"", "clip.MOV", true},
		{"", "clip.3g2", true},
		{"", "clip.mkv", true},
		{"", "photo.png", false},
		{"", "noext", false},
	}
	for _, tc := range videoCases {
		if got := IsVideo(tc.mime, tc.name); got != tc.want {
			t.Errorf("IsVideo(%q, %q) = %v, want %v", tc.mime, tc.name, got, tc.want)
		}
	}
}

func TestBlockedReasonsAndCandidateEvaluation(t *testing.T) {
	if r := ImageUploadBlockedReason(nil, "", false, false); r.GetKey() != text.KeyAttachmentChooseComputer {
		t.Fatalf("no machine image reason = %+v", r)
	}
	if r := FileUploadBlockedReason(nil, "", false, false); r.GetKey() != text.KeyAttachmentChooseComputer {
		t.Fatalf("no machine file reason = %+v", r)
	}
	lmNoMeta := &lists.Machine{ID: "m1"}
	if r := FileUploadBlockedReason(lmNoMeta, "m1", true, true); r.GetKey() != text.KeyAttachmentComputerUnreadFiles {
		t.Fatalf("unread meta file reason = %+v", r)
	}
	lmOldDaemon := &lists.Machine{ID: "m1", Meta: &v1.MachineMeta{Hostname: "Mac", Capabilities: &v1.MachineCapabilities{ImageUpload: true, FileUpload: false}}}
	if r := FileUploadBlockedReason(lmOldDaemon, "m1", true, true); r.GetKey() != text.KeyAttachmentUpdateRequired {
		t.Fatalf("unsupported file reason = %+v", r)
	}

	lmFull := &lists.Machine{
		ID: "m1",
		Meta: &v1.MachineMeta{
			Hostname: "Mac",
			Capabilities: &v1.MachineCapabilities{
				ImageUpload:         true,
				FileUpload:          true,
				MaxImagesPerMessage: 2,
				MaxFilesPerMessage:  2,
				MaxFileUploadBytes:  50 * 1024 * 1024,
			},
		},
	}
	caps := ResolveCaps(lmFull, "m1", true, true)
	if caps.MaxFileSizeText != "50.0 MB" {
		t.Fatalf("MaxFileSizeText = %q, want 50.0 MB", caps.MaxFileSizeText)
	}

	longName := strings.Repeat("a", 256) + ".txt"
	// Non-gallery (drag-and-drop / paste) images route as IMAGE; explicit force_file routes PNG as FILE.
	decisions, notice := EvaluateCandidates(caps, 1, 1, []*corev1.AttachmentCandidateInput{
		{Name: "pasted.png", MimeType: "image/png", SizeBytes: 1000},
		{Name: "dropped.JPG", MimeType: "", SizeBytes: 1000},
		{Name: "big.mov", MimeType: "", SizeBytes: 60 * 1024 * 1024},
		{Name: "clip.mp4", MimeType: "video/mp4", SizeBytes: 5 * 1024 * 1024},
		{Name: "extra.pdf", MimeType: "application/pdf", SizeBytes: 1024},
		{Name: "folder", IsDirectory: true},
		{Name: longName, SizeBytes: 10},
		{Name: "unknown.bin", SizeUnknown: true},
	})

	if len(decisions) != 8 {
		t.Fatalf("len(decisions) = %d, want 8", len(decisions))
	}
	if !decisions[0].Accepted || decisions[0].Route != corev1.AttachmentCandidateRoute_ATTACHMENT_CANDIDATE_ROUTE_IMAGE {
		t.Fatalf("d[0] = %+v", decisions[0])
	}
	if decisions[1].Accepted || decisions[1].RejectKind != corev1.AttachmentRejectKind_ATTACHMENT_REJECT_KIND_IMAGE_LIMIT {
		t.Fatalf("d[1] = %+v", decisions[1])
	}
	if decisions[2].Accepted || !decisions[2].IsVideo || decisions[2].RejectKind != corev1.AttachmentRejectKind_ATTACHMENT_REJECT_KIND_FILE_TOO_LARGE || decisions[2].RejectReason.GetKey() != text.KeyAttachmentNamedSizeLimit {
		t.Fatalf("d[2] = %+v", decisions[2])
	}
	if !decisions[3].Accepted || !decisions[3].IsVideo || decisions[3].Route != corev1.AttachmentCandidateRoute_ATTACHMENT_CANDIDATE_ROUTE_VIDEO_FILE {
		t.Fatalf("d[3] = %+v", decisions[3])
	}
	if decisions[4].Accepted || decisions[4].RejectKind != corev1.AttachmentRejectKind_ATTACHMENT_REJECT_KIND_FILE_LIMIT {
		t.Fatalf("d[4] = %+v", decisions[4])
	}
	if decisions[5].Accepted || decisions[5].RejectKind != corev1.AttachmentRejectKind_ATTACHMENT_REJECT_KIND_DIRECTORY {
		t.Fatalf("d[5] = %+v", decisions[5])
	}
	// After fileCount reached 2, subsequent file candidates hit FILE_LIMIT before filename/size checks.
	if decisions[6].RejectKind != corev1.AttachmentRejectKind_ATTACHMENT_REJECT_KIND_FILE_LIMIT {
		t.Fatalf("d[6] = %+v", decisions[6])
	}
	if notice == nil || notice.GetSeparatorKey() != text.KeyCommonNoteSeparator || len(notice.GetJoin()) != 4 {
		t.Fatalf("notice = %+v, want joined notice of 4 items", notice)
	}

	// Verify explicit file picker (force_file=true) sends PNG as FILE unchanged, plus filename & unknown size checks.
	d2, _ := EvaluateCandidates(caps, 0, 0, []*corev1.AttachmentCandidateInput{
		{Name: "raw.png", MimeType: "image/png", SizeBytes: 2048, ForceFile: true},
		{Name: longName, SizeBytes: 10, ForceFile: true},
		{Name: "unknown.bin", SizeUnknown: true, ForceFile: true},
	})
	if !d2[0].Accepted || d2[0].Route != corev1.AttachmentCandidateRoute_ATTACHMENT_CANDIDATE_ROUTE_FILE {
		t.Fatalf("d2[0] (force_file PNG) = %+v, want accepted FILE", d2[0])
	}
	if d2[1].RejectKind != corev1.AttachmentRejectKind_ATTACHMENT_REJECT_KIND_FILENAME_INVALID {
		t.Fatalf("d2[1] = %+v", d2[1])
	}
	if d2[2].RejectKind != corev1.AttachmentRejectKind_ATTACHMENT_REJECT_KIND_SIZE_UNKNOWN || d2[2].RejectReason.GetKey() != text.KeyAttachmentSizeUnreadable {
		t.Fatalf("d2[2] = %+v", d2[2])
	}
}

func TestClassifyImageProblemAndSummary(t *testing.T) {
	cases := []struct {
		name      string
		in        *corev1.ImageProblemInput
		wantKind  corev1.ImageProblemKind
		wantKey   string
		retryable bool
	}{
		{
			name:     "explicit kind wins over raw",
			in:       &corev1.ImageProblemInput{ExplicitKind: corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_ORIGINAL_TOO_LARGE, RawDiagnostic: "network timeout"},
			wantKind: corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_ORIGINAL_TOO_LARGE,
			wantKey:  text.KeyImageImageExceeds20MbTurnOffOriginalAndSend,
		},
		{
			name:     "local key dot form",
			in:       &corev1.ImageProblemInput{LocalKey: "image.ios_image_has_no_dimensions"},
			wantKind: corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_DIMENSIONS,
			wantKey:  text.KeyImageImageDimensionsAreTooLargeResizeItAndRetry,
		},
		{
			name:     "local key underscore form",
			in:       &corev1.ImageProblemInput{LocalKey: "attachment_ios_cannot_read_the_source_file_remove_it_and_select"},
			wantKind: corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_SOURCE_MISSING,
			wantKey:  text.KeyImageThisImageHasNoLocalCopyRemoveItAnd,
		},
		{
			name:      "error code resource_exhausted",
			in:        &corev1.ImageProblemInput{ErrorCode: "resource_exhausted"},
			wantKind:  corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_STORAGE_FULL,
			wantKey:   text.KeyImageTheComputerSAttachmentStorageIsFullClearSpace,
			retryable: true,
		},
		{
			name:      "error code generation_conflict unified to retryable upload_failed",
			in:        &corev1.ImageProblemInput{ErrorCode: "generation_conflict"},
			wantKind:  corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_UPLOAD_FAILED,
			wantKey:   text.KeyImageTheImageCouldNotReachTheComputerRetryWhen,
			retryable: true,
		},
		{
			name:     "invalid_argument with hash mismatch -> source_missing",
			in:       &corev1.ImageProblemInput{ErrorCode: "invalid_argument", RawDiagnostic: "sha256 hash mismatch"},
			wantKind: corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_SOURCE_MISSING,
			wantKey:  text.KeyImageThisImageHasNoLocalCopyRemoveItAnd,
		},
		{
			name:     "invalid_argument without hash -> decode",
			in:       &corev1.ImageProblemInput{ErrorCode: "invalid_argument", RawDiagnostic: "bad header"},
			wantKind: corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_DECODE,
			wantKey:  text.KeyImageCannotReadThisImageTryJpegOrPngInstead,
		},
		{
			name:     "raw 20 MiB",
			in:       &corev1.ImageProblemInput{RawDiagnostic: "file exceeds 20  MiB limit"},
			wantKind: corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_ORIGINAL_TOO_LARGE,
			wantKey:  text.KeyImageImageExceeds20MbTurnOffOriginalAndSend,
		},
		{
			name:     "raw dimensions",
			in:       &corev1.ImageProblemInput{RawDiagnostic: "canvas dimensions exceeded"},
			wantKind: corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_DIMENSIONS,
			wantKey:  text.KeyImageImageDimensionsAreTooLargeResizeItAndRetry,
		},
		{
			name:     "raw source missing ordered pair",
			in:       &corev1.ImageProblemInput{RawDiagnostic: "source file is missing"},
			wantKind: corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_SOURCE_MISSING,
			wantKey:  text.KeyImageThisImageHasNoLocalCopyRemoveItAnd,
		},
		{
			name:     "raw decode",
			in:       &corev1.ImageProblemInput{RawDiagnostic: "corrupt HEIC metadata"},
			wantKind: corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_DECODE,
			wantKey:  text.KeyImageCannotReadThisImageTryJpegOrPngInstead,
		},
		{
			name:      "raw storage full",
			in:        &corev1.ImageProblemInput{RawDiagnostic: "disk is full"},
			wantKind:  corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_STORAGE_FULL,
			wantKey:   text.KeyImageTheComputerSAttachmentStorageIsFullClearSpace,
			retryable: true,
		},
		{
			name:      "raw network beats png format keyword",
			in:        &corev1.ImageProblemInput{RawDiagnostic: "upload png: network timeout"},
			wantKind:  corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_UPLOAD_FAILED,
			wantKey:   text.KeyImageTheImageCouldNotReachTheComputerRetryWhen,
			retryable: true,
		},
		{
			name:      "raw network",
			in:        &corev1.ImageProblemInput{RawDiagnostic: "fetch timeout"},
			wantKind:  corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_UPLOAD_FAILED,
			wantKey:   text.KeyImageTheImageCouldNotReachTheComputerRetryWhen,
			retryable: true,
		},
		{
			name:     "unknown while reading source falls back to decode",
			in:       &corev1.ImageProblemInput{RawDiagnostic: "vendor error 42", IsReadingSource: true},
			wantKind: corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_DECODE,
			wantKey:  text.KeyImageCannotReadThisImageTryJpegOrPngInstead,
		},
		{
			name:     "unknown otherwise falls back to processing_failed",
			in:       &corev1.ImageProblemInput{RawDiagnostic: "unexpected boom"},
			wantKind: corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_PROCESSING_FAILED,
			wantKey:  text.KeyImageThisImageCouldNotBeProcessed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyImageProblem(tc.in)
			if got.Kind != tc.wantKind || got.Message.GetKey() != tc.wantKey || got.Retryable != tc.retryable {
				t.Fatalf("got (%v, %q, retryable=%v), want (%v, %q, retryable=%v)",
					got.Kind, got.Message.GetKey(), got.Retryable, tc.wantKind, tc.wantKey, tc.retryable)
			}
		})
	}

	if SummarizeImageFailures(nil) != nil {
		t.Fatalf("empty summary should be nil")
	}
	p1 := BuildImageProblem(corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_UPLOAD_FAILED)
	p2 := BuildImageProblem(corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_UPLOAD_FAILED)
	p3 := BuildImageProblem(corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_DECODE)
	if same := SummarizeImageFailures([]*corev1.ImageProblemView{p1, p2}); same.GetKey() != text.KeyImageTheImageCouldNotReachTheComputerRetryWhen {
		t.Fatalf("same-kind summary = %+v", same)
	}
	if mixed := SummarizeImageFailures([]*corev1.ImageProblemView{p1, p3}); mixed.GetKey() != text.KeyImageAttachFailed || mixed.Args[0].GetIntValue() != 2 {
		t.Fatalf("mixed summary = %+v", mixed)
	}
}
