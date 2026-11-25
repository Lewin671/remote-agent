// Package stun provides bounded, UDP-only address discovery. It neither opens
// the relay database nor logs requests, source addresses or transaction IDs.
package stun

import (
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"time"
)

const (
	maxPacket      = 576
	cookie         = uint32(0x2112a442)
	sourceLifetime = time.Minute
)

type Config struct {
	PacketsPerSecond, Burst             int
	SourcePacketsPerSecond, SourceBurst int
	BytesPerSecond, MaxSources          int
}

func DefaultConfig() Config { return Config{1000, 2000, 20, 40, 128 << 10, 4096} }
func (c Config) valid() bool {
	return c.PacketsPerSecond > 0 && c.Burst > 0 && c.SourcePacketsPerSecond > 0 && c.SourceBurst > 0 && c.BytesPerSecond > 0 && c.MaxSources > 0 && c.MaxSources <= 65536
}

type bucket struct {
	at             time.Time
	packets, bytes float64
}
type source struct {
	bucket
	seen time.Time
}
type Server struct {
	mu      sync.Mutex
	config  Config
	global  bucket
	sources map[netip.Addr]source
	sweep   time.Time
}

// New takes explicit limits. The caller validates flags without exposing peer
// addresses in errors; invalid configuration cannot create a serving loop.
func New(config Config) *Server {
	def := DefaultConfig()
	if config.PacketsPerSecond > 0 && config.PacketsPerSecond != def.PacketsPerSecond && config.Burst == def.Burst {
		config.Burst = max(1, config.PacketsPerSecond*2)
	}
	if config.SourcePacketsPerSecond > 0 && config.SourcePacketsPerSecond != def.SourcePacketsPerSecond && config.SourceBurst == def.SourceBurst {
		config.SourceBurst = max(1, config.SourcePacketsPerSecond*2)
	}
	if !config.valid() {
		return nil
	}
	return &Server{config: config, sources: make(map[netip.Addr]source)}
}
func refill(b bucket, now time.Time, rate, burst, byteRate int) bucket {
	if b.at.IsZero() {
		return bucket{now, float64(burst), float64(byteRate)}
	}
	dt := max(0, now.Sub(b.at).Seconds())
	return bucket{now, min(float64(burst), b.packets+dt*float64(rate)), min(float64(byteRate), b.bytes+dt*float64(byteRate))}
}
func (s *Server) allow(ip netip.Addr, n int, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.config
	s.global = refill(s.global, now, c.PacketsPerSecond, c.Burst, c.BytesPerSecond)
	if s.global.packets < 1 || s.global.bytes < float64(n) {
		return false
	}
	if now.Sub(s.sweep) >= time.Second {
		for addr, v := range s.sources {
			if now.Sub(v.seen) >= sourceLifetime {
				delete(s.sources, addr)
			}
		}
		s.sweep = now
	}
	v, exists := s.sources[ip]
	if !exists && len(s.sources) >= c.MaxSources {
		return false
	}
	v.bucket = refill(v.bucket, now, c.SourcePacketsPerSecond, c.SourceBurst, c.BytesPerSecond)
	// Failed requests do not extend the address retention period.
	if v.packets < 1 {
		return false
	}
	v.packets--
	v.seen = now
	s.sources[ip] = v
	s.global.packets--
	s.global.bytes -= float64(n)
	return true
}

// Reply supports only RFC 8489 Binding requests and sends at most one minimal
// response to the packet's source. No alternate target, TURN allocation,
// software description or unauthenticated error response is available.
func (s *Server) Reply(request []byte, from netip.AddrPort, now time.Time) []byte {
	if len(request) < 20 || len(request) > maxPacket || len(request)%4 != 0 || binary.BigEndian.Uint16(request) != 1 || int(binary.BigEndian.Uint16(request[2:])) != len(request)-20 || binary.BigEndian.Uint32(request[4:]) != cookie {
		return nil
	}
	addr := from.Addr().Unmap()
	if !addr.IsValid() || addr.IsUnspecified() || addr.IsMulticast() || from.Port() == 0 {
		return nil
	}
	fingerprint := false
	for at := 20; at < len(request); {
		if at+4 > len(request) {
			return nil
		}
		kind := binary.BigEndian.Uint16(request[at:])
		n := int(binary.BigEndian.Uint16(request[at+2:]))
		end := at + 4 + n
		if end > len(request) {
			return nil
		}
		if kind == 0x8028 {
			if fingerprint || n != 4 || end != len(request) || binary.BigEndian.Uint32(request[at+4:]) != crc32.ChecksumIEEE(request[:at])^0x5354554e {
				return nil
			}
			fingerprint = true
		} else if kind < 0x8000 {
			// Comprehension-required extensions have no semantics in this discovery
			// service. In particular CHANGE-REQUEST cannot turn it into a reflector.
			return nil
		}
		at = end + (4-n%4)%4
		if at > len(request) {
			return nil
		}
	}
	n := 32
	if addr.Is6() {
		n = 44
	}
	if fingerprint {
		n += 8
	}
	if !s.allow(addr, n, now) {
		return nil
	}
	response := make([]byte, n)
	binary.BigEndian.PutUint16(response, 0x0101)
	binary.BigEndian.PutUint16(response[2:], uint16(n-20))
	copy(response[4:20], request[4:20])
	binary.BigEndian.PutUint16(response[20:], 0x0020)
	size := 8
	family := byte(1)
	ip := addr.AsSlice()
	if addr.Is6() {
		size = 20
		family = 2
	}
	binary.BigEndian.PutUint16(response[22:], uint16(size))
	response[25] = family
	binary.BigEndian.PutUint16(response[26:], from.Port()^uint16(cookie>>16))
	for i, b := range ip {
		response[28+i] = b ^ request[4+i]
	}
	if fingerprint {
		at := n - 8
		binary.BigEndian.PutUint16(response[at:], 0x8028)
		binary.BigEndian.PutUint16(response[at+2:], 4)
		binary.BigEndian.PutUint32(response[at+4:], crc32.ChecksumIEEE(response[:at])^0x5354554e)
	}
	return response
}

type udpConn interface {
	SetReadDeadline(time.Time) error
	SetWriteDeadline(time.Time) error
	ReadFromUDPAddrPort([]byte) (int, netip.AddrPort, error)
	WriteToUDPAddrPort([]byte, netip.AddrPort) (int, error)
	Close() error
}

// Serve owns conn until context cancellation. One fixed read/write loop bounds
// work and memory even under spoofed-source floods; it starts no packet workers.
func (s *Server) Serve(ctx context.Context, conn *net.UDPConn) error {
	return s.serve(ctx, conn)
}

func isPermanentUDPReadError(err error) bool {
	return errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.EBADF) || errors.Is(err, syscall.ENOTSOCK) || errors.Is(err, syscall.EINVAL)
}

func (s *Server) serve(ctx context.Context, conn udpConn) error {
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	defer func() { s.mu.Lock(); clear(s.sources); s.mu.Unlock() }()
	var buffer [maxPacket + 1]byte
	var lastWarn time.Time
	for {
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		n, from, err := conn.ReadFromUDPAddrPort(buffer[:])
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				s.mu.Lock()
				now := time.Now()
				for addr, v := range s.sources {
					if now.Sub(v.seen) >= sourceLifetime {
						delete(s.sources, addr)
					}
				}
				s.mu.Unlock()
				continue
			}
			if isPermanentUDPReadError(err) {
				return err
			}
			if now := time.Now(); now.Sub(lastWarn) >= time.Minute {
				lastWarn = now
				slog.Warn("stun: transient UDP read error; continuing")
			}
			timer := time.NewTimer(5 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
			continue
		}
		response := s.Reply(buffer[:n], from, time.Now())
		if response == nil {
			continue
		}
		// No sender-controlled fields are ever passed to a logger. A failed reply
		// is dropped; a UDP write failure must not create an error reply or retry.
		_ = conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
		_, _ = conn.WriteToUDPAddrPort(response, from)
	}
}
