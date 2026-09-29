package ptyhost

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"
)

// Conn is a control connection to a running Host. It is safe for
// concurrent use: each request waits for its reply before the next is sent.
type Conn struct {
	mu  sync.Mutex
	c   net.Conn
	r   *bufio.Reader
	enc *json.Encoder
}

// Dial connects to the control socket at path.
func Dial(path string) (*Conn, error) {
	c, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return nil, err
	}
	return &Conn{c: c, r: bufio.NewReaderSize(c, 64*1024), enc: json.NewEncoder(c)}, nil
}

// Close closes the connection; a hold made on it is released.
func (c *Conn) Close() error { return c.c.Close() }

func (c *Conn) do(req Request) (Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.c.SetDeadline(time.Now().Add(5 * time.Second))
	if err := c.enc.Encode(req); err != nil {
		return Response{}, err
	}
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		return Response{}, err
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return Response{}, err
	}
	if !resp.OK {
		return resp, errors.New("pty-host: " + resp.Error)
	}
	return resp, nil
}

// Hold buffers local input for at most d.
func (c *Conn) Hold(d time.Duration) (Response, error) {
	return c.do(Request{Op: "hold", HoldMS: int(d / time.Millisecond)})
}

// Release stops holding and flushes buffered local input.
func (c *Conn) Release() error {
	_, err := c.do(Request{Op: "release"})
	return err
}

// Write sends bytes to the CLI as if typed.
func (c *Conn) Write(b []byte) error {
	_, err := c.do(Request{Op: "write", Data: b})
	return err
}

// Status reports the host's state.
func (c *Conn) Status() (Response, error) {
	return c.do(Request{Op: "status"})
}
