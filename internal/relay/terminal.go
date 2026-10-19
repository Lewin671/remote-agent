package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/coder/websocket"
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
func (c *conn) openTerminal(ctx context.Context, sessionID, streamID string) error {
	if len(streamID) < minRequestID {
		return errors.New("invalid stream ID")
	}
	key, err := c.sessionKey(sessionID, true)
	if err != nil {
		return err
	}
	t, err := c.r.backend.OpenTerminal(sessionID)
	if err != nil {
		return err
	}
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(c.acct.Server, "http")+"/v1/term", &websocket.DialOptions{HTTPClient: httpClient()})
	if err != nil {
		t.Close()
		return fmt.Errorf("join the terminal stream: %w", err)
	}
	ws.SetReadLimit(1 << 20)
	hello, _ := proto.Marshal(&pb.TermHello{Token: c.token, StreamId: streamID})
	if err := ws.Write(ctx, websocket.MessageBinary, hello); err != nil {
		t.Close()
		ws.CloseNow()
		return err
	}
	lifetime := c.lifetime
	if lifetime == nil {
		lifetime = ctx
	}
	go c.serveTerminal(lifetime, ws, t, key, sessionID, streamID)
	return nil
}

func (c *conn) serveTerminal(ctx context.Context, ws *websocket.Conn, t Terminal, key []byte, sessionID, streamID string) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer ws.CloseNow()
	defer t.Close()

	var seq uint64
	send := func(f *pb.TerminalFrame) error {
		seq++
		env, err := c.security.sealMessage(&pb.Header{
			Kind: pb.PayloadKind_PAYLOAD_KIND_TERMINAL_OUTPUT, MachineId: c.acct.DeviceID, SessionId: sessionID,
			RequestId: streamID, Seq: seq, Sender: c.acct.DeviceID,
		}, deviceauth.TerminalOutput, key, f)
		if err != nil {
			return err
		}
		b, err := proto.Marshal(env)
		if err != nil {
			return err
		}
		wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
		defer wcancel()
		return ws.Write(wctx, websocket.MessageBinary, b)
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
	go func() {
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
	}()
	go func() {
		defer cancel()
		c.readTerminal(ctx, ws, t, key, sessionID, streamID, historyRequests)
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.Done():
			_ = send(&pb.TerminalFrame{Kind: &pb.TerminalFrame_Closed{Closed: t.Reason()}})
			ws.Close(websocket.StatusNormalClosure, "")
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
func (c *conn) readTerminal(ctx context.Context, ws *websocket.Conn, t Terminal, key []byte, sessionID, streamID string, historyRequests chan<- *pb.TerminalHistoryRequest) {
	var last uint64
	for {
		typ, b, err := ws.Read(ctx)
		if err != nil || typ != websocket.MessageBinary {
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
		h, err := deviceauth.ParseHeader(wire.Header)
		if err != nil || h.Machine != c.acct.DeviceID || h.Session != sessionID || h.Request != streamID || h.Seq <= last || !h.Purpose.Control() || h.Purpose == deviceauth.Command {
			return
		}
		_, err = c.security.accept(ctx, wire, key, uint64(time.Now().UnixMilli()), func(h deviceauth.Header, plain []byte) ([]byte, error) {
			plain, err := deviceauth.Unpack(h, plain)
			if err != nil {
				return nil, err
			}
			f := &pb.TerminalFrame{}
			if proto.Unmarshal(plain, f) != nil {
				return nil, deviceauth.ErrInvalid
			}
			switch k := f.Kind.(type) {
			case *pb.TerminalFrame_Input:
				return nil, t.Input(k.Input)
			case *pb.TerminalFrame_Resize:
				return nil, t.Resize(int(k.Resize.Cols), int(k.Resize.Rows), k.Resize.Fit)
			case *pb.TerminalFrame_HistoryRequest:
				if k.HistoryRequest == nil || len(k.HistoryRequest.RequestId) == 0 || len(k.HistoryRequest.RequestId) > 128 || len(k.HistoryRequest.Cursor) > 128 {
					return nil, deviceauth.ErrInvalid
				}
				select {
				case historyRequests <- k.HistoryRequest:
					return nil, nil
				default:
					return nil, deviceauth.ErrCapacity
				}
			default:
				return nil, deviceauth.ErrInvalid
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
