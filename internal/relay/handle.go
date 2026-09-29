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
	sid := func(id string) error {
		// Remote commands name sessions in full; the header binds them.
		if id != sessionID || !slices.Contains(r.backend.SessionIDs(), id) {
			return connect.NewError(connect.CodeNotFound, errors.New("no such session"))
		}
		return nil
	}
	l := r.backend.Local()
	out := &pb.CommandResult{}
	var err error
	switch k := cmd.Kind.(type) {
	case *pb.Command_Send:
		if err = sid(k.Send.SessionId); err == nil {
			var res *connect.Response[pb.SendResponse]
			if res, err = l.Send(ctx, connect.NewRequest(k.Send)); err == nil {
				out.Kind = &pb.CommandResult_Send{Send: res.Msg}
			}
		}
	case *pb.Command_Interrupt:
		if err = sid(k.Interrupt.SessionId); err == nil {
			var res *connect.Response[pb.InterruptResponse]
			if res, err = l.Interrupt(ctx, connect.NewRequest(k.Interrupt)); err == nil {
				out.Kind = &pb.CommandResult_Interrupt{Interrupt: res.Msg}
			}
		}
	case *pb.Command_ResolveApproval:
		if err = sid(k.ResolveApproval.SessionId); err == nil {
			var res *connect.Response[pb.ResolveApprovalResponse]
			if res, err = l.ResolveApproval(ctx, connect.NewRequest(k.ResolveApproval)); err == nil {
				out.Kind = &pb.CommandResult_ResolveApproval{ResolveApproval: res.Msg}
			}
		}
	case *pb.Command_AnswerQuestion:
		if err = sid(k.AnswerQuestion.SessionId); err == nil {
			var res *connect.Response[pb.AnswerQuestionResponse]
			if res, err = l.AnswerQuestion(ctx, connect.NewRequest(k.AnswerQuestion)); err == nil {
				out.Kind = &pb.CommandResult_AnswerQuestion{AnswerQuestion: res.Msg}
			}
		}
	case *pb.Command_StopSession:
		if err = sid(k.StopSession.SessionId); err == nil {
			var res *connect.Response[pb.StopSessionResponse]
			if res, err = l.StopSession(ctx, connect.NewRequest(k.StopSession)); err == nil {
				out.Kind = &pb.CommandResult_StopSession{StopSession: res.Msg}
			}
		}
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
