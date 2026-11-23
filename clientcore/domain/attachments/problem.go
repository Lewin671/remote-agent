package attachments

import (
	"strings"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

// Untranslated English codec/transport/daemon diagnostic keyword lists.
// Product failures are classified by explicit_kind, error_code, or legacy local_key first;
// never feed localized UI prose into raw_diagnostic.
const (
	rawDimensionsTokens   = "pixels|dimensions|canvas|too large"
	rawStorageFullTokens  = "quota"
	rawUploadFailedTokens = "network|offline|unavailable|fetch|timeout|deadline|expired"
	rawDecodeTokens       = "decode|png|jpeg|heic|webp|metadata|exif|corrupt|unsupported"
)

// ProblemMessage returns the localized user-facing message for a classified ImageProblemKind.
func ProblemMessage(kind corev1.ImageProblemKind) *corev1.Text {
	switch kind {
	case corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_ORIGINAL_TOO_LARGE:
		return text.Msg(text.KeyImageImageExceeds20MbTurnOffOriginalAndSend)
	case corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_DIMENSIONS:
		return text.Msg(text.KeyImageImageDimensionsAreTooLargeResizeItAndRetry)
	case corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_SOURCE_MISSING:
		return text.Msg(text.KeyImageThisImageHasNoLocalCopyRemoveItAnd)
	case corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_DECODE:
		return text.Msg(text.KeyImageCannotReadThisImageTryJpegOrPngInstead)
	case corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_STORAGE_FULL:
		return text.Msg(text.KeyImageTheComputerSAttachmentStorageIsFullClearSpace)
	case corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_UPLOAD_FAILED:
		return text.Msg(text.KeyImageTheImageCouldNotReachTheComputerRetryWhen)
	default:
		return text.Msg(text.KeyImageThisImageCouldNotBeProcessed)
	}
}

// ProblemRetryable reports whether a failed image with kind can be retried in place.
func ProblemRetryable(kind corev1.ImageProblemKind) bool {
	return kind == corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_STORAGE_FULL ||
		kind == corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_UPLOAD_FAILED
}

// BuildImageProblem constructs the ImageProblemView for kind.
func BuildImageProblem(kind corev1.ImageProblemKind) *corev1.ImageProblemView {
	if kind == corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_UNSPECIFIED {
		kind = corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_PROCESSING_FAILED
	}
	return &corev1.ImageProblemView{
		Kind:      kind,
		Message:   ProblemMessage(kind),
		Retryable: ProblemRetryable(kind),
	}
}

// ClassifyImageProblem classifies a structured shell error signal into a unified ImageProblemView.
// Precedence: explicit_kind > error_code > legacy local_key > raw_diagnostic > is_reading_source fallback.
func ClassifyImageProblem(in *corev1.ImageProblemInput) *corev1.ImageProblemView {
	if in == nil {
		return BuildImageProblem(corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_PROCESSING_FAILED)
	}
	if in.ExplicitKind != corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_UNSPECIFIED {
		return BuildImageProblem(in.ExplicitKind)
	}
	if k := classifyErrorCode(in.ErrorCode, in.RawDiagnostic); k != corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_UNSPECIFIED {
		return BuildImageProblem(k)
	}
	if k := classifyRawDiagnostic(in.RawDiagnostic); k != corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_UNSPECIFIED {
		return BuildImageProblem(k)
	}
	if in.IsReadingSource {
		return BuildImageProblem(corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_DECODE)
	}
	return BuildImageProblem(corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_PROCESSING_FAILED)
}

// SummarizeImageFailures returns the single summary line shown above the composer for failed images:
// empty (nil) when there are no problems; the shared problem message when every problem has the
// same kind; or image.attach_failed(count) when problems span multiple kinds.
func SummarizeImageFailures(problems []*corev1.ImageProblemView) *corev1.Text {
	if len(problems) == 0 {
		return nil
	}
	firstKind := problems[0].GetKind()
	allSame := true
	for _, p := range problems[1:] {
		if p.GetKind() != firstKind {
			allSame = false
			break
		}
	}
	if allSame {
		if problems[0].GetMessage() != nil {
			return problems[0].GetMessage()
		}
		return ProblemMessage(firstKind)
	}
	return text.Msg(text.KeyImageAttachFailed, text.Int("count", int64(len(problems))))
}

func classifyStableToken(token string) corev1.ImageProblemKind {
	switch token {
	case "original_too_large":
		return corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_ORIGINAL_TOO_LARGE
	case "dimensions":
		return corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_DIMENSIONS
	case "source_missing":
		return corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_SOURCE_MISSING
	case "decode":
		return corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_DECODE
	case "storage_full":
		return corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_STORAGE_FULL
	case "upload_failed":
		return corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_UPLOAD_FAILED
	case "processing_failed":
		return corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_PROCESSING_FAILED
	default:
		return corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_UNSPECIFIED
	}
}

func classifyErrorCode(code, rawDiagnostic string) corev1.ImageProblemKind {
	c := strings.ToLower(strings.TrimSpace(code))
	if c == "" {
		return corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_UNSPECIFIED
	}
	switch c {
	case "resource_exhausted", "enospc", "out_of_space":
		return corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_STORAGE_FULL
	case "network", "unavailable", "deadline_exceeded", "expired", "unknown", "unknown_outcome", "not_found", "generation_conflict", "io_error", "url_error", "timeout":
		return corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_UPLOAD_FAILED
	case "invalid_argument":
		if strings.Contains(strings.ToLower(rawDiagnostic), "hash") {
			return corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_SOURCE_MISSING
		}
		return corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_DECODE
	case "data_loss":
		return corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_DECODE
	case "file_not_found", "no_such_file":
		return corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_SOURCE_MISSING
	case "permission_denied", "unauthenticated", "already_exists":
		return corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_PROCESSING_FAILED
	default:
		return corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_UNSPECIFIED
	}
}

func classifyRawDiagnostic(raw string) corev1.ImageProblemKind {
	s := strings.TrimSpace(raw)
	if s == "" {
		return corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_UNSPECIFIED
	}
	lower := strings.ToLower(s)
	if stable := classifyStableToken(lower); stable != corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_UNSPECIFIED {
		return stable
	}
	if has20MB(lower) {
		return corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_ORIGINAL_TOO_LARGE
	}
	if containsAnyToken(lower, rawDimensionsTokens) {
		return corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_DIMENSIONS
	}
	if containsOrderedPair(lower, "source", "missing") ||
		containsOrderedPair(lower, "source", "read") {
		return corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_SOURCE_MISSING
	}
	// Check storage and network failures before format/decode tokens so messages like
	// "upload png: network timeout" remain retryable UPLOAD_FAILED instead of DECODE.
	if containsAnyToken(lower, rawStorageFullTokens) ||
		containsOrderedPair(lower, "storage", "full") ||
		containsOrderedPair(lower, "disk", "full") {
		return corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_STORAGE_FULL
	}
	if containsAnyToken(lower, rawUploadFailedTokens) {
		return corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_UPLOAD_FAILED
	}
	if containsAnyToken(lower, rawDecodeTokens) {
		return corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_DECODE
	}
	return corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_UNSPECIFIED
}

func containsAnyToken(haystack, pipeList string) bool {
	for pipeList != "" {
		var tok string
		if idx := strings.IndexByte(pipeList, '|'); idx >= 0 {
			tok = pipeList[:idx]
			pipeList = pipeList[idx+1:]
		} else {
			tok = pipeList
			pipeList = ""
		}
		if tok != "" && strings.Contains(haystack, tok) {
			return true
		}
	}
	return false
}

func containsOrderedPair(haystack, first, second string) bool {
	idx := strings.Index(haystack, first)
	if idx < 0 {
		return false
	}
	return strings.Contains(haystack[idx+len(first):], second)
}

// has20MB matches /20\s*(mb|mib)/ on a lowercased string without importing regexp.
func has20MB(lower string) bool {
	rest := lower
	for {
		idx := strings.Index(rest, "20")
		if idx < 0 {
			return false
		}
		after := rest[idx+2:]
		i := 0
		for i < len(after) && (after[i] == ' ' || after[i] == '\t' || after[i] == '\n' || after[i] == '\r') {
			i++
		}
		tail := after[i:]
		if strings.HasPrefix(tail, "mb") || strings.HasPrefix(tail, "mib") {
			return true
		}
		rest = after
	}
}
