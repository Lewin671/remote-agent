package screens

import (
	"github.com/Lewin671/remote-agent/clientcore/domain/attachments"
	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

// machineOnlineReady unifies live send/attachment reachability across session header/actions,
// new_session, and BuildAttachmentEval, consistently honoring DisplayGrace and Removed.
func machineOnlineReady(
	conn lists.ConnectionKind,
	displayGrace bool,
	computerOnline bool,
	unlinked bool,
	protocolMismatch bool,
	accountControlLost bool,
	removed bool,
) bool {
	return (conn == lists.ConnConnected || displayGrace) &&
		computerOnline && !unlinked && !protocolMismatch && !accountControlLost && !removed
}

// BuildAttachmentEval computes attachment capability limits, picked-candidate verdicts,
// and image problem classification / failure summary for a target machine or session.
func BuildAttachmentEval(st *State, req *corev1.AttachmentEvalScreenRequest) *corev1.AttachmentEvalViewModel {
	if st == nil {
		st = NewState(nil)
	}
	if req == nil {
		req = &corev1.AttachmentEvalScreenRequest{}
	}

	machineID := req.MachineId
	if machineID == "" && req.SessionId != "" {
		if ls := findSessionEntry(st, req.SessionId); ls != nil {
			machineID = ls.MachineID
		}
	}
	lm := findMachine(st, machineID)
	computerOnline := lists.MachineReachable(lm) || (st.DisplayGrace && lm != nil && lm.Online)
	unlinked := !st.DisplayGrace && (lists.WorldUnlinked(&st.World) || lists.MachineUnlinked(lm, &st.World))
	protoMismatch := lm != nil && lists.ProtocolMismatchDirection(lm, st.World.ClientVersion) != 0
	onlineReady := machineOnlineReady(
		st.World.Connection,
		st.DisplayGrace,
		computerOnline,
		unlinked,
		protoMismatch,
		st.World.AccountControlLost,
		st.Removed,
	)

	if req.PutBack {
		computerOnline, onlineReady = true, true
	}
	caps := attachments.ResolveCaps(lm, machineID, computerOnline, onlineReady)

	imagesAllowed := caps.SupportsImages && caps.ImageUploadBlockedReason == nil
	filesAllowed := caps.SupportsFiles && caps.FileUploadBlockedReason == nil
	room := attachments.LibraryRoom(req.CurrentImages, caps.MaxImages, req.CurrentFiles, caps.MaxFiles, imagesAllowed, filesAllowed)

	var galleryBlocked *corev1.Text
	if !imagesAllowed && !filesAllowed {
		if caps.ImageUploadBlockedReason != nil {
			galleryBlocked = caps.ImageUploadBlockedReason
		} else {
			galleryBlocked = caps.FileUploadBlockedReason
		}
	} else if room == 0 {
		if imagesAllowed {
			galleryBlocked = text.Msg(text.KeyAttachmentMaxImages, text.Int("count", int64(caps.MaxImages)))
		} else {
			galleryBlocked = text.Msg(text.KeyAttachmentMaxFiles, text.Int("count", int64(caps.MaxFiles)))
		}
	}

	canPickImages := imagesAllowed && req.CurrentImages < caps.MaxImages
	canPickFiles := filesAllowed && req.CurrentFiles < caps.MaxFiles
	imagesPickBlocked := pickBlockedReason(canPickImages, caps.SupportsImages, caps.ImageUploadBlockedReason, text.KeyAttachmentMaxImages, caps.MaxImages)
	filesPickBlocked := pickBlockedReason(canPickFiles, caps.SupportsFiles, caps.FileUploadBlockedReason, text.KeyAttachmentMaxFiles, caps.MaxFiles)

	decisions, notice := attachments.EvaluateCandidates(caps, req.CurrentImages, req.CurrentFiles, req.Candidates)

	var problems []*corev1.ImageProblemView
	if len(req.Problems) > 0 {
		problems = make([]*corev1.ImageProblemView, 0, len(req.Problems))
		for _, p := range req.Problems {
			problems = append(problems, attachments.ClassifyImageProblem(p))
		}
	}
	summary := attachments.SummarizeImageFailures(problems)

	return &corev1.AttachmentEvalViewModel{
		CapabilitiesLoaded:       caps.CapabilitiesLoaded,
		SupportsImages:           caps.SupportsImages,
		SupportsFiles:            caps.SupportsFiles,
		MaxImages:                caps.MaxImages,
		MaxFiles:                 caps.MaxFiles,
		MaxFileBytes:             caps.MaxFileBytes,
		MaxFileNameBytes:         attachments.MaxFileNameUTF8Bytes,
		MaxFileSizeText:          caps.MaxFileSizeText,
		OriginalImageMaxBytes:    attachments.OriginalImageMaxBytes,
		GalleryAllowsVideo:       caps.SupportsFiles,
		GalleryAcceptMime:        attachments.GalleryAcceptMime(caps.SupportsFiles),
		LibraryRoom:              room,
		CanPickImages:            canPickImages,
		CanPickFiles:             canPickFiles,
		CanPickGallery:           (imagesAllowed || filesAllowed) && room > 0,
		ImageUploadBlockedReason: caps.ImageUploadBlockedReason,
		FileUploadBlockedReason:  caps.FileUploadBlockedReason,
		GalleryBlockedReason:     galleryBlocked,
		OriginalImageHint:        text.Msg(text.KeyImageOriginalHint),
		FileMetadataNotice:       text.Msg(text.KeyAttachmentMetadata),
		Decisions:                decisions,
		Notice:                   notice,
		Problems:                 problems,
		ImagesPickBlockedReason:  imagesPickBlocked,
		FilesPickBlockedReason:   filesPickBlocked,
		FailureSummary:           summary,
		OriginalTotalSizeText:    attachments.FormatOriginalTotalSize(req.TotalImageBytes),
		DraftViewerPositionText:  attachments.DraftViewerPositionText(req.DraftViewerIndex, req.CurrentImages),
	}
}

// pickBlockedReason explains why a picker is disabled: the machine-level reason when the
// machine cannot accept that kind, otherwise the per-message limit. Nil when picking is
// allowed or the kind is unsupported without a reason (the shell hides the picker then).
func pickBlockedReason(canPick, supported bool, capReason *corev1.Text, limitKey string, limit int32) *corev1.Text {
	switch {
	case canPick:
		return nil
	case capReason != nil:
		return capReason
	case !supported:
		return nil
	default:
		return text.Msg(limitKey, text.Int("count", int64(limit)))
	}
}
