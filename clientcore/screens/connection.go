package screens

import (
	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

// BuildConnection constructs the ConnectionViewModel from world state and the
// engine's current sync snapshot.
func BuildConnection(st *State, sv *corev1.SyncStateView) *corev1.ConnectionViewModel {
	if st == nil {
		st = NewState(nil)
	}
	if sv == nil {
		sv = &corev1.SyncStateView{}
	}

	conn := st.World.Connection
	if sv.Connection != corev1.ConnState_CONN_STATE_UNSPECIFIED {
		switch sv.Connection {
		case corev1.ConnState_CONN_STATE_CONNECTED:
			conn = lists.ConnConnected
		case corev1.ConnState_CONN_STATE_RECONNECTING:
			conn = lists.ConnReconnecting
		case corev1.ConnState_CONN_STATE_CONNECTING:
			conn = lists.ConnConnecting
		case corev1.ConnState_CONN_STATE_OFFLINE:
			conn = lists.ConnOffline
		case corev1.ConnState_CONN_STATE_NO_NETWORK:
			conn = lists.ConnNoNetwork
		}
	}

	protoConn := mapSessionConnState(conn)
	initialListed := sv.MachinesListed || sv.SessionsListed || sv.PrefsLoaded
	kept := (sv.MachinesListed && sv.SessionsListed) || len(sv.Sessions) > 0 || sv.PrefsLoaded

	var kind corev1.ConnectionBannerKind
	switch {
	case !sv.NetworkAvailable && conn == lists.ConnNoNetwork:
		kind = corev1.ConnectionBannerKind_CONNECTION_BANNER_KIND_NO_NETWORK
	case !sv.NetworkAvailable:
		kind = corev1.ConnectionBannerKind_CONNECTION_BANNER_KIND_NO_NETWORK
	case sv.Removed || conn == lists.ConnOffline || (sv.Unlinked && !sv.ReachabilityUnknown):
		kind = corev1.ConnectionBannerKind_CONNECTION_BANNER_KIND_NO_SERVER
	case conn == lists.ConnConnected:
		kind = corev1.ConnectionBannerKind_CONNECTION_BANNER_KIND_NONE
	default:
		kind = corev1.ConnectionBannerKind_CONNECTION_BANNER_KIND_WAITING
	}

	var banner *corev1.ConnectionBannerView
	if kind != corev1.ConnectionBannerKind_CONNECTION_BANNER_KIND_NONE && !sv.DisplayGrace {
		banner = &corev1.ConnectionBannerView{
			Kind:     kind,
			Visible:  true,
			KeptData: kept,
		}
		switch kind {
		case corev1.ConnectionBannerKind_CONNECTION_BANNER_KIND_NO_NETWORK:
			if kept {
				banner.Text = text.Msg(text.KeyConnectionNoNetworkPrevious)
			} else {
				banner.Text = text.Msg(text.KeyConnectionNoNetworkEmpty)
			}
		case corev1.ConnectionBannerKind_CONNECTION_BANNER_KIND_NO_SERVER:
			banner.ShowDetails = true
			banner.ErrorTone = !kept
			banner.Text = text.Msg(text.KeyConnectionServerRetrying)
			reason := sv.ConnectionReason
			if !kept && !text.IsEmpty(reason) {
				banner.FailedText = text.Msg(text.KeyConnectionAndroidServerFailed, text.ArgText("reason", reason))
			}
			detailReason := reason
			if text.IsEmpty(detailReason) {
				detailReason = text.Msg(text.KeyConnectionNoResponse)
			}
			banner.DetailText = text.Msg(text.KeyConnectionServerDetails, text.ArgText("reason", detailReason))
		case corev1.ConnectionBannerKind_CONNECTION_BANNER_KIND_WAITING:
			if sv.LiveLoaded || sv.SecurityLoaded {
				banner.Text = text.Msg(text.KeyConnectionReconnecting)
			} else {
				banner.Text = text.Msg(text.KeyConnectionConnecting)
			}
		}
	}

	var disconnectedAtText *corev1.Text
	if sv.DisconnectedAtMs > 0 {
		disconnectedAtText = formatRelativeTime(sv.DisconnectedAtMs, st.Cal)
	}
	details := &corev1.ConnectionServerDetailsView{
		ShowDisconnectedAt: sv.SecurityLoaded,
		DisconnectedAtMs:   sv.DisconnectedAtMs,
		DisconnectedAtText: disconnectedAtText,
		ReasonText:         sv.ConnectionReason,
	}

	return &corev1.ConnectionViewModel{
		State:               protoConn,
		Connected:           conn == lists.ConnConnected && !sv.Removed,
		Removed:             sv.Removed || st.Removed,
		DisplayGrace:        sv.DisplayGrace || st.DisplayGrace,
		ReachabilityUnknown: sv.ReachabilityUnknown || st.ReachabilityUnknown,
		NetworkAvailable:    sv.NetworkAvailable,
		LiveLoaded:          sv.LiveLoaded,
		InitialListed:       initialListed,
		SecurityLoaded:      sv.SecurityLoaded,
		IsMember:            sv.IsMember,
		AccountClockSkewed:  sv.AccountClockSkewed || st.World.AccountClockSkewed,
		DisconnectedAtMs:    sv.DisconnectedAtMs,
		ReasonText:          sv.ConnectionReason,
		ClockSkewBannerText: sv.ClockSkewBannerText,
		LeaseRenewalAdvice:  sv.LeaseRenewalAdvice,
		Banner:              banner,
		ServerDetails:       details,
		AuthorityDeviceId:   sv.AuthorityDeviceId,
		IsManager:           sv.IsManager || st.World.IsManager,
	}
}
