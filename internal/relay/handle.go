package relay

import (
	"context"
	"errors"
	"slices"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/e2e"
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
	h, plain, err := e2e.Open(c.keys.Command, call.Command)
	switch {
	case errors.Is(err, e2e.ErrVersion):
		fail("unsupported envelope version")
		return
	case err != nil:
		fail("the command does not open with this account's key")
		return
	case e2e.Expect(h, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_COMMAND, MachineId: c.acct.DeviceID}) != nil:
		fail("the command is addressed to another machine")
		return
	case len(h.RequestId) < minRequestID:
		fail("the command has no valid request ID")
		return
	}

	var reject *pb.CommandResult
	now := time.Now()
	switch deadline := time.UnixMilli(h.DeadlineMs); {
	case !now.Before(deadline):
		reject = &pb.CommandResult{ErrorCode: connect.CodeDeadlineExceeded.String(), ErrorMessage: "the command expired before it reached the machine"}
	case deadline.After(now.Add(maxAhead)):
		reject = &pb.CommandResult{ErrorCode: connect.CodeInvalidArgument.String(), ErrorMessage: "the command's deadline is too far ahead; check the device clocks"}
	}
	res := c.r.cmds.do(h.RequestId, h.DeadlineMs, reject, func() *pb.CommandResult {
		cmd := &pb.Command{}
		if err := proto.Unmarshal(plain, cmd); err != nil {
			return &pb.CommandResult{ErrorCode: connect.CodeInvalidArgument.String(), ErrorMessage: "malformed command"}
		}
		return c.r.execute(ctx, h.SessionId, cmd)
	})
	env, err := e2e.SealMessage(c.keys.Command, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_COMMAND_RESULT, MachineId: c.acct.DeviceID, SessionId: h.SessionId,
		RequestId: h.RequestId, Sender: c.acct.DeviceID,
	}, res)
	if err != nil {
		fail("cannot seal the result")
		return
	}
	c.reply(ctx, &pb.ForwardReply{CallId: call.CallId, Result: env})
}

// execute runs a command on the session named in its sealed header.
func (r *Relay) execute(ctx context.Context, sessionID string, cmd *pb.Command) *pb.CommandResult {
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
	case *pb.Command_Compact:
		err = on(k.Compact.SessionId, func() error {
			return call(ctx, l.Compact, k.Compact, func(m *pb.CompactResponse) { out.Kind = &pb.CommandResult_Compact{Compact: m} })
		})
	case *pb.Command_AnswerPlan:
		err = on(k.AnswerPlan.SessionId, func() error {
			return call(ctx, l.AnswerPlan, k.AnswerPlan, func(m *pb.AnswerPlanResponse) { out.Kind = &pb.CommandResult_AnswerPlan{AnswerPlan: m} })
		})
	case *pb.Command_CreateSession:
		err = machine(func() error {
			req := k.CreateSession
			// A remote client's environment means nothing here; the
			// session gets the daemon's.
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
