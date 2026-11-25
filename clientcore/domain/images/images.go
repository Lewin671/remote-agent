// Package images holds shared chat image viewing rules, timeline viewer-image
// collection, and "[Image #N]" placeholder segmentation rules (R8).
package images

import (
	"math"
	"strconv"
	"strings"
	"unicode"

	"github.com/Lewin671/remote-agent/clientcore/domain/sharedfiles"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

const (
	defaultAspectRatio = 4.0 / 3.0
	minClampedAspect   = 0.4
	maxClampedAspect   = 2.5
	placeholderPrefix  = "[Image #"
)

type placeholderMatch struct {
	start int
	end   int
	index int32
}

// SplitImagePlaceholders segments a user message's text around CLI "[Image #N]"
// placeholders matching known image refs (cli_index > 0):
//   - Only indices present in refs count; unknown placeholders like "[Image #9]" stay literal text.
//   - When all known placeholders appear only at the start before any non-whitespace text,
//     they are dropped and leading whitespace is trimmed (the numbered thumbnails above the
//     bubble stand for them).
//   - When any known placeholder appears after non-whitespace text, EVERY known placeholder
//     (including leading ones) is emitted as an inline image segment with image.placeholder
//     ("图 N" / "Image N") so references like "[Image #1] on desktop, [Image #2] on phone"
//     keep their meaning.
func SplitImagePlaceholders(msg string, refs []*v1.ImageRef) []*corev1.SessionMessageTextSegment {
	if msg == "" {
		return nil
	}
	known := make(map[int32]bool, len(refs))
	for _, r := range refs {
		if r != nil && r.CliIndex > 0 {
			known[r.CliIndex] = true
		}
	}
	if len(known) == 0 {
		return []*corev1.SessionMessageTextSegment{textSegment(msg)}
	}

	matches := findKnownPlaceholders(msg, known)
	if len(matches) == 0 {
		return []*corev1.SessionMessageTextSegment{textSegment(msg)}
	}

	// Find the end of the leading known-placeholder prefix (interleaved only with whitespace).
	leadEnd := 0
	leadCount := 0
	for _, m := range matches {
		if strings.TrimFunc(msg[leadEnd:m.start], unicode.IsSpace) != "" {
			break
		}
		leadEnd = m.end
		leadCount++
	}
	hasLaterRef := leadCount < len(matches)
	if !hasLaterRef {
		rest := strings.TrimLeftFunc(msg[leadEnd:], unicode.IsSpace)
		if rest == "" {
			return nil
		}
		return []*corev1.SessionMessageTextSegment{textSegment(rest)}
	}

	out := make([]*corev1.SessionMessageTextSegment, 0, len(matches)*2+1)
	cursor := 0
	for _, m := range matches {
		if m.start > cursor {
			out = append(out, textSegment(msg[cursor:m.start]))
		}
		out = append(out, &corev1.SessionMessageTextSegment{
			Kind: &corev1.SessionMessageTextSegment_ImageIndex{ImageIndex: m.index},
			ImageLabel: text.Msg(
				text.KeyImagePlaceholder,
				text.Int("index", int64(m.index)),
			),
		})
		cursor = m.end
	}
	if cursor < len(msg) {
		out = append(out, textSegment(msg[cursor:]))
	}
	return out
}

func textSegment(s string) *corev1.SessionMessageTextSegment {
	return &corev1.SessionMessageTextSegment{
		Kind: &corev1.SessionMessageTextSegment_Text{Text: s},
	}
}

func findKnownPlaceholders(s string, known map[int32]bool) []placeholderMatch {
	var out []placeholderMatch
	pos := 0
	for pos < len(s) {
		rel := strings.Index(s[pos:], placeholderPrefix)
		if rel < 0 {
			break
		}
		start := pos + rel
		numStart := start + len(placeholderPrefix)
		end := numStart
		var n int64
		valid := true
		for end < len(s) && s[end] >= '0' && s[end] <= '9' {
			if n <= math.MaxInt32 {
				n = n*10 + int64(s[end]-'0')
			}
			end++
		}
		if end == numStart || end >= len(s) || s[end] != ']' || n <= 0 || n > math.MaxInt32 {
			valid = false
		}
		if valid && known[int32(n)] {
			out = append(out, placeholderMatch{
				start: start,
				end:   end + 1,
				index: int32(n),
			})
			pos = end + 1
		} else {
			pos = start + len(placeholderPrefix)
		}
	}
	return out
}

// AspectRatio returns width/height prioritizing preview, display, then recorded dimensions,
// falling back to 4/3 when dimensions are unavailable.
func AspectRatio(ref *v1.ImageRef) float64 {
	if ref == nil {
		return defaultAspectRatio
	}
	pairs := [3][2]int32{
		{ref.PreviewWidth, ref.PreviewHeight},
		{ref.DisplayWidth, ref.DisplayHeight},
		{ref.Width, ref.Height},
	}
	for _, p := range pairs {
		if p[0] > 0 && p[1] > 0 {
			return float64(p[0]) / float64(p[1])
		}
	}
	return defaultAspectRatio
}

// ClampedAspectRatio returns AspectRatio clamped to [0.4, 2.5] for thumbnail strip layout.
func ClampedAspectRatio(ref *v1.ImageRef) float64 {
	a := AspectRatio(ref)
	if a < minClampedAspect {
		return minClampedAspect
	}
	if a > maxClampedAspect {
		return maxClampedAspect
	}
	return a
}

// SizeText formats the image's pixel dimensions ("W × H"), preferring original dimensions
// before CLI downscaling when both are positive; returns "" when unknown.
func SizeText(ref *v1.ImageRef) string {
	if ref == nil {
		return ""
	}
	w, h := ref.OriginalWidth, ref.OriginalHeight
	if w <= 0 || h <= 0 {
		w, h = ref.Width, ref.Height
	}
	if w <= 0 || h <= 0 {
		return ""
	}
	return strconv.Itoa(int(w)) + " × " + strconv.Itoa(int(h))
}

// FormatExtension returns the file extension for an ImageFormat (defaults to "jpg").
func FormatExtension(f v1.ImageFormat) string {
	switch f {
	case v1.ImageFormat_IMAGE_FORMAT_PNG:
		return "png"
	case v1.ImageFormat_IMAGE_FORMAT_WEBP:
		return "webp"
	case v1.ImageFormat_IMAGE_FORMAT_GIF:
		return "gif"
	default:
		return "jpg"
	}
}

// FormatMime returns the MIME type for an ImageFormat (defaults to "image/jpeg").
func FormatMime(f v1.ImageFormat) string {
	switch f {
	case v1.ImageFormat_IMAGE_FORMAT_PNG:
		return "image/png"
	case v1.ImageFormat_IMAGE_FORMAT_WEBP:
		return "image/webp"
	case v1.ImageFormat_IMAGE_FORMAT_GIF:
		return "image/gif"
	default:
		return "image/jpeg"
	}
}

// HasDisplayCopy reports whether the daemon uploaded a fetchable display copy for ref.
func HasDisplayCopy(ref *v1.ImageRef) bool {
	return ref != nil && ref.Sync == v1.ImageSync_IMAGE_SYNC_SYNCED && ref.ImageId != ""
}

// ImageSyncState converts a wire ImageSync enum into the core-owned SessionImageSyncState.
func ImageSyncState(s v1.ImageSync) corev1.SessionImageSyncState {
	switch s {
	case v1.ImageSync_IMAGE_SYNC_SYNCED:
		return corev1.SessionImageSyncState_SESSION_IMAGE_SYNC_STATE_SYNCED
	case v1.ImageSync_IMAGE_SYNC_PREVIEW_ONLY:
		return corev1.SessionImageSyncState_SESSION_IMAGE_SYNC_STATE_PREVIEW_ONLY
	case v1.ImageSync_IMAGE_SYNC_NAME_ONLY:
		return corev1.SessionImageSyncState_SESSION_IMAGE_SYNC_STATE_NAME_ONLY
	default:
		return corev1.SessionImageSyncState_SESSION_IMAGE_SYNC_STATE_UNSPECIFIED
	}
}

// BuildImageView projects a wire ImageRef into the core-owned SessionImageView.
func BuildImageView(ref *v1.ImageRef) *corev1.SessionImageView {
	if ref == nil {
		return nil
	}
	var preview []byte
	if len(ref.Preview) > 0 {
		preview = make([]byte, len(ref.Preview))
		copy(preview, ref.Preview)
	}
	return &corev1.SessionImageView{
		ImageId:            ref.ImageId,
		SyncState:          ImageSyncState(ref.Sync),
		CliIndex:           ref.CliIndex,
		Width:              ref.Width,
		Height:             ref.Height,
		Preview:            preview,
		PreviewFormat:      ref.PreviewFormat,
		DisplayName:        DisplayName(ref.Path),
		HasDisplayCopy:     HasDisplayCopy(ref),
		AspectRatio:        AspectRatio(ref),
		ClampedAspectRatio: ClampedAspectRatio(ref),
		SizeText:           SizeText(ref),
	}
}

// BuildImageViews converts a slice of wire ImageRefs into core-owned SessionImageViews.
func BuildImageViews(refs []*v1.ImageRef) []*corev1.SessionImageView {
	if len(refs) == 0 {
		return nil
	}
	out := make([]*corev1.SessionImageView, 0, len(refs))
	for _, ref := range refs {
		if v := BuildImageView(ref); v != nil {
			out = append(out, v)
		}
	}
	return out
}

// SourceLabel returns the localized caption describing where an image came from.
func SourceLabel(kind corev1.SessionImageSourceKind, agentName string) *corev1.Text {
	switch kind {
	case corev1.SessionImageSourceKind_SESSION_IMAGE_SOURCE_KIND_TOOL:
		return text.Msg(text.KeyImageToolSource, text.Str("agent", agentName))
	case corev1.SessionImageSourceKind_SESSION_IMAGE_SOURCE_KIND_GENERATED:
		return text.Msg(text.KeyImageGeneratedBy, text.Str("agent", agentName))
	default:
		return text.Msg(text.KeyImageUserSource)
	}
}

// DisplayName extracts and sanitizes the filename portion of an image's recorded path,
// or "" if no path was recorded.
func DisplayName(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	return sharedfiles.SanitizeFileName(trimmed)
}

// DownloadName returns a guaranteed non-empty sanitized filename for saving/downloading/sharing
// a session viewer image: uses displayName (appending "."+ext if it has no extension) when non-empty,
// otherwise falls back to "image-<N>.<ext>" using 1-based viewerIndex.
func DownloadName(displayName, ext string, viewerIndex int32) string {
	cleanExt := strings.TrimPrefix(strings.TrimSpace(ext), ".")
	if cleanExt == "" {
		cleanExt = "jpg"
	}
	name := strings.TrimSpace(displayName)
	if name != "" {
		name = sharedfiles.SanitizeFileName(name)
		if dot := strings.LastIndexByte(name, '.'); dot > 0 && dot < len(name)-1 {
			return name
		}
		return name + "." + cleanExt
	}
	idx := viewerIndex
	if idx < 0 {
		idx = 0
	}
	return "image-" + strconv.Itoa(int(idx)+1) + "." + cleanExt
}

func stripPreview(ref *v1.ImageRef) *v1.ImageRef {
	if ref == nil {
		return nil
	}
	return &v1.ImageRef{
		ImageId:        ref.ImageId,
		Source:         ref.Source,
		Sync:           ref.Sync,
		CliIndex:       ref.CliIndex,
		Path:           ref.Path,
		Format:         ref.Format,
		Width:          ref.Width,
		Height:         ref.Height,
		Bytes:          ref.Bytes,
		OriginalWidth:  ref.OriginalWidth,
		OriginalHeight: ref.OriginalHeight,
		DisplayWidth:   ref.DisplayWidth,
		DisplayHeight:  ref.DisplayHeight,
		Preview:        nil,
		PreviewFormat:  ref.PreviewFormat,
		PreviewWidth:   ref.PreviewWidth,
		PreviewHeight:  ref.PreviewHeight,
		UploadId:       ref.UploadId,
	}
}

// CollectViewerImages walks timeline items in chronological order, including items inside
// collapsed quiet scheduled groups (marking them with in_collapsed_group = true) so opening
// the viewer from any image or paging across a session is consistent across all three shells.
// The returned Ref strips embedded 320px preview bytes because the viewer fetches full images by image_id.
func CollectViewerImages(
	items []*corev1.SessionTimelineItem,
	agentName, machineHome string,
) []*corev1.SessionViewerImageView {
	var out []*corev1.SessionViewerImageView
	appendRef := func(
		seq uint64,
		evIdx int32,
		ts int64,
		kind corev1.SessionImageSourceKind,
		ref *v1.ImageRef,
		toolTitle, revisedPrompt string,
		inCollapsed bool,
	) {
		if ref == nil {
			return
		}
		dispPath := ""
		if strings.TrimSpace(ref.Path) != "" {
			dispPath = sharedfiles.DisplaySourcePath(ref.Path, machineHome, "")
		}
		dispName := DisplayName(ref.Path)
		ext := FormatExtension(ref.Format)
		viewerIdx := int32(len(out))
		out = append(out, &corev1.SessionViewerImageView{
			ViewerIndex:        viewerIdx,
			Seq:                seq,
			EventImageIndex:    evIdx,
			TimestampMs:        ts,
			Kind:               kind,
			Ref:                stripPreview(ref),
			ToolTitle:          toolTitle,
			RevisedPrompt:      revisedPrompt,
			SourceLabel:        SourceLabel(kind, agentName),
			SizeText:           SizeText(ref),
			AspectRatio:        AspectRatio(ref),
			ClampedAspectRatio: ClampedAspectRatio(ref),
			HasDisplayCopy:     HasDisplayCopy(ref),
			DisplayName:        dispName,
			DisplayPath:        dispPath,
			FileExtension:      ext,
			MimeType:           FormatMime(ref.Format),
			InCollapsedGroup:   inCollapsed,
			DownloadName:       DownloadName(dispName, ext, viewerIdx),
			ImageId:            ref.ImageId,
			SyncState:          ImageSyncState(ref.Sync),
			Width:              ref.Width,
			Height:             ref.Height,
			PreviewFormat:      ref.PreviewFormat,
		})
	}

	var visit func(list []*corev1.SessionTimelineItem, inCollapsed bool)
	visit = func(list []*corev1.SessionTimelineItem, inCollapsed bool) {
		for _, item := range list {
			if item == nil {
				continue
			}
			switch k := item.Kind.(type) {
			case *corev1.SessionTimelineItem_UserMessage:
				if k.UserMessage == nil {
					continue
				}
				for i, ref := range k.UserMessage.Images {
					src := corev1.SessionImageSourceKind_SESSION_IMAGE_SOURCE_KIND_USER
					if ref != nil {
						switch ref.Source {
						case v1.ImageSource_IMAGE_SOURCE_TOOL:
							src = corev1.SessionImageSourceKind_SESSION_IMAGE_SOURCE_KIND_TOOL
						case v1.ImageSource_IMAGE_SOURCE_GENERATED:
							src = corev1.SessionImageSourceKind_SESSION_IMAGE_SOURCE_KIND_GENERATED
						}
					}
					appendRef(item.Seq, int32(i), item.TimestampMs, src, ref, "", "", inCollapsed)
				}
			case *corev1.SessionTimelineItem_ToolCall:
				if k.ToolCall == nil {
					continue
				}
				seq := k.ToolCall.Seq
				if seq == 0 {
					seq = item.Seq
				}
				for i, ref := range k.ToolCall.Images {
					appendRef(seq, int32(i), item.TimestampMs, corev1.SessionImageSourceKind_SESSION_IMAGE_SOURCE_KIND_TOOL, ref, k.ToolCall.Title, "", inCollapsed)
				}
			case *corev1.SessionTimelineItem_ToolGroup:
				if k.ToolGroup == nil {
					continue
				}
				for _, call := range k.ToolGroup.Calls {
					if call == nil {
						continue
					}
					seq := call.Seq
					if seq == 0 {
						seq = item.Seq
					}
					for i, ref := range call.Images {
						appendRef(seq, int32(i), item.TimestampMs, corev1.SessionImageSourceKind_SESSION_IMAGE_SOURCE_KIND_TOOL, ref, call.Title, "", inCollapsed)
					}
				}
			case *corev1.SessionTimelineItem_GeneratedImages:
				if k.GeneratedImages == nil {
					continue
				}
				for i, ref := range k.GeneratedImages.Images {
					appendRef(item.Seq, int32(i), item.TimestampMs, corev1.SessionImageSourceKind_SESSION_IMAGE_SOURCE_KIND_GENERATED, ref, "", k.GeneratedImages.RevisedPrompt, inCollapsed)
				}
			case *corev1.SessionTimelineItem_QuietScheduledGroup:
				if k.QuietScheduledGroup == nil {
					continue
				}
				visit(k.QuietScheduledGroup.Items, inCollapsed || !k.QuietScheduledGroup.Expanded)
			}
		}
	}
	visit(items, false)
	return out
}

// ViewerStartIndex resolves the starting index in viewerImages for (seq, eventImageIndex).
// If seq > 0 matches an image's seq, returns clamp(firstMatch + eventImageIndex, 0, len-1);
// if seq is not found or 0, falls back to clamp(fallbackIndex, 0, len-1).
func ViewerStartIndex(all []*corev1.SessionViewerImageView, seq uint64, eventImageIndex, fallbackIndex int32) int32 {
	if len(all) == 0 {
		return 0
	}
	n := fallbackIndex
	if seq > 0 {
		found := false
		for i, img := range all {
			if img != nil && img.Seq == seq {
				n = int32(i) + eventImageIndex
				found = true
				break
			}
		}
		if !found {
			n = 0
		}
	}
	return ClampViewerIndex(n, int32(len(all)))
}

// StepViewerIndex moves current by delta within [0, total-1].
func StepViewerIndex(current, delta, total int32) int32 {
	if total <= 0 {
		return 0
	}
	return ClampViewerIndex(current+delta, total)
}

// ClampViewerIndex clamps idx to [0, total-1] (or 0 when total <= 0).
func ClampViewerIndex(idx, total int32) int32 {
	if total <= 0 || idx < 0 {
		return 0
	}
	if idx >= total {
		return total - 1
	}
	return idx
}

// BuildViewerView builds the SessionImageViewerView for the current viewer state and images.
func BuildViewerView(
	open bool,
	useSeq bool,
	seq uint64,
	eventImageIndex int32,
	viewerIndex int32,
	all []*corev1.SessionViewerImageView,
	header *corev1.SessionHeaderView,
	timelineLoaded bool,
) *corev1.SessionImageViewerView {
	total := int32(len(all))
	out := &corev1.SessionImageViewerView{
		Open:       open,
		TotalCount: total,
	}
	if total == 0 {
		if header != nil && !header.Found && header.Missing {
			if header.MissingText != nil {
				out.EmptyText = header.MissingText
			} else {
				out.EmptyText = text.Msg(text.KeyCommonCannotFindThisSession)
			}
		} else if timelineLoaded {
			out.EmptyText = text.Msg(text.KeyImageViewerEmpty)
		} else {
			out.EmptyText = text.Msg(text.KeyCommonLoading)
		}
		return out
	}

	var cur int32
	if useSeq {
		cur = ViewerStartIndex(all, seq, eventImageIndex, 0)
	} else {
		cur = ClampViewerIndex(viewerIndex, total)
	}
	out.CurrentIndex = cur
	out.HasPrev = cur > 0
	out.HasNext = cur+1 < total
	out.PositionText = text.Plain(strconv.Itoa(int(cur)+1) + " / " + strconv.Itoa(int(total)))
	out.CurrentImage = all[cur]
	return out
}
