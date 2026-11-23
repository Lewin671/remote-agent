package screens

import (
	"github.com/Lewin671/remote-agent/clientcore/domain/attachments"
	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

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
	computerOnline := lists.MachineReachable(lm)
	unlinked := lists.WorldUnlinked(&st.World) || lists.MachineUnlinked(lm, &st.World)
	protoMismatch := lm != nil && lists.ProtocolMismatchDirection(lm, st.World.ClientVersion) != 0
	onlineReady := (st.World.Connection == lists.ConnConnected || st.DisplayGrace) &&
		computerOnline && !unlinked && !protoMismatch && !st.World.AccountControlLost

	caps := attachments.ResolveCaps(lm, machineID, computerOnline, onlineReady, st.UILocale)

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

	decisions, notice := attachments.EvaluateCandidates(caps, req.CurrentImages, req.CurrentFiles, req.Candidates, st.UILocale)

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
		CanPickImages:            imagesAllowed && req.CurrentImages < caps.MaxImages,
		CanPickFiles:             filesAllowed && req.CurrentFiles < caps.MaxFiles,
		CanPickGallery:           (imagesAllowed || filesAllowed) && room > 0,
		ImageUploadBlockedReason: caps.ImageUploadBlockedReason,
		FileUploadBlockedReason:  caps.FileUploadBlockedReason,
		GalleryBlockedReason:     galleryBlocked,
		OriginalImageHint:        text.Msg(text.KeyImageOriginalHint),
		FileMetadataNotice:       text.Msg(text.KeyAttachmentMetadata),
		Decisions:                decisions,
		Notice:                   notice,
		Problems:                 problems,
		FailureSummary:           summary,
	}
}
