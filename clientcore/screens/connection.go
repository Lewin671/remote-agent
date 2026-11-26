package screens

import (
	"github.com/Lewin671/remote-agent/clientcore/domain/world"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

// BuildConnection constructs the ConnectionViewModel from world state and the
// engine's current sync snapshot, sharing buildSyncConnBanner as the single
// source of truth for connection banner presentation.
func BuildConnection(st *State, sv *world.SyncState) *corev1.ConnectionViewModel {
	if st == nil {
		st = NewState(nil)
	}
	if sv == nil {
		sv = &world.SyncState{}
	}

	protoConn := sv.Connection
	if protoConn == corev1.ConnState_CONN_STATE_UNSPECIFIED {
		protoConn = mapSessionConnState(st.World.Connection)
	}

	details := &corev1.ConnectionServerDetailsView{
		ShowDisconnectedAt: sv.SecurityLoaded && sv.DisconnectedAtMs > 0,
		DisconnectedAtMs:   sv.DisconnectedAtMs,
		ReasonText:         sv.ConnectionReason,
	}

	return &corev1.ConnectionViewModel{
		State:               protoConn,
		Connected:           protoConn == corev1.ConnState_CONN_STATE_CONNECTED && !sv.Removed,
		Removed:             sv.Removed || st.Removed,
		DisplayGrace:        sv.DisplayGrace || st.DisplayGrace,
		ReachabilityUnknown: sv.ReachabilityUnknown || st.ReachabilityUnknown,
		LiveLoaded:          sv.LiveLoaded,
		LeaseRenewalAdvice:  sv.LeaseRenewalAdvice,
		Banner:              buildSyncConnBanner(sv, nil),
		ServerDetails:       details,
		ClockSkewNotice:     clockSkewNoticeText(st.World.AccountClockSkewed),
	}
}

// clockSkewNoticeText is the one place that words "this device's clock is
// behind the account state" for the home banner and the connection strip.
func clockSkewNoticeText(skewed bool) *corev1.Text {
	if !skewed {
		return nil
	}
	return text.Msg(text.KeyErrorsThisDeviceSClockIsBehindTheAccountState)
}
