package sharedfiles

import (
	"bytes"
	"strings"
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name        string
		wantKind    corev1.SharedFileKind
		wantLabel   string
		wantAlbum   bool
		wantDur     bool
		wantPrev    corev1.SharedFileWebPreviewKind
		wantMime    string
	}{
		{"clip.MP4", corev1.SharedFileKind_SHARED_FILE_KIND_VIDEO, text.KeySharedFileTypeVideo, true, true, corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_VIDEO, "video/mp4"},
		{"clip.mov", corev1.SharedFileKind_SHARED_FILE_KIND_VIDEO, text.KeySharedFileTypeVideo, true, true, corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_VIDEO, "video/quicktime"},
		{"clip.3gp", corev1.SharedFileKind_SHARED_FILE_KIND_VIDEO, text.KeySharedFileTypeVideo, true, true, corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_UNSPECIFIED, ""},
		{"clip.flv", corev1.SharedFileKind_SHARED_FILE_KIND_VIDEO, text.KeySharedFileTypeVideo, true, true, corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_UNSPECIFIED, ""},
		{"pic.png", corev1.SharedFileKind_SHARED_FILE_KIND_IMAGE, text.KeySharedFileTypeImage, true, false, corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_IMAGE, "image/png"},
		{"pic.heic", corev1.SharedFileKind_SHARED_FILE_KIND_IMAGE, text.KeySharedFileTypeImage, true, false, corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_UNSPECIFIED, ""},
		{"diagram.svg", corev1.SharedFileKind_SHARED_FILE_KIND_IMAGE, text.KeySharedFileTypeImage, true, false, corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_UNSPECIFIED, ""},
		{"song.mp3", corev1.SharedFileKind_SHARED_FILE_KIND_AUDIO, text.KeySharedFileTypeAudio, false, true, corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_AUDIO, "audio/mpeg"},
		{"song.flac", corev1.SharedFileKind_SHARED_FILE_KIND_AUDIO, text.KeySharedFileTypeAudio, false, true, corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_UNSPECIFIED, ""},
		{"bundle.tar.gz", corev1.SharedFileKind_SHARED_FILE_KIND_ARCHIVE, text.KeySharedFileTypeArchive, false, false, corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_UNSPECIFIED, ""},
		{"bundle.zst", corev1.SharedFileKind_SHARED_FILE_KIND_ARCHIVE, text.KeySharedFileTypeArchive, false, false, corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_UNSPECIFIED, ""},
		{"report.pdf", corev1.SharedFileKind_SHARED_FILE_KIND_PDF, text.KeySharedFileTypePdf, false, false, corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_UNSPECIFIED, ""},
		{"notes.md", corev1.SharedFileKind_SHARED_FILE_KIND_DOC, text.KeySharedFileTypeOther, false, false, corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_UNSPECIFIED, ""},
		{"sheet.xlsx", corev1.SharedFileKind_SHARED_FILE_KIND_DOC, text.KeySharedFileTypeOther, false, false, corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_UNSPECIFIED, ""},
		{"deck.key", corev1.SharedFileKind_SHARED_FILE_KIND_DOC, text.KeySharedFileTypeOther, false, false, corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_UNSPECIFIED, ""},
		{"index.html", corev1.SharedFileKind_SHARED_FILE_KIND_OTHER, text.KeySharedFileTypeOther, false, false, corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_UNSPECIFIED, ""},
		{".gitignore", corev1.SharedFileKind_SHARED_FILE_KIND_OTHER, text.KeySharedFileTypeOther, false, false, corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_UNSPECIFIED, ""},
		{"Makefile", corev1.SharedFileKind_SHARED_FILE_KIND_OTHER, text.KeySharedFileTypeOther, false, false, corev1.SharedFileWebPreviewKind_SHARED_FILE_WEB_PREVIEW_KIND_UNSPECIFIED, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.name)
			if got.Kind != tc.wantKind || got.KindLabel.GetKey() != tc.wantLabel ||
				got.SupportsAlbumSave != tc.wantAlbum || got.HasMediaDuration != tc.wantDur ||
				got.WebPreviewKind != tc.wantPrev || got.WebPreviewMime != tc.wantMime {
				t.Fatalf("Classify(%q) = %+v", tc.name, got)
			}
		})
	}
}

func TestSanitizeAndSplitFileName(t *testing.T) {
	longASCII := strings.Repeat("a", 270) + ".tar.gz" // ext ".gz" is 3 bytes -> 252 + 3 = 255 bytes
	// "文" is 3 UTF-8 bytes; ".pdf" is 4 bytes -> max stem is 251 bytes -> floor(251/3)=83 runes = 249 bytes + 4 = 253 bytes
	longCJK := strings.Repeat("文", 90) + ".pdf"
	longNoExt := strings.Repeat("x", 260) + ".verylongextensionname"

	sanCases := []struct {
		in   string
		want string
	}{
		{"/Users/me/dir/report.pdf", "report.pdf"},
		{`C:\Users\me\secret.txt`, "secret.txt"},
		{"/a/b/", "file"},
		{"   ", "file"},
		{".", "file"},
		{"..", "file"},
		{"bad\x00\x1f\x7f\u0085\u202e\u200f\u061c\u2066\u200b\ufeff\u2028\u2029\u00adname.png", "badname.png"},
		{longASCII, strings.Repeat("a", 252) + ".gz"},
		{longCJK, strings.Repeat("文", 83) + ".pdf"},
		{longNoExt, strings.Repeat("x", 255)},
	}
	for _, tc := range sanCases {
		got := SanitizeFileName(tc.in)
		if got != tc.want {
			t.Fatalf("SanitizeFileName(%q) = %q (bytes=%d), want %q (bytes=%d)", tc.in, got, len(got), tc.want, len(tc.want))
		}
		if len(got) > MaxFileNameBytes {
			t.Fatalf("SanitizeFileName(%q) exceeded %d bytes: %d", tc.in, MaxFileNameBytes, len(got))
		}
	}

	splitCases := []struct {
		in       string
		wantHead string
		wantTail string
	}{
		{"daocheng-final.mp4", "daocheng-fi", "nal.mp4"},
		{"a.pdf", "a", ".pdf"},
		{"ab.pdf", "a", "b.pdf"},
		{"报告最终版.docx", "报告", "最终版.docx"},
		{"Makefile", "Makefile", ""},
		{".env", ".env", ""},
		{"trailing.", "trailing.", ""},
		{"name.superlongextension", "name.superlongextension", ""},
	}
	for _, tc := range splitCases {
		h, tail := SplitMiddleTruncateName(tc.in)
		if h != tc.wantHead || tail != tc.wantTail {
			t.Fatalf("SplitMiddleTruncateName(%q) = (%q, %q), want (%q, %q)", tc.in, h, tail, tc.wantHead, tc.wantTail)
		}
	}
}

func TestFormatFileSizeAndProgress(t *testing.T) {
	sizeCases := []struct {
		bytes int64
		want  string
	}{
		{-5, "0 B"},
		{0, "0 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{mibBytes - 1, "1.0 MB"}, // 1023.999 KB carries to 1.0 MB
		{mibBytes, "1.0 MB"},
		{24*mibBytes + 307*kibBytes, "24.3 MB"},
		{gibBytes - 1, "1.00 GB"}, // 1023.999 MB carries to 1.00 GB
		{gibBytes, "1.00 GB"},
		{2*gibBytes + 512*mibBytes, "2.50 GB"},
	}
	for _, tc := range sizeCases {
		if got := FormatFileSize(tc.bytes); got != tc.want {
			t.Fatalf("FormatFileSize(%d) = %q, want %q", tc.bytes, got, tc.want)
		}
	}

	progCases := []struct {
		done, total   int64
		wantD, wantT  string
	}{
		{0, 512, "0", "512 B"},
		{512 * kibBytes, 24 * mibBytes, "512.0 KB", "24.0 MB"},
		{8 * mibBytes, 24 * mibBytes, "8.0", "24.0 MB"},
		{30 * mibBytes, 24 * mibBytes, "24.0", "24.0 MB"},
	}
	for _, tc := range progCases {
		d, tot := FormatFileProgress(tc.done, tc.total)
		if d != tc.wantD || tot != tc.wantT {
			t.Fatalf("FormatFileProgress(%d, %d) = (%q, %q), want (%q, %q)", tc.done, tc.total, d, tot, tc.wantD, tc.wantT)
		}
	}
}

func TestFormatDurationAndEtaAndPath(t *testing.T) {
	durCases := []struct {
		ms   int64
		want string
	}{
		{-100, "0:00"},
		{0, "0:00"},
		{499, "0:00"},
		{500, "0:01"},
		{102_000, "1:42"},
		{3661_000, "1:01:01"},
	}
	for _, tc := range durCases {
		if got := FormatMediaDuration(tc.ms); got != tc.want {
			t.Fatalf("FormatMediaDuration(%d) = %q, want %q", tc.ms, got, tc.want)
		}
	}

	if eta := FormatEta(0); eta.GetKey() != text.KeySharedFileEtaSeconds || eta.Args[0].GetIntValue() != 1 {
		t.Fatalf("FormatEta(0) = %+v", eta)
	}
	if eta := FormatEta(59); eta.GetKey() != text.KeySharedFileEtaSeconds || eta.Args[0].GetIntValue() != 59 {
		t.Fatalf("FormatEta(59) = %+v", eta)
	}
	if eta := FormatEta(61); eta.GetKey() != text.KeySharedFileEtaMinutes || eta.Args[0].GetIntValue() != 2 {
		t.Fatalf("FormatEta(61) = %+v", eta)
	}
	if eta := FormatEta(3601); eta.GetKey() != text.KeySharedFileEtaHours || eta.Args[0].GetIntValue() != 2 {
		t.Fatalf("FormatEta(3601) = %+v", eta)
	}

	if got := DisplaySourcePath("/Users/alice/work/out.mp4", "/Users/alice", "out.mp4"); got != "~/work/out.mp4" {
		t.Fatalf("DisplaySourcePath home match = %q", got)
	}
	if got := DisplaySourcePath("/home/bob/deep/a/b/c.pdf", "", "c.pdf"); got != "~/deep/a/b/c.pdf" {
		t.Fatalf("DisplaySourcePath posix fallback = %q", got)
	}
	if got := DisplaySourcePath("", "/Users/alice", "../bad.pdf"); got != "bad.pdf" {
		t.Fatalf("DisplaySourcePath empty fallback = %q", got)
	}
}

func TestResolveCardStatusPriority(t *testing.T) {
	cases := []struct {
		name      string
		local     *LocalState
		available bool
		online    bool
		exited    bool
		want      corev1.SharedFileCardStatus
	}{
		{
			name:      "downloaded beats offline and exited",
			local:     &LocalState{Status: corev1.SharedFileTransferStatus_SHARED_FILE_TRANSFER_STATUS_DOWNLOADED},
			available: true, online: false, exited: true,
			want: corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_DOWNLOADED,
		},
		{
			name:      "downloading beats exited",
			local:     &LocalState{Status: corev1.SharedFileTransferStatus_SHARED_FILE_TRANSFER_STATUS_DOWNLOADING, DownloadedBytes: 100},
			available: true, online: true, exited: true,
			want: corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_DOWNLOADING,
		},
		{
			name:      "exited beats paused",
			local:     &LocalState{Status: corev1.SharedFileTransferStatus_SHARED_FILE_TRANSFER_STATUS_PAUSED, DownloadedBytes: 100},
			available: true, online: true, exited: true,
			want: corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_CLEANED,
		},
		{
			name:      "not_found cleaned_up beats paused",
			local:     &LocalState{Status: corev1.SharedFileTransferStatus_SHARED_FILE_TRANSFER_STATUS_CLEANED_UP},
			available: true, online: true, exited: false,
			want: corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_CLEANED,
		},
		{
			name:      "paused with partial bytes beats offline",
			local:     &LocalState{Status: corev1.SharedFileTransferStatus_SHARED_FILE_TRANSFER_STATUS_PAUSED, DownloadedBytes: 1024},
			available: true, online: false, exited: false,
			want: corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_PAUSED,
		},
		{
			name:      "paused with 0 bytes yields offline when computer offline",
			local:     &LocalState{Status: corev1.SharedFileTransferStatus_SHARED_FILE_TRANSFER_STATUS_PAUSED, DownloadedBytes: 0},
			available: true, online: false, exited: false,
			want: corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_OFFLINE,
		},
		{
			name:      "offline beats failed",
			local:     &LocalState{Status: corev1.SharedFileTransferStatus_SHARED_FILE_TRANSFER_STATUS_FAILED, FailureKind: corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_DISCONNECTED},
			available: true, online: false, exited: false,
			want: corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_OFFLINE,
		},
		{
			name:      "failed shown when online",
			local:     &LocalState{Status: corev1.SharedFileTransferStatus_SHARED_FILE_TRANSFER_STATUS_FAILED, FailureKind: corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_CHECKSUM_MISMATCH},
			available: true, online: true, exited: false,
			want: corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_FAILED,
		},
		{
			name:      "idle available when online",
			local:     nil,
			available: true, online: true, exited: false,
			want: corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_AVAILABLE,
		},
		{
			name:      "empty file_id resolves to cleaned",
			local:     nil,
			available: false, online: true, exited: false,
			want: corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_CLEANED,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveCardStatus(tc.local, tc.available, tc.online, tc.exited); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBuildCardMenusAndActions(t *testing.T) {
	hash := []byte{0xde, 0xad, 0xbe, 0xef}
	videoFile := &v1.SharedFile{
		FileId:    "0123456789abcdef0123456789abcdef",
		Name:      "daocheng-final.mp4",
		Path:      "/Users/me/out/daocheng-final.mp4",
		SizeBytes: 24 * mibBytes,
		Sha256:    hash,
	}

	// 1. Paused while offline -> Resume disabled with offline reason; Cancel enabled.
	pausedOff := BuildCard(CardInput{
		File:        videoFile,
		Local:       &LocalState{Status: corev1.SharedFileTransferStatus_SHARED_FILE_TRANSFER_STATUS_PAUSED, DownloadedBytes: 8 * mibBytes},
		MachineName: "MacBook",
		MachineHome: "/Users/me",
		Online:      false,
	})
	if !bytes.Equal(pausedOff.Sha256, hash) {
		t.Fatalf("expected sha256 passthrough")
	}
	if pausedOff.Status != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_PAUSED ||
		pausedOff.PrimaryAction != corev1.SharedFilePrimaryAction_SHARED_FILE_PRIMARY_ACTION_RESUME ||
		pausedOff.PrimaryEnabled || pausedOff.PrimaryDisabledReason.GetKey() != text.KeySharedFileSubtitleOffline {
		t.Fatalf("unexpected paused-offline primary action: %+v", pausedOff)
	}
	if len(pausedOff.MenuItems) != 2 || pausedOff.MenuItems[0].Enabled || pausedOff.MenuItems[0].DisabledReason.GetKey() != text.KeySharedFileSubtitleOffline || !pausedOff.MenuItems[1].Enabled || !pausedOff.MenuItems[1].Destructive {
		t.Fatalf("unexpected paused-offline menu items: %+v", pausedOff.MenuItems)
	}

	// 2. Downloaded video with duration -> duration subtitle & album save menu item first.
	dl := BuildCard(CardInput{
		File:        videoFile,
		Local:       &LocalState{Status: corev1.SharedFileTransferStatus_SHARED_FILE_TRANSFER_STATUS_DOWNLOADED, HasDuration: true, DurationMs: 102_000},
		MachineName: "MacBook",
		MachineHome: "/Users/me",
		Online:      false,
		SessionExited: true,
	})
	if dl.Status != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_DOWNLOADED ||
		dl.DurationText != "1:42" ||
		dl.Subtitle.GetKey() != text.KeySharedFileSubtitleMediaSize ||
		dl.PrimaryAction != corev1.SharedFilePrimaryAction_SHARED_FILE_PRIMARY_ACTION_OPEN ||
		!dl.PrimaryEnabled {
		t.Fatalf("unexpected downloaded video card: %+v", dl)
	}
	if len(dl.MenuItems) != 5 ||
		dl.MenuItems[0].Action != corev1.SharedFileMenuAction_SHARED_FILE_MENU_ACTION_SAVE_TO_ALBUM ||
		dl.MenuItems[1].Action != corev1.SharedFileMenuAction_SHARED_FILE_MENU_ACTION_SAVE ||
		dl.MenuItems[2].Action != corev1.SharedFileMenuAction_SHARED_FILE_MENU_ACTION_SHARE ||
		dl.MenuItems[3].Action != corev1.SharedFileMenuAction_SHARED_FILE_MENU_ACTION_OPEN_WITH ||
		dl.MenuItems[4].Action != corev1.SharedFileMenuAction_SHARED_FILE_MENU_ACTION_DELETE_LOCAL ||
		!dl.MenuItems[4].Destructive || !dl.MenuItems[4].Separated {
		t.Fatalf("unexpected downloaded video menu: %+v", dl.MenuItems)
	}

	// 3. Failed with partial bytes -> Retry + Cancel.
	failedPartial := BuildCard(CardInput{
		File: videoFile,
		Local: &LocalState{
			Status:          corev1.SharedFileTransferStatus_SHARED_FILE_TRANSFER_STATUS_FAILED,
			DownloadedBytes: 4 * mibBytes,
			FailureKind:     corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_BROWSER_STORAGE_FULL,
		},
		Online: true,
	})
	if failedPartial.Status != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_FAILED ||
		!failedPartial.SubtitleIsError ||
		len(failedPartial.MenuItems) != 2 ||
		failedPartial.MenuItems[0].Action != corev1.SharedFileMenuAction_SHARED_FILE_MENU_ACTION_RETRY ||
		failedPartial.MenuItems[1].Action != corev1.SharedFileMenuAction_SHARED_FILE_MENU_ACTION_CANCEL_DOWNLOAD {
		t.Fatalf("unexpected failed partial card: %+v", failedPartial)
	}

	// 4. Offline & Cleaned -> no menu items, primary disabled with reason.
	off := BuildCard(CardInput{File: videoFile, Online: false})
	if off.Status != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_OFFLINE || len(off.MenuItems) != 0 || off.PrimaryEnabled || off.PrimaryDisabledReason.GetKey() != text.KeySharedFileSubtitleOffline {
		t.Fatalf("unexpected offline card: %+v", off)
	}
	cleaned := BuildCard(CardInput{File: videoFile, Online: true, SessionExited: true})
	if cleaned.Status != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_CLEANED || len(cleaned.MenuItems) != 0 || cleaned.PrimaryAction != corev1.SharedFilePrimaryAction_SHARED_FILE_PRIMARY_ACTION_UNSPECIFIED || cleaned.PrimaryDisabledReason.GetKey() != text.KeySharedFileSubtitleCleaned {
		t.Fatalf("unexpected cleaned card: %+v", cleaned)
	}
}
