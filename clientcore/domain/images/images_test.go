package images

import (
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

func TestSplitImagePlaceholders(t *testing.T) {
	refs := func(idxs ...int32) []*v1.ImageRef {
		out := make([]*v1.ImageRef, len(idxs))
		for i, n := range idxs {
			out[i] = &v1.ImageRef{CliIndex: n}
		}
		return out
	}

	type segSpec struct {
		text string
		img  int32
	}
	cases := []struct {
		name string
		msg  string
		refs []*v1.ImageRef
		want []segSpec
	}{
		{
			name: "empty message",
			msg:  "",
			refs: refs(1),
			want: nil,
		},
		{
			name: "leading placeholders dropped when no later refs",
			msg:  "[Image #3] [Image #4]这两个页面底部的按钮都点不到",
			refs: refs(3, 4),
			want: []segSpec{{text: "这两个页面底部的按钮都点不到"}},
		},
		{
			name: "leading placeholder preserved as chip when later ref exists",
			msg:  "[Image #1]电脑上，[Image #2]手机上，为什么手机上这么挤？",
			refs: refs(1, 2),
			want: []segSpec{
				{img: 1},
				{text: "电脑上，"},
				{img: 2},
				{text: "手机上，为什么手机上这么挤？"},
			},
		},
		{
			name: "only leading placeholders yields empty segments",
			msg:  "  [Image #1]  [Image #2]  ",
			refs: refs(1, 2),
			want: nil,
		},
		{
			name: "unknown placeholder stays literal text",
			msg:  "看 [Image #9] 这个写法",
			refs: refs(1),
			want: []segSpec{{text: "看 [Image #9] 这个写法"}},
		},
		{
			name: "overflowing placeholder index stays literal text",
			msg:  "看 [Image #99999999999999999999] 这个",
			refs: refs(1),
			want: []segSpec{{text: "看 [Image #99999999999999999999] 这个"}},
		},
		{
			name: "no refs keeps text verbatim",
			msg:  "[Image #1] hello",
			refs: nil,
			want: []segSpec{{text: "[Image #1] hello"}},
		},
		{
			name: "inline placeholder only",
			msg:  "对比 [Image #1] 和上一版",
			refs: refs(1),
			want: []segSpec{
				{text: "对比 "},
				{img: 1},
				{text: " 和上一版"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SplitImagePlaceholders(tc.msg, tc.refs)
			if len(got) != len(tc.want) {
				t.Fatalf("len=%d, want %d: got=%+v", len(got), len(tc.want), got)
			}
			for i, w := range tc.want {
				if w.img > 0 {
					if got[i].GetImageIndex() != w.img {
						t.Fatalf("seg[%d] image=%d, want %d", i, got[i].GetImageIndex(), w.img)
					}
					if got[i].GetImageLabel().GetKey() != text.KeyImagePlaceholder {
						t.Fatalf("seg[%d] label=%+v, want key %s", i, got[i].GetImageLabel(), text.KeyImagePlaceholder)
					}
				} else if got[i].GetText() != w.text {
					t.Fatalf("seg[%d] text=%q, want %q", i, got[i].GetText(), w.text)
				}
			}
		})
	}
}

func TestCollectViewerImagesIncludesCollapsedScheduledGroupAndToolGroupSeqs(t *testing.T) {
	ref := func(id string, src v1.ImageSource) *v1.ImageRef {
		return &v1.ImageRef{
			ImageId: id,
			Source:  src,
			Sync:    v1.ImageSync_IMAGE_SYNC_SYNCED,
			Format:  v1.ImageFormat_IMAGE_FORMAT_PNG,
			Width:   1200,
			Height:  800,
			Path:    "/Users/alice/shots/" + id + ".png",
		}
	}
	items := []*corev1.SessionTimelineItem{
		{
			Seq: 1, TimestampMs: 100,
			Kind: &corev1.SessionTimelineItem_UserMessage{
				UserMessage: &corev1.SessionUserMessageItem{
					Images: []*v1.ImageRef{ref("u1", v1.ImageSource_IMAGE_SOURCE_USER)},
				},
			},
		},
		{
			Seq: 2, EndSeq: 4, TimestampMs: 200,
			Kind: &corev1.SessionTimelineItem_ToolGroup{
				ToolGroup: &corev1.SessionToolGroupItem{
					Calls: []*corev1.SessionToolCallItem{
						{Seq: 2, Title: "Read a.png", Images: []*v1.ImageRef{ref("t1", v1.ImageSource_IMAGE_SOURCE_TOOL)}},
						{Seq: 4, Title: "Read b.png", Images: []*v1.ImageRef{ref("t2", v1.ImageSource_IMAGE_SOURCE_TOOL)}},
					},
				},
			},
		},
		{
			Seq: 5, EndSeq: 8, TimestampMs: 300,
			Kind: &corev1.SessionTimelineItem_QuietScheduledGroup{
				QuietScheduledGroup: &corev1.SessionQuietScheduledGroupItem{
					GroupId:  "quiet-1",
					Expanded: false,
					Items: []*corev1.SessionTimelineItem{
						{
							Seq: 6, TimestampMs: 310,
							Kind: &corev1.SessionTimelineItem_GeneratedImages{
								GeneratedImages: &corev1.SessionGeneratedImagesItem{
									RevisedPrompt: "a misty hill",
									Images:        []*v1.ImageRef{ref("g1", v1.ImageSource_IMAGE_SOURCE_GENERATED)},
								},
							},
						},
					},
				},
			},
		},
	}

	got := CollectViewerImages(items, "Codex", "/Users/alice")
	if len(got) != 4 {
		t.Fatalf("expected 4 viewer images (including collapsed scheduled group), got %d", len(got))
	}
	if got[0].Seq != 1 || got[0].ViewerIndex != 0 || got[0].InCollapsedGroup || got[0].DisplayPath != "~/shots/u1.png" || got[0].SizeText != "1200 × 800" {
		t.Fatalf("unexpected viewer[0]: %+v", got[0])
	}
	if got[1].Seq != 2 || got[2].Seq != 4 || got[2].ToolTitle != "Read b.png" {
		t.Fatalf("expected exact per-call seq in tool group, got seqs %d and %d", got[1].Seq, got[2].Seq)
	}
	if got[3].Seq != 6 || !got[3].InCollapsedGroup || got[3].RevisedPrompt != "a misty hill" || got[3].SourceLabel.GetKey() != text.KeyImageGeneratedBy {
		t.Fatalf("unexpected collapsed scheduled group image: %+v", got[3])
	}

	// Start index & stepping rules.
	if idx := ViewerStartIndex(got, 4, 0, 0); idx != 2 {
		t.Fatalf("ViewerStartIndex(seq=4, 0) = %d, want 2", idx)
	}
	if idx := ViewerStartIndex(got, 6, 0, 0); idx != 3 {
		t.Fatalf("ViewerStartIndex(seq=6, 0) = %d, want 3", idx)
	}
	if idx := ViewerStartIndex(got, 999, 0, 0); idx != 0 {
		t.Fatalf("ViewerStartIndex(missing seq) = %d, want 0", idx)
	}
	if idx := StepViewerIndex(0, -1, 4); idx != 0 {
		t.Fatalf("StepViewerIndex(0, -1, 4) = %d, want 0", idx)
	}
	if idx := StepViewerIndex(2, 1, 4); idx != 3 {
		t.Fatalf("StepViewerIndex(2, +1, 4) = %d, want 3", idx)
	}
	if idx := StepViewerIndex(3, 1, 4); idx != 3 {
		t.Fatalf("StepViewerIndex(3, +1, 4) = %d, want 3", idx)
	}
}

func TestAspectSizeAndFormatHelpers(t *testing.T) {
	if got := AspectRatio(nil); got != 4.0/3.0 {
		t.Fatalf("nil aspect = %v", got)
	}
	tall := &v1.ImageRef{PreviewWidth: 100, PreviewHeight: 1000}
	if ClampedAspectRatio(tall) != 0.4 {
		t.Fatalf("tall clamped aspect = %v, want 0.4", ClampedAspectRatio(tall))
	}
	wide := &v1.ImageRef{Width: 4000, Height: 1000}
	if ClampedAspectRatio(wide) != 2.5 {
		t.Fatalf("wide clamped aspect = %v, want 2.5", ClampedAspectRatio(wide))
	}
	orig := &v1.ImageRef{Width: 1024, Height: 768, OriginalWidth: 2880, OriginalHeight: 1800}
	if SizeText(orig) != "2880 × 1800" {
		t.Fatalf("SizeText(orig) = %q", SizeText(orig))
	}
	if FormatExtension(v1.ImageFormat_IMAGE_FORMAT_WEBP) != "webp" || FormatMime(v1.ImageFormat_IMAGE_FORMAT_WEBP) != "image/webp" {
		t.Fatalf("unexpected webp ext/mime")
	}
	if FormatExtension(v1.ImageFormat_IMAGE_FORMAT_UNSPECIFIED) != "jpg" || FormatMime(v1.ImageFormat_IMAGE_FORMAT_UNSPECIFIED) != "image/jpeg" {
		t.Fatalf("unexpected default ext/mime")
	}
	if DisplayName("") != "" || DisplayName("   ") != "" {
		t.Fatalf("empty DisplayName should stay empty")
	}
	if got := DisplayName("/tmp/\u202e\u200b\u2028shot.png"); got != "shot.png" {
		t.Fatalf("DisplayName sanitized = %q, want shot.png", got)
	}
}
