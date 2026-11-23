// Package attachments holds shared composer attachment rules (R7):
// computer capability clamping, file/gallery-video limits, picked candidate routing
// & validation, image error classification, and failure summary text.
package attachments

import (
	"strings"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/sharedfiles"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

const (
	// DefaultMaxImages is both the default and upper clamp (6) for per-message image count
	// per docs/session-images.md ("一条消息最多 6 张").
	DefaultMaxImages int32 = 6
	// DefaultMaxFiles is both the default and upper clamp for per-message file count
	// (including gallery videos), unifying Web/Android/iOS.
	DefaultMaxFiles int32 = 6
	// DefaultMaxFileBytes is both the default and upper clamp (100 MiB) for a single
	// uploaded file (including gallery videos), unifying Web/Android/iOS.
	DefaultMaxFileBytes int64 = 100 * 1024 * 1024
	// MaxFileNameUTF8Bytes is the maximum allowed UTF-8 byte length of an attachment filename.
	MaxFileNameUTF8Bytes int32 = 255
	// OriginalImageMaxBytes is the maximum size (20 MiB) when sending original-quality images.
	OriginalImageMaxBytes int64 = 20 * 1024 * 1024
)

// imageExtensions is the unified set of image extensions recognized when MIME is absent
// (e.g., OS drag-and-drop or clipboard entries without a declared content type).
var imageExtensions = map[string]bool{
	"png": true, "jpg": true, "jpeg": true, "gif": true, "webp": true,
	"heic": true, "heif": true, "bmp": true, "avif": true, "tif": true, "tiff": true,
}

// videoExtensions is the unified set of video extensions recognized when the OS or browser
// omits a MIME type for a picked media item (matches Android Files.kt VIDEO_EXTENSIONS).
var videoExtensions = map[string]bool{
	"mp4": true, "m4v": true, "mov": true, "3gp": true, "3g2": true,
	"webm": true, "mkv": true, "avi": true, "mpg": true, "mpeg": true,
	"ts": true, "wmv": true, "flv": true,
}

// MaxImages resolves the per-message image limit from daemon capabilities, clamped to <= 6
// per docs/session-images.md ("一条消息最多 6 张").
func MaxImages(mc *v1.MachineCapabilities) int32 {
	if mc == nil || mc.MaxImagesPerMessage <= 0 {
		return DefaultMaxImages
	}
	if mc.MaxImagesPerMessage > DefaultMaxImages {
		return DefaultMaxImages
	}
	return mc.MaxImagesPerMessage
}

// MaxFiles resolves the per-message file limit from daemon capabilities, clamped to <= 6.
func MaxFiles(mc *v1.MachineCapabilities) int32 {
	if mc == nil || mc.MaxFilesPerMessage <= 0 {
		return DefaultMaxFiles
	}
	if mc.MaxFilesPerMessage > DefaultMaxFiles {
		return DefaultMaxFiles
	}
	return mc.MaxFilesPerMessage
}

// MaxFileBytes resolves the per-file size limit from daemon capabilities, clamped to <= 100 MiB.
func MaxFileBytes(mc *v1.MachineCapabilities) int64 {
	if mc == nil || mc.MaxFileUploadBytes <= 0 {
		return DefaultMaxFileBytes
	}
	if mc.MaxFileUploadBytes > DefaultMaxFileBytes {
		return DefaultMaxFileBytes
	}
	return mc.MaxFileUploadBytes
}

// MaxFileMB returns the integer MiB display value for maxBytes (minimum 1 when positive).
func MaxFileMB(maxBytes int64) int64 {
	if maxBytes <= 0 {
		return DefaultMaxFileBytes / (1024 * 1024)
	}
	mb := (maxBytes + 512*1024) / (1024 * 1024)
	if mb < 1 {
		return 1
	}
	return mb
}

// GalleryAcceptMime returns the HTML file input accept string for the photo/video picker.
func GalleryAcceptMime(supportsFiles bool) string {
	if supportsFiles {
		return "image/*,video/*"
	}
	return "image/*"
}

// IsImage reports whether a candidate is an image based on its MIME type ("image/") or,
// when MIME is empty, its filename extension.
func IsImage(mimeType, name string) bool {
	m := strings.TrimSpace(mimeType)
	if m != "" {
		return strings.HasPrefix(strings.ToLower(m), "image/")
	}
	ext := fileExtensionLower(name)
	return ext != "" && imageExtensions[ext]
}

// IsVideo reports whether a picked candidate should be treated as a video (routed as a file).
// When mimeType is non-empty, it checks the "video/" prefix; otherwise it falls back to
// the unified 13-extension set on the filename.
func IsVideo(mimeType, name string) bool {
	m := strings.TrimSpace(mimeType)
	if m != "" {
		return strings.HasPrefix(strings.ToLower(m), "video/")
	}
	ext := fileExtensionLower(name)
	return ext != "" && videoExtensions[ext]
}

func fileExtensionLower(name string) string {
	base := name
	if idx := strings.LastIndexAny(base, "/\\"); idx >= 0 {
		base = base[idx+1:]
	}
	dot := strings.LastIndexByte(base, '.')
	if dot <= 0 || dot == len(base)-1 {
		return ""
	}
	return strings.ToLower(base[dot+1:])
}

// LibraryRoom returns how many more items the photo/video gallery picker may request:
// remaining image slots (when images are allowed) plus remaining file slots (when videos/files are allowed).
func LibraryRoom(currentImages, maxImages, currentFiles, maxFiles int32, supportsImages, supportsFiles bool) int32 {
	var room int32
	if supportsImages && maxImages > currentImages {
		room += maxImages - currentImages
	}
	if supportsFiles && maxFiles > currentFiles {
		room += maxFiles - currentFiles
	}
	return room
}

// ImageUploadBlockedReason returns the localized reason images cannot currently be added,
// or nil when image upload is available.
func ImageUploadBlockedReason(lm *lists.Machine, machineID string, computerOnline, onlineReady bool) *corev1.Text {
	if lm == nil && machineID == "" {
		return text.Msg(text.KeyAttachmentChooseComputer)
	}
	name := lists.MachineName(lm, machineID)
	if lm == nil || !computerOnline {
		return text.Msg(text.KeyAttachmentComputerOffline, text.Str("name", name))
	}
	if !onlineReady {
		return text.Msg(text.KeyAttachmentDisconnected)
	}
	if lm.Meta == nil {
		return text.Msg(text.KeyAttachmentComputerUnread, text.Str("name", name))
	}
	if lm.Meta.Capabilities == nil || !lm.Meta.Capabilities.ImageUpload {
		return text.Msg(text.KeyAttachmentPathSpaces, text.Str("name", name))
	}
	return nil
}

// FileUploadBlockedReason returns the localized reason files (and gallery videos) cannot
// currently be added, or nil when file upload is available.
func FileUploadBlockedReason(lm *lists.Machine, machineID string, computerOnline, onlineReady bool) *corev1.Text {
	if lm == nil && machineID == "" {
		return text.Msg(text.KeyAttachmentChooseComputer)
	}
	name := lists.MachineName(lm, machineID)
	if lm == nil || !computerOnline {
		return text.Msg(text.KeyAttachmentComputerOfflineFiles, text.Str("name", name))
	}
	if !onlineReady {
		return text.Msg(text.KeyAttachmentDisconnectedFiles)
	}
	if lm.Meta == nil {
		return text.Msg(text.KeyAttachmentComputerUnreadFiles, text.Str("name", name))
	}
	if lm.Meta.Capabilities == nil || !lm.Meta.Capabilities.FileUpload {
		return text.Msg(text.KeyAttachmentUpdateRequired)
	}
	return nil
}

// ResolvedCaps bundles the computed capability limits and block reasons for one target machine.
type ResolvedCaps struct {
	CapabilitiesLoaded       bool
	SupportsImages           bool
	SupportsFiles            bool
	MaxImages                int32
	MaxFiles                 int32
	MaxFileBytes             int64
	MaxFileSizeText          string
	ImageUploadBlockedReason *corev1.Text
	FileUploadBlockedReason  *corev1.Text
}

// ResolveCaps computes the unified attachment capabilities for a target machine.
func ResolveCaps(lm *lists.Machine, machineID string, computerOnline, onlineReady bool) ResolvedCaps {
	var mc *v1.MachineCapabilities
	loaded := lm != nil && lm.Meta != nil
	if loaded {
		mc = lm.Meta.Capabilities
	}
	maxBytes := MaxFileBytes(mc)
	return ResolvedCaps{
		CapabilitiesLoaded:       loaded,
		SupportsImages:           mc != nil && mc.ImageUpload,
		SupportsFiles:            mc != nil && mc.FileUpload,
		MaxImages:                MaxImages(mc),
		MaxFiles:                 MaxFiles(mc),
		MaxFileBytes:             maxBytes,
		MaxFileSizeText:          sharedfiles.FormatFileSize(maxBytes),
		ImageUploadBlockedReason: ImageUploadBlockedReason(lm, machineID, computerOnline, onlineReady),
		FileUploadBlockedReason:  FileUploadBlockedReason(lm, machineID, computerOnline, onlineReady),
	}
}

// EvaluateCandidates routes and validates a batch of picked candidates against caps and current counts,
// returning per-candidate verdicts and a single localized notice (or nil when all are accepted).
// Per docs/session-files.md: images (from gallery, drag-and-drop, or paste) route as IMAGE unless
// force_file is set (explicit "文件" picker, where images are sent unchanged as FILE); all non-images
// (including videos) route as VIDEO_FILE / FILE.
func EvaluateCandidates(
	caps ResolvedCaps,
	currentImages, currentFiles int32,
	candidates []*corev1.AttachmentCandidateInput,
) ([]*corev1.AttachmentCandidateDecision, *corev1.Text) {
	if len(candidates) == 0 {
		return nil, nil
	}
	if currentImages < 0 {
		currentImages = 0
	}
	if currentFiles < 0 {
		currentFiles = 0
	}
	imagesAllowed := caps.SupportsImages && caps.ImageUploadBlockedReason == nil
	filesAllowed := caps.SupportsFiles && caps.FileUploadBlockedReason == nil

	decisions := make([]*corev1.AttachmentCandidateDecision, 0, len(candidates))
	var notices []*corev1.Text
	seenKeys := make(map[string]bool)
	addNotice := func(t *corev1.Text, dedup string) {
		if t == nil {
			return
		}
		if dedup != "" {
			if seenKeys[dedup] {
				return
			}
			seenKeys[dedup] = true
		}
		notices = append(notices, t)
	}

	var extraImages int32
	imgCount := currentImages
	fileCount := currentFiles
	maxMB := MaxFileMB(caps.MaxFileBytes)

	for _, c := range candidates {
		if c == nil {
			continue
		}
		isVid := IsVideo(c.MimeType, c.Name)
		displayName := sharedfiles.SanitizeFileName(c.Name)
		if strings.TrimSpace(c.Name) == "" {
			displayName = ""
		}
		var formattedSize string
		if !c.SizeUnknown && c.SizeBytes >= 0 {
			formattedSize = sharedfiles.FormatFileSize(c.SizeBytes)
		}

		// Per docs/session-files.md: images route as IMAGE (whether from gallery, drag-and-drop,
		// or paste) unless force_file is set (explicit "文件" picker). Non-images route as files.
		asImage := !c.ForceFile && !c.IsDirectory && IsImage(c.MimeType, c.Name)

		if asImage {
			if !imagesAllowed {
				reason := caps.ImageUploadBlockedReason
				if reason == nil {
					reason = text.Msg(text.KeyAttachmentPathSpaces, text.Str("name", ""))
				}
				decisions = append(decisions, &corev1.AttachmentCandidateDecision{
					Route:         corev1.AttachmentCandidateRoute_ATTACHMENT_CANDIDATE_ROUTE_REJECTED,
					Accepted:      false,
					RejectKind:    corev1.AttachmentRejectKind_ATTACHMENT_REJECT_KIND_IMAGES_UNSUPPORTED,
					RejectReason:  reason,
					DisplayName:   displayName,
					FormattedSize: formattedSize,
				})
				addNotice(reason, "images_unsupported")
				continue
			}
			if imgCount >= caps.MaxImages {
				extraImages++
				reason := text.Msg(text.KeyAttachmentMaxImages, text.Int("count", int64(caps.MaxImages)))
				decisions = append(decisions, &corev1.AttachmentCandidateDecision{
					Route:         corev1.AttachmentCandidateRoute_ATTACHMENT_CANDIDATE_ROUTE_REJECTED,
					Accepted:      false,
					RejectKind:    corev1.AttachmentRejectKind_ATTACHMENT_REJECT_KIND_IMAGE_LIMIT,
					RejectReason:  reason,
					DisplayName:   displayName,
					FormattedSize: formattedSize,
				})
				continue
			}
			imgCount++
			decisions = append(decisions, &corev1.AttachmentCandidateDecision{
				Route:         corev1.AttachmentCandidateRoute_ATTACHMENT_CANDIDATE_ROUTE_IMAGE,
				Accepted:      true,
				DisplayName:   displayName,
				FormattedSize: formattedSize,
			})
			continue
		}

		// File or gallery video path.
		if !filesAllowed {
			reason := caps.FileUploadBlockedReason
			if reason == nil {
				reason = text.Msg(text.KeyAttachmentUpdateRequired)
			}
			decisions = append(decisions, &corev1.AttachmentCandidateDecision{
				Route:         corev1.AttachmentCandidateRoute_ATTACHMENT_CANDIDATE_ROUTE_REJECTED,
				Accepted:      false,
				RejectKind:    corev1.AttachmentRejectKind_ATTACHMENT_REJECT_KIND_FILES_UNSUPPORTED,
				RejectReason:  reason,
				DisplayName:   displayName,
				FormattedSize: formattedSize,
				IsVideo:       isVid,
			})
			addNotice(reason, "files_unsupported")
			continue
		}
		if c.IsDirectory {
			reason := text.Msg(text.KeyAttachmentZipFolder)
			decisions = append(decisions, &corev1.AttachmentCandidateDecision{
				Route:         corev1.AttachmentCandidateRoute_ATTACHMENT_CANDIDATE_ROUTE_REJECTED,
				Accepted:      false,
				RejectKind:    corev1.AttachmentRejectKind_ATTACHMENT_REJECT_KIND_DIRECTORY,
				RejectReason:  reason,
				DisplayName:   displayName,
				FormattedSize: formattedSize,
				IsVideo:       isVid,
			})
			addNotice(reason, "directory")
			continue
		}
		if fileCount >= caps.MaxFiles {
			reason := text.Msg(text.KeyAttachmentMaxFiles, text.Int("count", int64(caps.MaxFiles)))
			decisions = append(decisions, &corev1.AttachmentCandidateDecision{
				Route:         corev1.AttachmentCandidateRoute_ATTACHMENT_CANDIDATE_ROUTE_REJECTED,
				Accepted:      false,
				RejectKind:    corev1.AttachmentRejectKind_ATTACHMENT_REJECT_KIND_FILE_LIMIT,
				RejectReason:  reason,
				DisplayName:   displayName,
				FormattedSize: formattedSize,
				IsVideo:       isVid,
			})
			addNotice(reason, "file_limit")
			continue
		}
		trimmedName := strings.TrimSpace(c.Name)
		if trimmedName == "" || len(trimmedName) > int(MaxFileNameUTF8Bytes) {
			reason := text.Msg(text.KeyAttachmentFilenameLimit)
			decisions = append(decisions, &corev1.AttachmentCandidateDecision{
				Route:         corev1.AttachmentCandidateRoute_ATTACHMENT_CANDIDATE_ROUTE_REJECTED,
				Accepted:      false,
				RejectKind:    corev1.AttachmentRejectKind_ATTACHMENT_REJECT_KIND_FILENAME_INVALID,
				RejectReason:  reason,
				DisplayName:   displayName,
				FormattedSize: formattedSize,
				IsVideo:       isVid,
			})
			addNotice(reason, "filename_invalid")
			continue
		}
		if c.SizeUnknown || c.SizeBytes < 0 {
			reason := text.Msg(text.KeyAttachmentSizeUnreadable)
			decisions = append(decisions, &corev1.AttachmentCandidateDecision{
				Route:         corev1.AttachmentCandidateRoute_ATTACHMENT_CANDIDATE_ROUTE_REJECTED,
				Accepted:      false,
				RejectKind:    corev1.AttachmentRejectKind_ATTACHMENT_REJECT_KIND_SIZE_UNKNOWN,
				RejectReason:  reason,
				DisplayName:   displayName,
				FormattedSize: formattedSize,
				IsVideo:       isVid,
			})
			addNotice(reason, "size_unknown")
			continue
		}
		if c.SizeBytes > caps.MaxFileBytes {
			var reason *corev1.Text
			if displayName != "" {
				reason = text.Msg(text.KeyAttachmentNamedSizeLimit, text.Str("name", displayName), text.Int("limit", maxMB))
			} else {
				reason = text.Msg(text.KeyAttachmentSizeLimit, text.Int("limit", maxMB))
			}
			decisions = append(decisions, &corev1.AttachmentCandidateDecision{
				Route:         corev1.AttachmentCandidateRoute_ATTACHMENT_CANDIDATE_ROUTE_REJECTED,
				Accepted:      false,
				RejectKind:    corev1.AttachmentRejectKind_ATTACHMENT_REJECT_KIND_FILE_TOO_LARGE,
				RejectReason:  reason,
				DisplayName:   displayName,
				FormattedSize: formattedSize,
				IsVideo:       isVid,
			})
			addNotice(reason, "file_too_large:"+displayName)
			continue
		}

		fileCount++
		route := corev1.AttachmentCandidateRoute_ATTACHMENT_CANDIDATE_ROUTE_FILE
		if isVid {
			route = corev1.AttachmentCandidateRoute_ATTACHMENT_CANDIDATE_ROUTE_VIDEO_FILE
		}
		decisions = append(decisions, &corev1.AttachmentCandidateDecision{
			Route:         route,
			Accepted:      true,
			DisplayName:   displayName,
			FormattedSize: formattedSize,
			IsVideo:       isVid,
		})
	}

	if extraImages > 0 {
		notices = append([]*corev1.Text{
			text.Msg(text.KeyAttachmentImagesLimit, text.Int("limit", int64(caps.MaxImages)), text.Int("count", int64(extraImages))),
		}, notices...)
	}

	var notice *corev1.Text
	if len(notices) == 1 {
		notice = notices[0]
	} else if len(notices) > 1 {
		notice = text.Join(text.KeyCommonNoteSeparator, notices...)
	}
	return decisions, notice
}
