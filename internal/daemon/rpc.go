package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/agent"
)

type handler struct {
	remoteagentv1connect.UnimplementedLocalServiceHandler
	d *Daemon
}

func rpcErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, errExited), errors.Is(err, errExpired), errors.Is(err, errNotAnswering), errors.Is(err, errWriting), errors.Is(err, errRunning),
		errors.Is(err, errBusy), errors.Is(err, errNotReady), errors.Is(err, errNoPlan), errors.Is(err, agent.ErrUnsupported), errors.Is(err, agent.ErrBackground):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return err
	}
	return connect.NewError(connect.CodeUnknown, err)
}

func (h *handler) CreateSession(ctx context.Context, req *connect.Request[pb.CreateSessionRequest]) (*connect.Response[pb.CreateSessionResponse], error) {
	s, err := h.d.createBy(req.Msg, originOf(ctx))
	var ce *connect.Error
	if errors.As(err, &ce) {
		return nil, err
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&pb.CreateSessionResponse{Session: s.info()}), nil
}

func (h *handler) ListSessions(ctx context.Context, req *connect.Request[pb.ListSessionsRequest]) (*connect.Response[pb.ListSessionsResponse], error) {
	return connect.NewResponse(&pb.ListSessionsResponse{Sessions: h.d.list(req.Msg.IncludeExited)}), nil
}

func (h *handler) GetSession(ctx context.Context, req *connect.Request[pb.GetSessionRequest]) (*connect.Response[pb.GetSessionResponse], error) {
	s, err := h.d.session(req.Msg.SessionId)
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&pb.GetSessionResponse{Session: s.info()}), nil
}

func (h *handler) Send(ctx context.Context, req *connect.Request[pb.SendRequest]) (*connect.Response[pb.SendResponse], error) {
	if req.Msg.Text == "" && len(req.Msg.ImageIds) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("text is empty"))
	}
	s, err := h.d.session(req.Msg.SessionId)
	if err != nil {
		return nil, rpcErr(err)
	}
	origin := originOf(ctx)
	var images []queuedImage
	if len(req.Msg.ImageIds) > 0 {
		owner, err := imageOwner(origin)
		if err != nil {
			return nil, err
		}
		if images, err = h.d.uploads.take(req.Msg.ImageIds, owner, s.meta.ID); err != nil {
			return nil, err
		}
	}
	pos, err := s.enqueue(req.Msg.Text, req.Msg.RequestId, images, origin)
	if err != nil {
		h.d.uploads.untake(images, s.meta.ID)
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&pb.SendResponse{QueuePosition: int32(pos)}), nil
}

func (h *handler) Interrupt(ctx context.Context, req *connect.Request[pb.InterruptRequest]) (*connect.Response[pb.InterruptResponse], error) {
	s, err := h.d.session(req.Msg.SessionId)
	if err != nil {
		return nil, rpcErr(err)
	}
	ok, err := s.interruptBy(originOf(ctx))
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&pb.InterruptResponse{Interrupted: ok}), nil
}

func (h *handler) StopSession(ctx context.Context, req *connect.Request[pb.StopSessionRequest]) (*connect.Response[pb.StopSessionResponse], error) {
	s, err := h.d.session(req.Msg.SessionId)
	if err != nil {
		return nil, rpcErr(err)
	}
	if err := s.kill(); err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&pb.StopSessionResponse{}), nil
}

func (h *handler) ResolveApproval(ctx context.Context, req *connect.Request[pb.ResolveApprovalRequest]) (*connect.Response[pb.ResolveApprovalResponse], error) {
	s, err := h.d.session(req.Msg.SessionId)
	if err != nil {
		return nil, rpcErr(err)
	}
	if err := s.resolveBy(req.Msg.ApprovalId, req.Msg.Allow, req.Msg.Message, originOf(ctx)); err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&pb.ResolveApprovalResponse{}), nil
}

func (h *handler) AnswerQuestion(ctx context.Context, req *connect.Request[pb.AnswerQuestionRequest]) (*connect.Response[pb.AnswerQuestionResponse], error) {
	s, err := h.d.session(req.Msg.SessionId)
	if err != nil {
		return nil, rpcErr(err)
	}
	options := req.Msg.Options
	if len(options) == 0 {
		options = []int32{req.Msg.Option}
	}
	if err := s.answerBy(req.Msg.QuestionId, options, req.Msg.QuestionIndex, originOf(ctx)); err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&pb.AnswerQuestionResponse{}), nil
}

func (h *handler) WatchEvents(ctx context.Context, req *connect.Request[pb.WatchEventsRequest], stream *connect.ServerStream[pb.WatchEventsResponse]) error {
	s, err := h.d.session(req.Msg.SessionId)
	if err != nil {
		return rpcErr(err)
	}
	after := req.Msg.AfterSeq
	for {
		evs, changed := s.log.Since(after)
		for _, e := range evs {
			if err := stream.Send(&pb.WatchEventsResponse{Event: e}); err != nil {
				return err
			}
			after = e.Seq
		}
		if !req.Msg.Follow {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-changed:
		}
	}
}

func (h *handler) Hook(ctx context.Context, req *connect.Request[pb.HookRequest]) (*connect.Response[pb.HookResponse], error) {
	s, err := h.d.session(req.Msg.SessionId)
	if err != nil {
		return nil, rpcErr(err)
	}
	var p agent.Hook
	if err := json.Unmarshal(req.Msg.Payload, &p); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	out, err := s.hook(ctx, &p)
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&pb.HookResponse{Output: out}), nil
}

func (h *handler) CancelInput(ctx context.Context, req *connect.Request[pb.CancelInputRequest]) (*connect.Response[pb.CancelInputResponse], error) {
	s, err := h.d.session(req.Msg.SessionId)
	if err != nil {
		return nil, rpcErr(err)
	}
	if err := s.cancelInputBy(req.Msg.RequestId, originOf(ctx)); err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&pb.CancelInputResponse{}), nil
}

func (h *handler) SetModel(ctx context.Context, req *connect.Request[pb.SetModelRequest]) (*connect.Response[pb.SetModelResponse], error) {
	s, err := h.d.session(req.Msg.SessionId)
	if err != nil {
		return nil, rpcErr(err)
	}
	if err := s.setModelBy(req.Msg.Model, req.Msg.Effort, originOf(ctx)); err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&pb.SetModelResponse{}), nil
}

func (h *handler) ListModels(ctx context.Context, req *connect.Request[pb.ListModelsRequest]) (*connect.Response[pb.ListModelsResponse], error) {
	res, err := h.d.listModels(req.Msg.Agent)
	if errors.Is(err, agent.ErrUnsupported) {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(res), nil
}

func (h *handler) SetApprovalMode(ctx context.Context, req *connect.Request[pb.SetApprovalModeRequest]) (*connect.Response[pb.SetApprovalModeResponse], error) {
	s, err := h.d.session(req.Msg.SessionId)
	if err != nil {
		return nil, rpcErr(err)
	}
	if err := s.setApprovalModeBy(req.Msg.Mode, originOf(ctx)); err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&pb.SetApprovalModeResponse{}), nil
}

func (h *handler) Compact(ctx context.Context, req *connect.Request[pb.CompactRequest]) (*connect.Response[pb.CompactResponse], error) {
	s, err := h.d.session(req.Msg.SessionId)
	if err != nil {
		return nil, rpcErr(err)
	}
	if err := s.compact(); err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&pb.CompactResponse{}), nil
}

func (h *handler) AnswerPlan(ctx context.Context, req *connect.Request[pb.AnswerPlanRequest]) (*connect.Response[pb.AnswerPlanResponse], error) {
	s, err := h.d.session(req.Msg.SessionId)
	if err != nil {
		return nil, rpcErr(err)
	}
	if err := s.answerPlanBy(req.Msg.Choice, originOf(ctx)); err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&pb.AnswerPlanResponse{}), nil
}

func (h *handler) ListDirectories(ctx context.Context, req *connect.Request[pb.ListDirectoriesRequest]) (*connect.Response[pb.ListDirectoriesResponse], error) {
	res, err := listDirectories(req.Msg.Path)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(res), nil
}

func (h *handler) GetSettings(ctx context.Context, req *connect.Request[pb.GetSettingsRequest]) (*connect.Response[pb.GetSettingsResponse], error) {
	return connect.NewResponse(&pb.GetSettingsResponse{Settings: h.d.settings()}), nil
}

func (h *handler) UpdateSettings(ctx context.Context, req *connect.Request[pb.UpdateSettingsRequest]) (*connect.Response[pb.UpdateSettingsResponse], error) {
	if req.Msg.Settings == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("settings are required"))
	}
	st, err := h.d.updateSettings(req.Msg.Settings)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&pb.UpdateSettingsResponse{Settings: st}), nil
}

func (h *handler) ResumeSession(ctx context.Context, req *connect.Request[pb.ResumeSessionRequest]) (*connect.Response[pb.ResumeSessionResponse], error) {
	s, err := h.d.session(req.Msg.SessionId)
	if err != nil {
		return nil, rpcErr(err)
	}
	if _, err := h.d.resume(s.meta.ID); err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&pb.ResumeSessionResponse{Session: s.info()}), nil
}

func (h *handler) BeginImageUpload(ctx context.Context, req *connect.Request[pb.BeginImageUploadRequest]) (*connect.Response[pb.BeginImageUploadResponse], error) {
	owner, err := imageOwner(originOf(ctx))
	if err != nil {
		return nil, err
	}
	st, err := h.d.uploads.begin(req.Msg, owner)
	if err != nil {
		return nil, uploadErr(err)
	}
	return connect.NewResponse(&pb.BeginImageUploadResponse{Status: st}), nil
}

func (h *handler) PutImageChunk(ctx context.Context, req *connect.Request[pb.PutImageChunkRequest]) (*connect.Response[pb.PutImageChunkResponse], error) {
	owner, err := imageOwner(originOf(ctx))
	if err != nil {
		return nil, err
	}
	st, err := h.d.uploads.put(req.Msg, owner)
	if err != nil {
		return nil, uploadErr(err)
	}
	return connect.NewResponse(&pb.PutImageChunkResponse{Status: st}), nil
}

func (h *handler) GetImageUploadStatus(ctx context.Context, req *connect.Request[pb.GetImageUploadStatusRequest]) (*connect.Response[pb.GetImageUploadStatusResponse], error) {
	owner, err := imageOwner(originOf(ctx))
	if err != nil {
		return nil, err
	}
	st, err := h.d.uploads.status(req.Msg.TransferId, owner)
	if err != nil {
		return nil, uploadErr(err)
	}
	return connect.NewResponse(&pb.GetImageUploadStatusResponse{Status: st}), nil
}

func (h *handler) CompleteImageUpload(ctx context.Context, req *connect.Request[pb.CompleteImageUploadRequest]) (*connect.Response[pb.CompleteImageUploadResponse], error) {
	owner, err := imageOwner(originOf(ctx))
	if err != nil {
		return nil, err
	}
	st, err := h.d.uploads.complete(req.Msg.TransferId, owner)
	if err != nil {
		return nil, uploadErr(err)
	}
	return connect.NewResponse(&pb.CompleteImageUploadResponse{Status: st}), nil
}

func (h *handler) AbortImageUpload(ctx context.Context, req *connect.Request[pb.AbortImageUploadRequest]) (*connect.Response[pb.AbortImageUploadResponse], error) {
	owner, err := imageOwner(originOf(ctx))
	if err != nil {
		return nil, err
	}
	if err := h.d.uploads.abort(req.Msg.TransferId, owner); err != nil {
		return nil, uploadErr(err)
	}
	return connect.NewResponse(&pb.AbortImageUploadResponse{}), nil
}

// uploadErr keeps the codes of upload errors and hides the others, which
// may name files.
func uploadErr(err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return err
	}
	slog.Error("image upload", "err", errClass(err))
	return connect.NewError(connect.CodeInternal, errors.New("the machine could not store the image"))
}
