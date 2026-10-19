package relay

import (
	"context"
	"errors"
	"slices"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"google.golang.org/protobuf/proto"
)

// minRequestID is the shortest accepted request ID. Clients use 128
// random bits, 32 hex characters.
const minRequestID = 16

// handleCall opens a forwarded command, runs it at most once, and replies
// with the sealed result.
func (c *conn) handleCall(ctx context.Context, call *pb.ForwardCall) {
	fail := func(msg string) {
		c.reply(ctx, &pb.ForwardReply{CallId: call.CallId, Error: msg})
	}

	if c.security == nil {
		fail("remote security is not initialized")
		return
	}
	wire, err := deviceauth.FromEnvelope(call.Command)
	if err != nil {
		fail("the command is not a valid security envelope")
		return
	}
	header, err := deviceauth.ParseHeader(wire.Header)
	if err != nil || header.Purpose != deviceauth.Command || header.Machine != c.acct.DeviceID || len(header.Request) < minRequestID {
		fail("invalid command scope")
		return
	}
	// An unknown state is fetched outside the acceptance gate and the gate is
	// retried once; the endpoint re-verifies the exact state inside it.
	result, err := c.security.accept(ctx, wire, nil, uint64(time.Now().UnixMilli()), func(h deviceauth.Header, plain []byte) ([]byte, error) {
		cmd := &pb.Command{}
		plain, err := deviceauth.Unpack(h, plain)
		if err != nil || proto.Unmarshal(plain, cmd) != nil {
			return nil, deviceauth.ErrInvalid
		}
		var res *pb.CommandResult
		if cmd.ProtocolVersion != protocol.Version {
			res = &pb.CommandResult{ErrorCode: connect.CodeFailedPrecondition.String(), ErrorMessage: protocol.Mismatch("the client", cmd.ProtocolVersion).(*connect.Error).Message()}
		} else {
			origin := &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: h.Sender, RequestId: h.Request}
			switch k := cmd.Kind.(type) {
			case *pb.Command_ResolveApproval:
				origin.ObservedEventSeq = k.ResolveApproval.ObservedEventSeq
			case *pb.Command_AnswerQuestion:
				origin.ObservedEventSeq = k.AnswerQuestion.ObservedEventSeq
			case *pb.Command_AnswerPlan:
				origin.ObservedEventSeq = k.AnswerPlan.ObservedEventSeq
				origin.ObservedScreenHash = k.AnswerPlan.ObservedScreenHash
			}
			callCtx := agent.WithOrigin(ctx, origin)
			cancel := func() {}
			callCtx, cancel = context.WithDeadline(callCtx, time.UnixMilli(int64(h.Deadline)))
			defer cancel()
			res = c.execute(callCtx, h.Session, cmd)
		}
		return proto.Marshal(res)
	})
	if err != nil {
		fail("the command was refused or its outcome is unknown; it was not repeated")
		return
	}
	env, err := c.security.seal(&pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_COMMAND_RESULT, MachineId: c.acct.DeviceID, SessionId: header.Session, RequestId: header.Request, Sender: c.acct.DeviceID}, deviceauth.Result, nil, result)
	if err != nil {
		fail("cannot authorize the result")
		return
	}
	c.reply(ctx, &pb.ForwardReply{CallId: call.CallId, Result: env})

}

// execute runs a command on the session named in its sealed header.
func (c *conn) execute(ctx context.Context, sessionID string, cmd *pb.Command) *pb.CommandResult {
	r := c.r
	l := r.backend.Local()
	out := &pb.CommandResult{}
	// on checks that a session command names the session its header binds
	// it to, in full, then runs it.
	on := func(id string, run func() error) error {
		if id != sessionID || !slices.Contains(r.backend.SessionIDs(), id) {
			return connect.NewError(connect.CodeNotFound, errors.New("no such session"))
		}
		return run()
	}
	// machine runs a command for the machine; its header names no session.
	machine := func(run func() error) error {
		if sessionID != "" {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("a machine command must not name a session"))
		}
		return run()
	}
	var err error
	switch k := cmd.Kind.(type) {
	case *pb.Command_Send:
		err = on(k.Send.SessionId, func() error {
			return call(ctx, l.Send, k.Send, func(m *pb.SendResponse) { out.Kind = &pb.CommandResult_Send{Send: m} })
		})
	case *pb.Command_Interrupt:
		err = on(k.Interrupt.SessionId, func() error {
			return call(ctx, l.Interrupt, k.Interrupt, func(m *pb.InterruptResponse) { out.Kind = &pb.CommandResult_Interrupt{Interrupt: m} })
		})
	case *pb.Command_ResolveApproval:
		err = on(k.ResolveApproval.SessionId, func() error {
			return call(ctx, l.ResolveApproval, k.ResolveApproval, func(m *pb.ResolveApprovalResponse) {
				out.Kind = &pb.CommandResult_ResolveApproval{ResolveApproval: m}
			})
		})
	case *pb.Command_AnswerQuestion:
		err = on(k.AnswerQuestion.SessionId, func() error {
			return call(ctx, l.AnswerQuestion, k.AnswerQuestion, func(m *pb.AnswerQuestionResponse) {
				out.Kind = &pb.CommandResult_AnswerQuestion{AnswerQuestion: m}
			})
		})
	case *pb.Command_StopSession:
		err = on(k.StopSession.SessionId, func() error {
			return call(ctx, l.StopSession, k.StopSession, func(m *pb.StopSessionResponse) { out.Kind = &pb.CommandResult_StopSession{StopSession: m} })
		})
	case *pb.Command_CancelInput:
		err = on(k.CancelInput.SessionId, func() error {
			return call(ctx, l.CancelInput, k.CancelInput, func(m *pb.CancelInputResponse) { out.Kind = &pb.CommandResult_CancelInput{CancelInput: m} })
		})
	case *pb.Command_ResumeInput:
		err = on(k.ResumeInput.SessionId, func() error {
			return call(ctx, l.ResumeInput, k.ResumeInput, func(m *pb.ResumeInputResponse) { out.Kind = &pb.CommandResult_ResumeInput{ResumeInput: m} })
		})
	case *pb.Command_SetModel:
		err = on(k.SetModel.SessionId, func() error {
			return call(ctx, l.SetModel, k.SetModel, func(m *pb.SetModelResponse) { out.Kind = &pb.CommandResult_SetModel{SetModel: m} })
		})
	case *pb.Command_SetApprovalMode:
		err = on(k.SetApprovalMode.SessionId, func() error {
			return call(ctx, l.SetApprovalMode, k.SetApprovalMode, func(m *pb.SetApprovalModeResponse) {
				out.Kind = &pb.CommandResult_SetApprovalMode{SetApprovalMode: m}
			})
		})
	case *pb.Command_RenameSession:
		err = on(k.RenameSession.SessionId, func() error {
			return call(ctx, l.RenameSession, k.RenameSession, func(m *pb.RenameSessionResponse) {
				out.Kind = &pb.CommandResult_RenameSession{RenameSession: m}
			})
		})
	case *pb.Command_Compact:
		err = on(k.Compact.SessionId, func() error {
			return call(ctx, l.Compact, k.Compact, func(m *pb.CompactResponse) { out.Kind = &pb.CommandResult_Compact{Compact: m} })
		})
	case *pb.Command_AnswerPlan:
		err = on(k.AnswerPlan.SessionId, func() error {
			return call(ctx, l.AnswerPlan, k.AnswerPlan, func(m *pb.AnswerPlanResponse) { out.Kind = &pb.CommandResult_AnswerPlan{AnswerPlan: m} })
		})
	case *pb.Command_OpenTerminal:
		err = on(k.OpenTerminal.SessionId, func() error {
			if err := c.openTerminal(ctx, sessionID, k.OpenTerminal.StreamId); err != nil {
				return connect.NewError(connect.CodeFailedPrecondition, err)
			}
			out.Kind = &pb.CommandResult_OpenTerminal{OpenTerminal: &pb.OpenTerminalResponse{}}
			return nil
		})
	case *pb.Command_ResumeSession:
		err = on(k.ResumeSession.SessionId, func() error {
			return call(ctx, l.ResumeSession, k.ResumeSession, func(m *pb.ResumeSessionResponse) {
				out.Kind = &pb.CommandResult_ResumeSession{ResumeSession: m}
			})
		})
	case *pb.Command_ForkSession:
		err = on(k.ForkSession.SessionId, func() error {
			return call(ctx, l.ForkSession, k.ForkSession, func(m *pb.ForkSessionResponse) {
				out.Kind = &pb.CommandResult_ForkSession{ForkSession: m}
			})
		})
	case *pb.Command_CreateSession:
		err = machine(func() error {
			req := k.CreateSession
			// A remote client's environment means nothing here: the
			// session starts through the machine's login shell, which
			// builds one from its own configuration.
			req.Env = nil
			return call(ctx, l.CreateSession, req, func(m *pb.CreateSessionResponse) { out.Kind = &pb.CommandResult_CreateSession{CreateSession: m} })
		})
	case *pb.Command_ListModels:
		err = machine(func() error {
			return call(ctx, l.ListModels, k.ListModels, func(m *pb.ListModelsResponse) { out.Kind = &pb.CommandResult_ListModels{ListModels: m} })
		})
	case *pb.Command_ListDirectories:
		err = machine(func() error {
			return call(ctx, l.ListDirectories, k.ListDirectories, func(m *pb.ListDirectoriesResponse) {
				out.Kind = &pb.CommandResult_ListDirectories{ListDirectories: m}
			})
		})
	case *pb.Command_GetSettings:
		err = machine(func() error {
			return call(ctx, l.GetSettings, k.GetSettings, func(m *pb.GetSettingsResponse) { out.Kind = &pb.CommandResult_GetSettings{GetSettings: m} })
		})
	case *pb.Command_BeginImageUpload:
		err = machine(func() error {
			return call(ctx, l.BeginImageUpload, k.BeginImageUpload, func(m *pb.BeginImageUploadResponse) {
				out.Kind = &pb.CommandResult_BeginImageUpload{BeginImageUpload: m}
			})
		})
	case *pb.Command_PutImageChunk:
		err = machine(func() error {
			return call(ctx, l.PutImageChunk, k.PutImageChunk, func(m *pb.PutImageChunkResponse) {
				out.Kind = &pb.CommandResult_PutImageChunk{PutImageChunk: m}
			})
		})
	case *pb.Command_GetImageUploadStatus:
		err = machine(func() error {
			return call(ctx, l.GetImageUploadStatus, k.GetImageUploadStatus, func(m *pb.GetImageUploadStatusResponse) {
				out.Kind = &pb.CommandResult_GetImageUploadStatus{GetImageUploadStatus: m}
			})
		})
	case *pb.Command_CompleteImageUpload:
		err = machine(func() error {
			return call(ctx, l.CompleteImageUpload, k.CompleteImageUpload, func(m *pb.CompleteImageUploadResponse) {
				out.Kind = &pb.CommandResult_CompleteImageUpload{CompleteImageUpload: m}
			})
		})
	case *pb.Command_AbortImageUpload:
		err = machine(func() error {
			return call(ctx, l.AbortImageUpload, k.AbortImageUpload, func(m *pb.AbortImageUploadResponse) {
				out.Kind = &pb.CommandResult_AbortImageUpload{AbortImageUpload: m}
			})
		})
	case *pb.Command_GetLiveSnapshot:
		err = on(k.GetLiveSnapshot.SessionId, func() error {
			owner, ok := r.backend.(interface {
				LiveSnapshot(string) (*pb.LiveSnapshot, error)
			})
			if !ok {
				return connect.NewError(connect.CodeFailedPrecondition, errors.New("live owner snapshot unavailable"))
			}
			snapshot, err := owner.LiveSnapshot(sessionID)
			if err != nil {
				return err
			}
			if proto.Size(snapshot) > 512<<10 {
				return connect.NewError(connect.CodeResourceExhausted, errors.New("live snapshot too large; use native terminal"))
			}
			out.Kind = &pb.CommandResult_LiveSnapshot{LiveSnapshot: snapshot}
			return nil
		})

	case *pb.Command_Schedule:
		err = machine(func() error {
			return call(ctx, l.Schedule, k.Schedule, func(m *pb.ScheduleResponse) {
				out.Kind = &pb.CommandResult_Schedule{Schedule: m}
			})
		})
	case *pb.Command_UpdateSettings:
		err = machine(func() error {
			return call(ctx, l.UpdateSettings, k.UpdateSettings, func(m *pb.UpdateSettingsResponse) {
				out.Kind = &pb.CommandResult_UpdateSettings{UpdateSettings: m}
			})
		})
	default:
		err = connect.NewError(connect.CodeUnimplemented, errors.New("this daemon does not know the command; update remote-agent"))
	}
	if err != nil {
		out.Kind = nil
		out.ErrorCode = connect.CodeOf(err).String()
		var ce *connect.Error
		if errors.As(err, &ce) {
			out.ErrorMessage = ce.Message()
		} else {
			out.ErrorMessage = err.Error()
		}
	}
	return out
}

// call runs one local API method and hands its response to set.
func call[Q, R any](ctx context.Context, fn func(context.Context, *connect.Request[Q]) (*connect.Response[R], error), req *Q, set func(*R)) error {
	res, err := fn(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	set(res.Msg)
	return nil
}
