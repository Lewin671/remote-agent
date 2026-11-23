// Package sharedfiles holds the shared display, classification, filename, size,
// status-priority, and menu rules for files exchanged in a session (R1).
package sharedfiles

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

const (
	MaxFileNameBytes     = 255
	MaxPreservedExtRunes = 16
	MaxSplitExtRunes     = 12
	SplitTailLeadRunes   = 3

	kibBytes = 1024
	mibBytes = 1024 * 1024
	gibBytes = 1024 * 1024 * 1024
)

// Classification describes a file's semantic kind and preview/album capabilities.
type Classification struct {
	Kind              corev1.SharedFileKind
	KindLabel         *corev1.Text
	SupportsAlbumSave bool
	HasMediaDuration  bool
	WebPreviewKind    corev1.SharedFileWebPreviewKind
	WebPreviewMime    string
}

// TransferStatus represents the core-owned transfer/cache lifecycle state of one shared file.
type TransferStatus uint8

const (
	TransferStatusUnspecified TransferStatus = iota
	TransferStatusDownloading
	TransferStatusPaused
	TransferStatusDownloaded
	TransferStatusFailed
	TransferStatusCleanedUp
)

// LocalState holds transfer progress, cache presence, and media metadata for one shared file.
type LocalState struct {
	Status           TransferStatus
	DownloadedBytes  int64
	HasEta           bool
	EtaSeconds       int64
	WaitingReconnect bool
	HasDuration      bool
	DurationMs       int64
	FailureKind      corev1.SharedFileFailureKind
	FailureDetail    string
	FailureText      *corev1.Text
}

// CardInput supplies all inputs required to build a display-ready SessionSharedFileItem.
type CardInput struct {
	File          *v1.SharedFile
	Local         *LocalState
	MachineName   string
	MachineHome   string
	Online        bool
	SessionExited bool
	UILocale      string
}

// SanitizeFileName takes the basename after the last '/' or '\', strips C0/DEL/C1
// control characters, Unicode Cf format characters (including bidi controls, U+200B, U+FEFF),
// and U+2028/U+2029 line/paragraph separators, trims whitespace, falls back to "file" when
// empty/./.., and caps length at 255 UTF-8 bytes on a rune boundary while preserving extensions
// up to 16 runes (including leading dot).
func SanitizeFileName(raw string) string {
	tail := raw
	if idx := strings.LastIndexAny(raw, "/\\"); idx >= 0 {
		tail = raw[idx+1:]
	}
	var b strings.Builder
	b.Grow(len(tail))
	for _, r := range tail {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			continue
		}
		if unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029 {
			continue
		}
		b.WriteRune(r)
	}
	name := strings.TrimFunc(b.String(), unicode.IsSpace)
	if name == "" || name == "." || name == ".." {
		return "file"
	}
	if len(name) <= MaxFileNameBytes {
		return name
	}
	dot := strings.LastIndexByte(name, '.')
	if dot > 0 {
		ext := name[dot:]
		if len(ext) > 1 && utf8.RuneCountInString(ext) <= MaxPreservedExtRunes && len(ext) < MaxFileNameBytes {
			head := truncateUTF8Bytes(name[:dot], MaxFileNameBytes-len(ext))
			if head != "" {
				return head + ext
			}
		}
	}
	return truncateUTF8Bytes(name, MaxFileNameBytes)
}

func truncateUTF8Bytes(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	end := 0
	for i := range s {
		if i > maxBytes {
			break
		}
		end = i
	}
	// Check if the rune starting at end also fits within maxBytes.
	if _, sz := utf8.DecodeRuneInString(s[end:]); end+sz <= maxBytes {
		end += sz
	}
	return s[:end]
}

// SplitMiddleTruncateName splits a filename into (head, tail) so middle ellipsis
// keeps the extension plus up to 3 leading stem runes intact.
// When there is no suitable extension (dot <= 0, trailing dot, or extLen > 12 runes),
// returns (name, "").
func SplitMiddleTruncateName(raw string) (head, tail string) {
	name := SanitizeFileName(raw)
	runes := []rune(name)
	n := len(runes)
	dot := lastRuneIndex(runes, '.')
	if dot <= 0 || dot >= n-1 || n-dot > MaxSplitExtRunes {
		return name, ""
	}
	keep := n - dot + SplitTailLeadRunes
	if keep >= n {
		keep = n - 1
	}
	return string(runes[:n-keep]), string(runes[n-keep:])
}

// ExtensionOf returns the lowercase extension (without dot) of a sanitized filename,
// or "" when none exists (including dotfiles like ".gitignore").
func ExtensionOf(name string) string {
	clean := SanitizeFileName(name)
	dot := strings.LastIndexByte(clean, '.')
	if dot <= 0 || dot == len(clean)-1 {
		return ""
	}
	return strings.ToLower(clean[dot+1:])
}

// Classify returns the unified file classification and web preview whitelist metadata.
func Classify(name string) Classification {
	ext := ExtensionOf(name)
	var kind corev1.SharedFileKind
	var previewKind corev1.SharedFileWebPreviewKind
	var previewMime string

	switch ext {
	case "mp4", "m4v":
		kind = corev1.SharedFileKind_SHARED_FILE_KIND_VIDEO
		previewKind = corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_VIDEO
		previewMime = "video/mp4"
	case "webm":
		kind = corev1.SharedFileKind_SHARED_FILE_KIND_VIDEO
		previewKind = corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_VIDEO
		previewMime = "video/webm"
	case "mov":
		kind = corev1.SharedFileKind_SHARED_FILE_KIND_VIDEO
		previewKind = corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_VIDEO
		previewMime = "video/quicktime"
	case "mkv", "avi", "3gp", "flv", "wmv":
		kind = corev1.SharedFileKind_SHARED_FILE_KIND_VIDEO

	case "png":
		kind = corev1.SharedFileKind_SHARED_FILE_KIND_IMAGE
		previewKind = corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_IMAGE
		previewMime = "image/png"
	case "jpg", "jpeg":
		kind = corev1.SharedFileKind_SHARED_FILE_KIND_IMAGE
		previewKind = corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_IMAGE
		previewMime = "image/jpeg"
	case "gif":
		kind = corev1.SharedFileKind_SHARED_FILE_KIND_IMAGE
		previewKind = corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_IMAGE
		previewMime = "image/gif"
	case "webp":
		kind = corev1.SharedFileKind_SHARED_FILE_KIND_IMAGE
		previewKind = corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_IMAGE
		previewMime = "image/webp"
	case "heic", "heif", "bmp", "svg":
		kind = corev1.SharedFileKind_SHARED_FILE_KIND_IMAGE

	case "mp3":
		kind = corev1.SharedFileKind_SHARED_FILE_KIND_AUDIO
		previewKind = corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_AUDIO
		previewMime = "audio/mpeg"
	case "m4a":
		kind = corev1.SharedFileKind_SHARED_FILE_KIND_AUDIO
		previewKind = corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_AUDIO
		previewMime = "audio/mp4"
	case "wav":
		kind = corev1.SharedFileKind_SHARED_FILE_KIND_AUDIO
		previewKind = corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_AUDIO
		previewMime = "audio/wav"
	case "ogg":
		kind = corev1.SharedFileKind_SHARED_FILE_KIND_AUDIO
		previewKind = corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_AUDIO
		previewMime = "audio/ogg"
	case "aac", "flac", "opus":
		kind = corev1.SharedFileKind_SHARED_FILE_KIND_AUDIO

	case "zip", "tar", "gz", "tgz", "bz2", "xz", "7z", "rar", "zst":
		kind = corev1.SharedFileKind_SHARED_FILE_KIND_ARCHIVE

	case "pdf":
		kind = corev1.SharedFileKind_SHARED_FILE_KIND_PDF

	case "doc", "docx", "xls", "xlsx", "ppt", "pptx", "txt", "md", "csv", "rtf", "pages", "numbers", "key":
		kind = corev1.SharedFileKind_SHARED_FILE_KIND_DOC

	default:
		kind = corev1.SharedFileKind_SHARED_FILE_KIND_OTHER
	}

	return Classification{
		Kind:              kind,
		KindLabel:         KindLabel(kind),
		SupportsAlbumSave: kind == corev1.SharedFileKind_SHARED_FILE_KIND_IMAGE || kind == corev1.SharedFileKind_SHARED_FILE_KIND_VIDEO,
		HasMediaDuration:  kind == corev1.SharedFileKind_SHARED_FILE_KIND_VIDEO || kind == corev1.SharedFileKind_SHARED_FILE_KIND_AUDIO,
		WebPreviewKind:    previewKind,
		WebPreviewMime:    previewMime,
	}
}

// KindLabel returns the localized type label for a SharedFileKind.
// Both DOC and OTHER display shared_file.type_other per docs/session-files.md.
func KindLabel(kind corev1.SharedFileKind) *corev1.Text {
	switch kind {
	case corev1.SharedFileKind_SHARED_FILE_KIND_VIDEO:
		return text.Msg(text.KeySharedFileTypeVideo)
	case corev1.SharedFileKind_SHARED_FILE_KIND_IMAGE:
		return text.Msg(text.KeySharedFileTypeImage)
	case corev1.SharedFileKind_SHARED_FILE_KIND_AUDIO:
		return text.Msg(text.KeySharedFileTypeAudio)
	case corev1.SharedFileKind_SHARED_FILE_KIND_ARCHIVE:
		return text.Msg(text.KeySharedFileTypeArchive)
	case corev1.SharedFileKind_SHARED_FILE_KIND_PDF:
		return text.Msg(text.KeySharedFileTypePdf)
	default:
		return text.Msg(text.KeySharedFileTypeOther)
	}
}

// DisplaySourcePath formats the source path on the computer for the long-press menu header:
// preserves the full path while shrinking the home directory to "~", falling back to the
// sanitized filename when rawPath is blank.
func DisplaySourcePath(rawPath, machineHome, fallbackName string) string {
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" {
		return SanitizeFileName(fallbackName)
	}
	home := strings.TrimRight(strings.TrimSpace(machineHome), "/")
	if home != "" {
		if trimmed == home {
			return "~"
		}
		if strings.HasPrefix(trimmed, home+"/") {
			return "~" + trimmed[len(home):]
		}
	}
	for _, prefix := range []string{"/Users/", "/home/"} {
		if strings.HasPrefix(trimmed, prefix) {
			rest := trimmed[len(prefix):]
			if slash := strings.IndexByte(rest, '/'); slash > 0 {
				return "~" + rest[slash:]
			}
			if rest != "" && !strings.Contains(rest, "/") {
				return "~"
			}
		}
	}
	return trimmed
}

// FormatFileSize formats byte counts using 1024-based binary scaling with B / KB / MB / GB labels.
// Rounding carries cleanly across unit boundaries (e.g. 1023.99 KB -> "1.0 MB", 1023.99 MB -> "1.00 GB").
func FormatFileSize(bytes int64) string {
	if bytes < 0 {
		bytes = 0
	}
	if bytes < kibBytes {
		return strconv.FormatInt(bytes, 10) + " B"
	}
	if bytes < mibBytes {
		kb := float64(bytes) / float64(kibBytes)
		if math.Round(kb*10)/10 >= 1024 {
			return "1.0 MB"
		}
		return strconv.FormatFloat(kb, 'f', 1, 64) + " KB"
	}
	if bytes < gibBytes {
		mb := float64(bytes) / float64(mibBytes)
		if math.Round(mb*10)/10 >= 1024 {
			return "1.00 GB"
		}
		return strconv.FormatFloat(mb, 'f', 1, 64) + " MB"
	}
	gb := float64(bytes) / float64(gibBytes)
	return strconv.FormatFloat(gb, 'f', 2, 64) + " GB"
}

// FormatFileProgress formats a (done, total) pair for downloading/paused subtitles,
// dropping the redundant unit on done when both values share the same unit.
func FormatFileProgress(done, total int64) (doneText, totalText string) {
	if total < 0 {
		total = 0
	}
	if done < 0 {
		done = 0
	}
	if done > total {
		done = total
	}
	totalText = FormatFileSize(total)
	doneText = FormatFileSize(done)
	tNum, tUnit, tOk := strings.Cut(totalText, " ")
	dNum, dUnit, dOk := strings.Cut(doneText, " ")
	_ = tNum
	if tOk && dOk && tUnit == dUnit {
		return dNum, totalText
	}
	return doneText, totalText
}

// FormatMediaDuration formats media length in milliseconds as m:ss, or h:mm:ss past an hour,
// rounding to the nearest second.
func FormatMediaDuration(durationMs int64) string {
	if durationMs < 0 {
		durationMs = 0
	}
	totalSecs := (durationMs + 500) / 1000
	h := totalSecs / 3600
	m := (totalSecs % 3600) / 60
	s := totalSecs % 60
	if h > 0 {
		return strconv.FormatInt(h, 10) + ":" + twoDigits(m) + ":" + twoDigits(s)
	}
	return strconv.FormatInt(m, 10) + ":" + twoDigits(s)
}

// FormatEta formats estimated remaining download seconds into seconds, ceil minutes, or ceil hours.
func FormatEta(seconds int64) *corev1.Text {
	if seconds < 1 {
		seconds = 1
	}
	if seconds < 60 {
		return text.Msg(text.KeySharedFileEtaSeconds, text.Int("count", seconds))
	}
	if seconds < 3600 {
		return text.Msg(text.KeySharedFileEtaMinutes, text.Int("count", (seconds+59)/60))
	}
	return text.Msg(text.KeySharedFileEtaHours, text.Int("count", (seconds+3599)/3600))
}

// ResolveCardStatus computes the unified card status priority from transfer state,
// computer reachability, session liveness, and file availability:
//  1. DOWNLOADED (local copy always wins over offline/exited)
//  2. DOWNLOADING (active transfer/verification in flight)
//  3. CLEANED (session exited, daemon returned not_found, or empty file_id)
//  4. PAUSED (preserved while offline when partial bytes exist)
//  5. OFFLINE (computer unreachable/disconnected; beats FAILED so retry isn't offered offline)
//  6. FAILED
//  7. AVAILABLE
func ResolveCardStatus(local *LocalState, available, online, sessionExited bool) corev1.SharedFileCardStatus {
	var st TransferStatus
	var dl int64
	if local != nil {
		st = local.Status
		dl = local.DownloadedBytes
	}
	if st == TransferStatusDownloaded {
		return corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_DOWNLOADED
	}
	if st == TransferStatusDownloading {
		return corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_DOWNLOADING
	}
	if sessionExited || st == TransferStatusCleanedUp || !available {
		return corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_CLEANED
	}
	if st == TransferStatusPaused && (online || dl > 0) {
		return corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_PAUSED
	}
	if !online {
		return corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_OFFLINE
	}
	if st == TransferStatusFailed {
		return corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_FAILED
	}
	return corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_AVAILABLE
}

// BuildCard constructs a complete, display-ready SessionSharedFileItem view model.
func BuildCard(in CardInput) *corev1.SessionSharedFileItem {
	f := in.File
	if f == nil {
		return nil
	}
	available := f.FileId != ""
	total := f.SizeBytes
	if total < 0 {
		total = 0
	}
	displayName := SanitizeFileName(f.Name)
	nameHead, nameTail := SplitMiddleTruncateName(displayName)
	cls := Classify(displayName)
	sizeStr := FormatFileSize(total)
	displayPath := DisplaySourcePath(f.Path, in.MachineHome, displayName)
	var sourceCaption *corev1.Text
	if strings.TrimSpace(in.MachineName) != "" {
		sourceCaption = text.Msg(text.KeySharedFileMenuFromMachine, text.Str("machine", in.MachineName))
	}

	status := ResolveCardStatus(in.Local, available, in.Online, in.SessionExited)

	var downloaded int64
	var progress float64
	var waitingReconnect bool
	var durationText string
	var subtitle *corev1.Text
	var subtitleIsError bool
	var a11yState *corev1.Text

	switch status {
	case corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_AVAILABLE:
		subtitle = text.Msg(text.KeySharedFileSubtitleKindSize, text.ArgText("kind", cls.KindLabel), text.Str("size", sizeStr))
		a11yState = text.Msg(text.KeySharedFileA11YStateAvailable)

	case corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_DOWNLOADING:
		downloaded = clampBytes(in.Local.DownloadedBytes, total)
		progress = byteProgress(downloaded, total)
		waitingReconnect = in.Local.WaitingReconnect
		doneStr, totalStr := FormatFileProgress(downloaded, total)
		if in.Local.HasEta && in.Local.EtaSeconds > 0 {
			subtitle = text.Msg(
				text.KeySharedFileSubtitleDownloadingEta,
				text.Str("downloaded", doneStr),
				text.Str("total", totalStr),
				text.ArgText("eta", FormatEta(in.Local.EtaSeconds)),
			)
		} else {
			subtitle = text.Msg(
				text.KeySharedFileSubtitleDownloading,
				text.Str("downloaded", doneStr),
				text.Str("total", totalStr),
			)
		}
		a11yState = text.Msg(text.KeySharedFileA11YStateDownloading)

	case corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_PAUSED:
		downloaded = clampBytes(in.Local.DownloadedBytes, total)
		progress = byteProgress(downloaded, total)
		doneStr, totalStr := FormatFileProgress(downloaded, total)
		subtitle = text.Msg(
			text.KeySharedFileSubtitlePaused,
			text.Str("downloaded", doneStr),
			text.Str("total", totalStr),
		)
		a11yState = text.Msg(text.KeySharedFileA11YStatePaused)

	case corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_DOWNLOADED:
		downloaded = total
		progress = 1
		if cls.HasMediaDuration && in.Local != nil && in.Local.HasDuration {
			durationText = FormatMediaDuration(in.Local.DurationMs)
			subtitle = text.Msg(text.KeySharedFileSubtitleMediaSize, text.Str("duration", durationText), text.Str("size", sizeStr))
		} else {
			subtitle = text.Msg(text.KeySharedFileSubtitleKindSize, text.ArgText("kind", cls.KindLabel), text.Str("size", sizeStr))
		}
		a11yState = text.Msg(text.KeySharedFileA11YStateDownloaded)

	case corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_FAILED:
		if in.Local != nil {
			downloaded = clampBytes(in.Local.DownloadedBytes, total)
			progress = byteProgress(downloaded, total)
		}
		subtitle = failureSubtitle(in.Local)
		subtitleIsError = true
		a11yState = subtitle

	case corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_OFFLINE:
		if in.Local != nil && in.Local.Status == TransferStatusFailed {
			downloaded = clampBytes(in.Local.DownloadedBytes, total)
			progress = byteProgress(downloaded, total)
		}
		subtitle = text.Msg(text.KeySharedFileSubtitleOffline)
		a11yState = subtitle

	case corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_CLEANED:
		subtitle = text.Msg(text.KeySharedFileSubtitleCleaned)
		a11yState = subtitle
	}

	a11yLabel := text.Msg(
		text.KeySharedFileA11YCard,
		text.Str("name", displayName),
		text.ArgText("kind", cls.KindLabel),
		text.Str("size", sizeStr),
		text.ArgText("state", a11yState),
	)

	primaryAction, primaryLabel, primaryEnabled, primaryDisabledReason := buildPrimaryAction(status, in.Online)
	menuItems := buildMenuItems(status, cls, in.Online, downloaded)

	return &corev1.SessionSharedFileItem{
		SizeBytes:             f.SizeBytes,
		FileId:                f.FileId,
		Sha256:                slices.Clone(f.Sha256),
		DisplayName:           displayName,
		NameHead:              nameHead,
		NameTail:              nameTail,
		Kind:                  cls.Kind,
		KindLabel:             cls.KindLabel,
		SizeText:              text.Plain(sizeStr),
		DisplayPath:           displayPath,
		SourceCaption:         sourceCaption,
		Status:                status,
		Subtitle:              subtitle,
		SubtitleIsError:       subtitleIsError,
		A11YLabel:             a11yLabel,
		DownloadedBytes:       downloaded,
		Progress:              progress,
		WaitingReconnect:      waitingReconnect,
		PrimaryAction:         primaryAction,
		PrimaryLabel:          primaryLabel,
		PrimaryEnabled:        primaryEnabled,
		PrimaryDisabledReason: primaryDisabledReason,
		MenuItems:             menuItems,
		SupportsAlbumSave:     cls.SupportsAlbumSave,
		HasMediaDuration:      cls.HasMediaDuration,
		DurationText:          durationText,
		WebPreviewKind:        cls.WebPreviewKind,
		WebPreviewMime:        cls.WebPreviewMime,
	}
}

func failureSubtitle(local *LocalState) *corev1.Text {
	var kind corev1.SharedFileFailureKind
	var detail string
	if local != nil {
		kind = local.FailureKind
		detail = strings.TrimSpace(local.FailureDetail)
	}
	var reason *corev1.Text
	switch kind {
	case corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_CHECKSUM_MISMATCH:
		reason = text.Msg(text.KeySharedFileReasonChecksumMismatch)
	case corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_DISCONNECTED:
		reason = text.Msg(text.KeySharedFileReasonDisconnected)
	case corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_PHONE_STORAGE_FULL:
		reason = text.Msg(text.KeySharedFileReasonDiskFull)
	case corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_BROWSER_STORAGE_FULL:
		reason = text.Msg(text.KeySharedFileReasonBrowserStorageFull)
	default:
		if local != nil && local.FailureText != nil {
			reason = local.FailureText
		} else if detail != "" {
			reason = text.Plain(detail)
		} else {
			reason = text.Msg(text.KeySharedFileReasonUnknown)
		}
	}
	return text.Msg(text.KeySharedFileSubtitleFailed, text.ArgText("reason", reason))
}

func buildPrimaryAction(
	status corev1.SharedFileCardStatus,
	online bool,
) (corev1.SharedFilePrimaryAction, *corev1.Text, bool, *corev1.Text) {
	switch status {
	case corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_AVAILABLE:
		return corev1.SharedFilePrimaryAction_SHARED_FILE_PRIMARY_ACTION_DOWNLOAD,
			text.Msg(text.KeySharedFileActionDownload), true, nil
	case corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_DOWNLOADING:
		return corev1.SharedFilePrimaryAction_SHARED_FILE_PRIMARY_ACTION_PAUSE,
			text.Msg(text.KeySharedFileActionPause), true, nil
	case corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_PAUSED:
		if !online {
			return corev1.SharedFilePrimaryAction_SHARED_FILE_PRIMARY_ACTION_RESUME,
				text.Msg(text.KeySharedFileActionResume), false, text.Msg(text.KeySharedFileSubtitleOffline)
		}
		return corev1.SharedFilePrimaryAction_SHARED_FILE_PRIMARY_ACTION_RESUME,
			text.Msg(text.KeySharedFileActionResume), true, nil
	case corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_DOWNLOADED:
		return corev1.SharedFilePrimaryAction_SHARED_FILE_PRIMARY_ACTION_OPEN,
			text.Msg(text.KeySharedFileActionOpen), true, nil
	case corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_FAILED:
		return corev1.SharedFilePrimaryAction_SHARED_FILE_PRIMARY_ACTION_RETRY,
			text.Msg(text.KeySharedFileActionRetry), true, nil
	case corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_OFFLINE:
		return corev1.SharedFilePrimaryAction_SHARED_FILE_PRIMARY_ACTION_DOWNLOAD,
			text.Msg(text.KeySharedFileActionDownload), false, text.Msg(text.KeySharedFileSubtitleOffline)
	default:
		return corev1.SharedFilePrimaryAction_SHARED_FILE_PRIMARY_ACTION_UNSPECIFIED,
			nil, false, text.Msg(text.KeySharedFileSubtitleCleaned)
	}
}

func buildMenuItems(
	status corev1.SharedFileCardStatus,
	cls Classification,
	online bool,
	downloadedBytes int64,
) []*corev1.SharedFileMenuItemView {
	switch status {
	case corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_AVAILABLE:
		return []*corev1.SharedFileMenuItemView{
			{
				Action:  corev1.SharedFileMenuAction_SHARED_FILE_MENU_ACTION_DOWNLOAD,
				Label:   text.Msg(text.KeySharedFileActionDownload),
				Enabled: true,
			},
		}

	case corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_DOWNLOADING:
		return []*corev1.SharedFileMenuItemView{
			{
				Action:  corev1.SharedFileMenuAction_SHARED_FILE_MENU_ACTION_PAUSE,
				Label:   text.Msg(text.KeySharedFileActionPause),
				Enabled: true,
			},
			{
				Action:      corev1.SharedFileMenuAction_SHARED_FILE_MENU_ACTION_CANCEL_DOWNLOAD,
				Label:       text.Msg(text.KeySharedFileActionCancelDownload),
				Enabled:     true,
				Destructive: true,
				Separated:   true,
			},
		}

	case corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_PAUSED:
		resume := &corev1.SharedFileMenuItemView{
			Action:  corev1.SharedFileMenuAction_SHARED_FILE_MENU_ACTION_RESUME,
			Label:   text.Msg(text.KeySharedFileActionResume),
			Enabled: online,
		}
		if !online {
			resume.DisabledReason = text.Msg(text.KeySharedFileSubtitleOffline)
		}
		return []*corev1.SharedFileMenuItemView{
			resume,
			{
				Action:      corev1.SharedFileMenuAction_SHARED_FILE_MENU_ACTION_CANCEL_DOWNLOAD,
				Label:       text.Msg(text.KeySharedFileActionCancelDownload),
				Enabled:     true,
				Destructive: true,
				Separated:   true,
			},
		}

	case corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_FAILED:
		items := []*corev1.SharedFileMenuItemView{
			{
				Action:  corev1.SharedFileMenuAction_SHARED_FILE_MENU_ACTION_RETRY,
				Label:   text.Msg(text.KeySharedFileActionRetry),
				Enabled: true,
			},
		}
		if downloadedBytes > 0 {
			items = append(items, &corev1.SharedFileMenuItemView{
				Action:      corev1.SharedFileMenuAction_SHARED_FILE_MENU_ACTION_CANCEL_DOWNLOAD,
				Label:       text.Msg(text.KeySharedFileActionCancelDownload),
				Enabled:     true,
				Destructive: true,
				Separated:   true,
			})
		}
		return items

	case corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_DOWNLOADED:
		items := make([]*corev1.SharedFileMenuItemView, 0, 5)
		if cls.SupportsAlbumSave {
			items = append(items, &corev1.SharedFileMenuItemView{
				Action:  corev1.SharedFileMenuAction_SHARED_FILE_MENU_ACTION_SAVE_TO_ALBUM,
				Label:   text.Msg(text.KeySharedFileMenuSaveToAlbum),
				Enabled: true,
			})
		}
		items = append(items,
			&corev1.SharedFileMenuItemView{
				Action:  corev1.SharedFileMenuAction_SHARED_FILE_MENU_ACTION_SAVE,
				Label:   text.Msg(text.KeySharedFileMenuSaveToDevice),
				Enabled: true,
			},
			&corev1.SharedFileMenuItemView{
				Action:  corev1.SharedFileMenuAction_SHARED_FILE_MENU_ACTION_SHARE,
				Label:   text.Msg(text.KeySharedFileMenuShare),
				Enabled: true,
			},
			&corev1.SharedFileMenuItemView{
				Action:  corev1.SharedFileMenuAction_SHARED_FILE_MENU_ACTION_OPEN_WITH,
				Label:   text.Msg(text.KeySharedFileMenuOpenWith),
				Enabled: true,
			},
			&corev1.SharedFileMenuItemView{
				Action:      corev1.SharedFileMenuAction_SHARED_FILE_MENU_ACTION_DELETE_LOCAL,
				Label:       text.Msg(text.KeySharedFileMenuDeleteLocal),
				Enabled:     true,
				Destructive: true,
				Separated:   true,
			},
		)
		return items

	default:
		// OFFLINE and CLEANED show only the source header in the long-press menu.
		return nil
	}
}

func clampBytes(v, max int64) int64 {
	if v < 0 {
		return 0
	}
	if max >= 0 && v > max {
		return max
	}
	return v
}

func byteProgress(done, total int64) float64 {
	if total <= 0 || done <= 0 {
		return 0
	}
	p := float64(done) / float64(total)
	if p > 1 {
		return 1
	}
	return p
}

func lastRuneIndex(runes []rune, target rune) int {
	for i := len(runes) - 1; i >= 0; i-- {
		if runes[i] == target {
			return i
		}
	}
	return -1
}

func twoDigits(n int64) string {
	if n < 10 {
		return "0" + strconv.FormatInt(n, 10)
	}
	return strconv.FormatInt(n, 10)
}
