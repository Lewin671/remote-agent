package server

import (
	"net/http"
	"time"

	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
)

const (
	// Bound stalled unary writes without charging time spent waiting for a daemon.
	rpcResponseWriteTimeout = 30 * time.Second
	// Ciphertext history, session listings and images need more time on slow links.
	largeRPCResponseWriteTimeout = 60 * time.Second
)

func rpcWriteTimeout(procedure string, normal, large time.Duration) time.Duration {
	switch procedure {
	case remoteagentv1connect.ClientServiceGetEventsProcedure,
		remoteagentv1connect.ClientServiceGetImageProcedure,
		remoteagentv1connect.ClientServiceListRemoteSessionsProcedure:
		return large
	default:
		return normal
	}
}

// Connect serializes unary responses after its interceptor returns. Start
// the deadline at the first response write, and never extend it on progress.
// net/http clears it after finishing the HTTP/1 response or HTTP/2 stream.
type rpcResponseWriter struct {
	http.ResponseWriter
	timeout time.Duration
	started bool
}

func (w *rpcResponseWriter) start() {
	if !w.started {
		w.started = true
		_ = http.NewResponseController(w.ResponseWriter).SetWriteDeadline(time.Now().Add(w.timeout))
	}
}

func (w *rpcResponseWriter) WriteHeader(status int) {
	w.start()
	w.ResponseWriter.WriteHeader(status)
}

func (w *rpcResponseWriter) Write(p []byte) (int, error) {
	w.start()
	return w.ResponseWriter.Write(p)
}

func (w *rpcResponseWriter) FlushError() error {
	w.start()
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *rpcResponseWriter) Flush() { _ = w.FlushError() }

func (w *rpcResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
