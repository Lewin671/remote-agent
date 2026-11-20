package relay

import (
	"time"

	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/shared/peerwire"
)

type peerRates struct {
	buckets [2]directedTokens
	touched time.Time
}
type peerReservation struct {
	peer    string
	until   uint64
	elapsed time.Time
}

// reserveAuthentication is resource accounting, not a replay acceptance or ACK.
// The session gate still validates ordering and state before any action. Keeping
// this separate bounds all connections/generations for a peer together, even
// when a signed but semantically invalid packet consumes a reservation.
func (m *directManager) reserveAuthentication(peer string, o peerwire.Opened, now time.Time) bool {
	if m.authReservations == nil {
		m.authReservations = map[string]peerReservation{}
		m.authCounts = map[string]int{}
	}
	wall := uint64(now.UnixMilli())
	for hash, entry := range m.authReservations {
		if wall > entry.until && now.After(entry.elapsed) {
			delete(m.authReservations, hash)
			m.authCounts[entry.peer]--
			if m.authCounts[entry.peer] == 0 {
				delete(m.authCounts, entry.peer)
			}
		}
	}
	hash := string(o.Hash)
	if _, exists := m.authReservations[hash]; exists {
		return true
	}
	if m.authCounts[peer] >= 2048 || len(m.authReservations) >= 16384 {
		return false
	}
	until := o.Context.Deadline + deviceauth.MaxClockSkew
	if wall >= until {
		return false
	}
	m.authReservations[hash] = peerReservation{peer, until, now.Add(time.Duration(until-wall) * time.Millisecond)}
	m.authCounts[peer]++
	return true
}
func takeTokens(old directedTokens, now time.Time, n int, fps, bps, burst float64) (directedTokens, bool) {
	if old.at.IsZero() {
		old = directedTokens{at: now, frames: burst, bytes: bps}
	}
	dt := max(0, now.Sub(old.at).Seconds())
	old.at = now
	old.frames = min(burst, old.frames+dt*fps)
	old.bytes = min(bps, old.bytes+dt*bps)
	if old.frames < 1 || old.bytes < float64(n) {
		return old, false
	}
	old.frames--
	old.bytes -= float64(n)
	return old, true
}
func (m *directManager) allowIncoming(peer string, class, n int, now time.Time) bool {
	if class < 0 || class > 1 || class == 0 && n > 64<<10 || class == 1 && n > 16<<10 {
		return false
	}
	if m.inboundPeers == nil {
		m.inboundPeers = map[string]*peerRates{}
	}
	for id, r := range m.inboundPeers {
		if now.Sub(r.touched) > time.Minute {
			delete(m.inboundPeers, id)
		}
	}
	rates := m.inboundPeers[peer]
	if rates == nil {
		if len(m.inboundPeers) >= deviceauth.MaxMembers {
			return false
		}
		rates = &peerRates{}
	}
	fps, bps, burst := 256.0, float64(4<<20), 256.0
	afps, abps, aburst := 1024.0, float64(16<<20), 1024.0
	if class == 1 {
		fps, bps, burst = 512, 512<<10, 1024
		afps, abps, aburst = 2048, 4<<20, 2048
	}
	per, ok := takeTokens(rates.buckets[class], now, n, fps, bps, burst)
	if !ok {
		return false
	}
	account, ok := takeTokens(m.inboundAccount[class], now, n, afps, abps, aburst)
	if !ok {
		return false
	}
	rates.buckets[class] = per
	rates.touched = now
	m.inboundPeers[peer] = rates
	m.inboundAccount[class] = account
	return true
}
