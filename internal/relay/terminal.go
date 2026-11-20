package relay

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/transport"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"google.golang.org/protobuf/proto"
)

// Terminal is a live view of a session's terminal; see daemon.TermStream.
type Terminal interface {
	Snapshot() (*pb.TerminalSnapshot, error)
	History(*pb.TerminalHistoryRequest) (*pb.TerminalHistoryPage, error)
	Output() <-chan []byte
	SizeChanged() <-chan struct{}
	Done() <-chan struct{}
	Reason() string
	Input(b []byte) error
	Resize(cols, rows int, fit bool) error
	Close() error
}

// termBatch bounds how much output goes into one frame.
const termBatch = 64 << 10

// openTerminal joins the stream a client asked for and serves it in the
// background until either side leaves.
func (c *endpoint) openTerminal(ctx context.Context, sessionID, streamID string) error {
	if len(streamID) < minRequestID {
		return errors.New("invalid stream ID")
	}
	lifetime := c.lifetime
	if lifetime == nil {
		lifetime = ctx
	}
	peer := agent.OriginFrom(ctx).GetDeviceId()
	streamCtx, streamCancel, err := c.security.streamContext(lifetime, peer)
	if err != nil {
		return err
	}
	established := false
	defer func() {
		if !established {
			streamCancel()
		}
	}()
	opening, cancelOpening := context.WithCancel(ctx)
	defer cancelOpening()
	stopOpening := context.AfterFunc(streamCtx, cancelOpening)
	defer stopOpening()
	ctx = opening
	key, err := c.sessionKey(sessionID, true)
	if err != nil {
		return err
	}
	t, err := c.r.backend.OpenTerminal(sessionID)
	if err != nil {
		return err
	}
	if err := c.security.confirmStream(streamCtx); err != nil {
		t.Close()
		return err
	}
	path, err := c.link.Router.OpenStream(ctx, transport.StreamRoute{ID: streamID})
	if err != nil {
		t.Close()
		return err
	}
	if err := c.security.confirmStream(streamCtx); err != nil {
		t.Close()
		path.Close()
		return err
	}
	if !c.link.ServeStream(func() { defer streamCancel(); c.serveTerminal(streamCtx, path, t, key, sessionID, streamID) }) {
		t.Close()
		path.Close()
		return context.Canceled
	}
	established = true
	return nil
}

func (c *endpoint) serveTerminal(ctx context.Context, path transport.Path, t Terminal, key []byte, sessionID, streamID string) {
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() {
		cancel()
		path.Close()
		workers.Wait()
		t.Close()
	}()

	var seq uint64
	send := func(f *pb.TerminalFrame) error {
		if err := c.security.confirmStream(ctx); err != nil {
			return err
		}
		seq++
		env, err := c.security.sealMessage(&pb.Header{
			Kind: pb.PayloadKind_PAYLOAD_KIND_TERMINAL_OUTPUT, MachineId: c.acct.DeviceID, SessionId: sessionID,
			RequestId: streamID, Seq: seq, Sender: c.acct.DeviceID,
		}, devicesec.TerminalOutput, key, f)
		if err != nil {
			return err
		}
		b, err := proto.Marshal(env)
		if err != nil {
			return err
		}
		wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
		defer wcancel()
		return path.Write(wctx, b)
	}

	snap, err := t.Snapshot()
	if err != nil {
		_ = send(&pb.TerminalFrame{Kind: &pb.TerminalFrame_Closed{Closed: "cannot read the screen"}})
		return
	}
	if err := send(&pb.TerminalFrame{Kind: &pb.TerminalFrame_Snapshot{Snapshot: snap}}); err != nil {
		return
	}
	historyRequests := make(chan *pb.TerminalHistoryRequest, 1)
	historyPages := make(chan *pb.TerminalHistoryPage, 1)
	workers.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case req := <-historyRequests:
				page, err := t.History(req)
				if err != nil {
					page = &pb.TerminalHistoryPage{RequestId: req.RequestId, Error: "cannot read terminal history"}
				}
				select {
				case historyPages <- page:
				case <-ctx.Done():
					return
				}
			}
		}
	})
	workers.Go(func() {
		defer cancel()
		c.readTerminal(ctx, path, t, key, sessionID, streamID, historyRequests)
	})
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.Done():
			_ = send(&pb.TerminalFrame{Kind: &pb.TerminalFrame_Closed{Closed: t.Reason()}})
			path.Finish()
			return
		case page := <-historyPages:
			if err := send(&pb.TerminalFrame{Kind: &pb.TerminalFrame_HistoryPage{HistoryPage: page}}); err != nil {
				return
			}
		case <-t.SizeChanged():
			snap, err := t.Snapshot()
			if err != nil {
				return
			}
			if err := send(&pb.TerminalFrame{Kind: &pb.TerminalFrame_Snapshot{Snapshot: snap}}); err != nil {
				return
			}
		case b := <-t.Output():
			// Send what has piled up as one frame.
			for more := true; more && len(b) < termBatch; {
				select {
				case next := <-t.Output():
					b = append(b, next...)
				default:
					more = false
				}
			}
			if err := send(&pb.TerminalFrame{Kind: &pb.TerminalFrame_Output{Output: b}}); err != nil {
				return
			}
		}
	}
}

// readTerminal applies the client's sealed input frames in order.
func (c *endpoint) readTerminal(ctx context.Context, path transport.Path, t Terminal, key []byte, sessionID, streamID string, historyRequests chan<- *pb.TerminalHistoryRequest) {
	var last uint64
	for {
		b, err := path.Read(ctx)
		if err != nil {
			return
		}
		env := &pb.Envelope{}
		if proto.Unmarshal(b, env) != nil {
			return
		}

		wire, err := deviceauth.FromEnvelope(env)
		if err != nil {
			return
		}
		h, err := devicesec.ParseHeader(wire.Header)
		if err != nil || h.Machine != c.acct.DeviceID || h.Session != sessionID || h.Request != streamID || h.Seq <= last || !h.Purpose.Control() || h.Purpose == devicesec.Command {
			return
		}
		_, err = c.security.accept(ctx, wire, key, uint64(time.Now().UnixMilli()), func(h devicesec.Header, plain []byte) (deviceauth.Completion, error) {
			plain, err := deviceauth.Unpack(h, plain)
			if err != nil {
				return deviceauth.Undetermined(), err
			}
			f := &pb.TerminalFrame{}
			if proto.Unmarshal(plain, f) != nil {
				return deviceauth.Undetermined(), devicesec.ErrInvalid
			}
			switch k := f.Kind.(type) {
			case *pb.TerminalFrame_Input:
				if err := t.Input(k.Input); err != nil {
					return deviceauth.Undetermined(), err
				}
				return deviceauth.Final(nil), nil
			case *pb.TerminalFrame_Resize:
				if err := t.Resize(int(k.Resize.Cols), int(k.Resize.Rows), k.Resize.Fit); err != nil {
					return deviceauth.Undetermined(), err
				}
				return deviceauth.Final(nil), nil
			case *pb.TerminalFrame_HistoryRequest:
				if k.HistoryRequest == nil || len(k.HistoryRequest.RequestId) == 0 || len(k.HistoryRequest.RequestId) > 128 || len(k.HistoryRequest.Cursor) > 128 {
					return deviceauth.Undetermined(), devicesec.ErrInvalid
				}
				select {
				case historyRequests <- k.HistoryRequest:
					return deviceauth.Final(nil), nil
				default:
					return deviceauth.Undetermined(), devicesec.ErrCapacity
				}
			default:
				return deviceauth.Undetermined(), devicesec.ErrInvalid
			}
		})
		if err == nil {
			last = h.Seq
		}

		if err != nil {
			slog.Warn("relay: terminal input", "stream", streamID[:8], "err", err)
		}
	}
}
