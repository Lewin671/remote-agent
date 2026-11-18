package screens

import (
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

func TestBuildHomeStructuredAttentionAndHighlight(t *testing.T) {
	st := NewState()
	st.Cal = lists.Calendar{NowMs: 1_700_000_000_000}
	st.World.Connection = lists.ConnConnected
	st.World.LiveLoaded = true
	st.World.Machines = []*lists.Machine{
		{
			ID:     "m1",
			Online: true,
			Meta: &v1.MachineMeta{
				Hostname: "dev-box",
				Capabilities: &v1.MachineCapabilities{
					ApprovalAlways: true,
				},
			},
		},
	}
	st.World.MachinesListed = true
	st.World.SessionsListed = true
	st.World.Sessions = []*lists.Session{
		{
			ID:        "s-cmd",
			MachineID: "m1",
			UpdatedMs: 1_700_000_000_000,
			Info: &v1.Session{
				Id:     "s-cmd",
				Cwd:    "/work/app",
				Title:  "Fix tests",
				Status: v1.SessionStatus_SESSION_STATUS_RUNNING,
			},
			Pending: lists.PendingAction{
				HasApproval:     true,
				ApprovalID:      "ap-1",
				ApprovalCommand: "go test ./...",
			},
		},
		{
			ID:        "s-tool",
			MachineID: "m1",
			UpdatedMs: 1_699_999_990_000,
			Info: &v1.Session{
				Id:     "s-tool",
				Cwd:    "/work/app",
				Title:  "Edit config",
				Status: v1.SessionStatus_SESSION_STATUS_RUNNING,
			},
			Pending: lists.PendingAction{
				HasApproval:      true,
				ApprovalID:       "ap-2",
				ApprovalToolName: "Write",
			},
		},
	}

	vm := BuildHome(st, 0)
	if vm.Headline == nil || vm.Headline.HighlightArg != "count" || vm.Headline.HighlightCount != 2 {
		t.Fatalf("expected headline highlightArg=count highlightCount=2, got %+v", vm.Headline)
	}
	if vm.TopAttentionCard == nil {
		t.Fatalf("expected TopAttentionCard")
	}
	if vm.TopAttentionCard.AlwaysKind != corev1.AlwaysAllowKind_ALWAYS_ALLOW_KIND_COMMAND_PREFIX {
		t.Fatalf("expected COMMAND_PREFIX AlwaysKind, got %v", vm.TopAttentionCard.AlwaysKind)
	}
	if vm.TopAttentionCard.AlwaysLabel == nil || vm.TopAttentionCard.AlwaysLabel.Key != "approval.always_allow" {
		t.Fatalf("expected approval.always_allow label, got %+v", vm.TopAttentionCard.AlwaysLabel)
	}
	if len(vm.Attention) != 2 || vm.Attention[0].Detail == nil || vm.Attention[0].Detail.GetLiteral() != "go test ./..." {
		t.Fatalf("expected structured Detail literal=\"go test ./...\", got %+v", vm.Attention)
	}

	// Tool approval without command populates TOOL_CATEGORY + AlwaysArg.
	st.World.Sessions = st.World.Sessions[1:]
	vmTool := BuildHome(st, 0)
	if vmTool.TopAttentionCard == nil ||
		vmTool.TopAttentionCard.AlwaysKind != corev1.AlwaysAllowKind_ALWAYS_ALLOW_KIND_TOOL_CATEGORY ||
		vmTool.TopAttentionCard.AlwaysArg != "Write" {
		t.Fatalf("expected TOOL_CATEGORY AlwaysKind with AlwaysArg=Write, got %+v", vmTool.TopAttentionCard)
	}
}

func TestBuildHomeDisplayGraceHoldsConnectedHeadlineAndHidesUnlinked(t *testing.T) {
	unlinkedFalse := false
	unlinkedTrue := true
	cases := []struct {
		name           string
		conn           lists.ConnectionKind
		worldUnlinked  bool
		displayGrace   bool
		wantConnState  corev1.ConnState
		wantHeadline   corev1.HeadlineKind
		wantRowStatus  corev1.SessionDisplayStatus
	}{
		{
			name:          "offline_during_display_grace_keeps_connected_headline_and_row_status",
			conn:          lists.ConnOffline,
			worldUnlinked: true,
			displayGrace:  true,
			wantConnState: corev1.ConnState_CONN_STATE_OFFLINE,
			wantHeadline:  corev1.HeadlineKind_HEADLINE_KIND_RUNNING,
			wantRowStatus: corev1.SessionDisplayStatus_SESSION_DISPLAY_STATUS_RUNNING,
		},
		{
			name:          "reconnecting_during_display_grace_keeps_connected_headline",
			conn:          lists.ConnReconnecting,
			worldUnlinked: false,
			displayGrace:  true,
			wantConnState: corev1.ConnState_CONN_STATE_RECONNECTING,
			wantHeadline:  corev1.HeadlineKind_HEADLINE_KIND_RUNNING,
			wantRowStatus: corev1.SessionDisplayStatus_SESSION_DISPLAY_STATUS_RUNNING,
		},
		{
			name:          "offline_after_grace_expires_shows_error_headline_and_unlinked_row",
			conn:          lists.ConnOffline,
			worldUnlinked: true,
			displayGrace:  false,
			wantConnState: corev1.ConnState_CONN_STATE_OFFLINE,
			wantHeadline:  corev1.HeadlineKind_HEADLINE_KIND_ERROR,
			wantRowStatus: corev1.SessionDisplayStatus_SESSION_DISPLAY_STATUS_UNLINKED,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := NewState()
			st.Cal = lists.Calendar{NowMs: 1_700_000_000_000}
			st.DisplayGrace = tc.displayGrace
			unlinkedPtr := &unlinkedFalse
			if tc.worldUnlinked {
				unlinkedPtr = &unlinkedTrue
			}
			mUnlinked := tc.worldUnlinked
			mReachable := !tc.worldUnlinked && tc.conn == lists.ConnConnected
			st.World = lists.World{
				Connection:     tc.conn,
				Unlinked:       unlinkedPtr,
				LiveLoaded:     true,
				MachinesListed: true,
				SessionsListed: true,
				Machines: []*lists.Machine{
					{
						ID:        "m1",
						Online:    true,
						Reachable: &mReachable,
						Unlinked:  &mUnlinked,
						Meta:      &v1.MachineMeta{Hostname: "dev-box"},
					},
				},
				Sessions: []*lists.Session{
					{
						ID:        "s1",
						MachineID: "m1",
						UpdatedMs: 1_700_000_000_000,
						Info: &v1.Session{
							Id:     "s1",
							Cwd:    "/work/app",
							Title:  "Active run",
							Status: v1.SessionStatus_SESSION_STATUS_RUNNING,
						},
					},
				},
			}

			vm := BuildHome(st, 0)
			if vm.Connection == nil || vm.Connection.State != tc.wantConnState {
				t.Fatalf("expected Connection.State=%v, got %+v", tc.wantConnState, vm.Connection)
			}
			if vm.Headline == nil || vm.Headline.Kind != tc.wantHeadline {
				t.Fatalf("expected Headline.Kind=%v, got %+v", tc.wantHeadline, vm.Headline)
			}
			if len(vm.Running) != 1 || vm.Running[0].Status != tc.wantRowStatus {
				t.Fatalf("expected Running[0].Status=%v, got %+v", tc.wantRowStatus, vm.Running)
			}
		})
	}
}

func TestBuildSessionDisplayGraceHoldsEnabledComposerAndStatus(t *testing.T) {
	unlinkedTrue := true
	cases := []struct {
		name            string
		displayGrace    bool
		wantStatus      corev1.SessionDisplayStatus
		wantEnabled     bool
		wantDisabledKey string
	}{
		{
			name:            "reconnecting_during_display_grace_keeps_enabled_composer_and_status",
			displayGrace:    true,
			wantStatus:      corev1.SessionDisplayStatus_SESSION_DISPLAY_STATUS_RUNNING,
			wantEnabled:     true,
			wantDisabledKey: "",
		},
		{
			name:            "reconnecting_after_grace_expires_shows_unlinked_and_connect_hint",
			displayGrace:    false,
			wantStatus:      corev1.SessionDisplayStatus_SESSION_DISPLAY_STATUS_UNLINKED,
			wantEnabled:     false,
			wantDisabledKey: "composer.ios_connect_send",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := NewState()
			st.DisplayGrace = tc.displayGrace
			st.World = lists.World{Connection: lists.ConnReconnecting, Unlinked: &unlinkedTrue}
			reachableFalse := false
			in := SessionViewInput{
				Found:          true,
				SessionsListed: true,
				LiveLoaded:     true,
				Conn:           lists.ConnReconnecting,
				Unlinked:       true,
				Session: &lists.Session{
					ID:        "s1",
					MachineID: "m1",
					Info: &v1.Session{
						Id:     "s1",
						Title:  "Run",
						Status: v1.SessionStatus_SESSION_STATUS_RUNNING,
					},
				},
				Machine: &lists.Machine{
					ID:        "m1",
					Online:    true,
					Reachable: &reachableFalse,
				},
			}
			vm := BuildSessionView(in, st, nil, nil)
			if vm.Header == nil || vm.Header.Status != tc.wantStatus {
				t.Fatalf("expected Header.Status=%v, got %+v", tc.wantStatus, vm.Header)
			}
			if vm.Composer == nil || vm.Composer.Enabled != tc.wantEnabled {
				t.Fatalf("expected Composer.Enabled=%v, got %+v", tc.wantEnabled, vm.Composer)
			}
			gotKey := ""
			if vm.Composer.DisabledReason != nil {
				gotKey = vm.Composer.DisabledReason.Key
			}
			if gotKey != tc.wantDisabledKey {
				t.Fatalf("expected DisabledReason.Key=%q, got %q", tc.wantDisabledKey, gotKey)
			}
		})
	}
}

func TestBuildHomeAndSessionColdStartReachabilityUnknown(t *testing.T) {
	unlinkedTrue := true
	unlinkedFalse := false
	reachableFalse := false
	cases := []struct {
		name                string
		conn                lists.ConnectionKind
		worldUnlinked       bool
		reachabilityUnknown bool
		wantRowStatus       corev1.SessionDisplayStatus
		wantReachability    corev1.ComputerReachability
		wantUnlinked        bool
		wantEnabled         bool
		wantDisabledKey     string
	}{
		{
			name:                "cold_start_connecting_before_stream_ready_hides_offline_and_unlinked",
			conn:                lists.ConnConnecting,
			worldUnlinked:       true,
			reachabilityUnknown: true,
			wantRowStatus:       corev1.SessionDisplayStatus_SESSION_DISPLAY_STATUS_RUNNING,
			wantReachability:    corev1.ComputerReachability_COMPUTER_REACHABILITY_UNKNOWN,
			wantUnlinked:        false,
			wantEnabled:         false,
			wantDisabledKey:     "connection.connecting",
		},
		{
			name:                "cold_start_between_stream_ready_and_lists_fetched_stays_connecting",
			conn:                lists.ConnConnected,
			worldUnlinked:       true,
			reachabilityUnknown: true,
			wantRowStatus:       corev1.SessionDisplayStatus_SESSION_DISPLAY_STATUS_RUNNING,
			wantReachability:    corev1.ComputerReachability_COMPUTER_REACHABILITY_UNKNOWN,
			wantUnlinked:        false,
			wantEnabled:         false,
			wantDisabledKey:     "connection.connecting",
		},
		{
			name:                "cold_start_grace_expired_while_unlinked_exposes_unlinked",
			conn:                lists.ConnConnecting,
			worldUnlinked:       true,
			reachabilityUnknown: false,
			wantRowStatus:       corev1.SessionDisplayStatus_SESSION_DISPLAY_STATUS_UNLINKED,
			wantReachability:    corev1.ComputerReachability_COMPUTER_REACHABILITY_OFFLINE,
			wantUnlinked:        true,
			wantEnabled:         false,
			wantDisabledKey:     "composer.ios_connect_send",
		},
		{
			name:                "lists_fetched_machine_offline_shows_offline",
			conn:                lists.ConnConnected,
			worldUnlinked:       false,
			reachabilityUnknown: false,
			wantRowStatus:       corev1.SessionDisplayStatus_SESSION_DISPLAY_STATUS_OFFLINE,
			wantReachability:    corev1.ComputerReachability_COMPUTER_REACHABILITY_OFFLINE,
			wantUnlinked:        false,
			wantEnabled:         false,
			wantDisabledKey:     "errors.the_computer_is_offline_the_command_was_not_sent",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := NewState()
			st.Cal = lists.Calendar{NowMs: 1_700_000_000_000}
			st.ReachabilityUnknown = tc.reachabilityUnknown
			unlinkedPtr := &unlinkedFalse
			if tc.worldUnlinked {
				unlinkedPtr = &unlinkedTrue
			}
			mUnlinked := tc.worldUnlinked
			m := &lists.Machine{
				ID:                  "m1",
				Online:              false,
				LastSeenMs:          1_699_999_940_000,
				Reachable:           &reachableFalse,
				Unlinked:            &mUnlinked,
				ReachabilityUnknown: tc.reachabilityUnknown,
			}
			s := &lists.Session{
				ID:        "s1",
				MachineID: "m1",
				UpdatedMs: 1_699_999_990_000,
				Info: &v1.Session{
					Id:     "s1",
					Cwd:    "/Users/me/proj",
					Title:  "Run",
					Status: v1.SessionStatus_SESSION_STATUS_RUNNING,
				},
			}
			st.World = lists.World{
				Connection:     tc.conn,
				Unlinked:       unlinkedPtr,
				MachinesListed: !tc.worldUnlinked,
				SessionsListed: true,
				LiveLoaded:     true,
				Machines:       []*lists.Machine{m},
				Sessions:       []*lists.Session{s},
			}

			homeVM := BuildHome(st, 0)
			if len(homeVM.Running) != 1 || homeVM.Running[0].Status != tc.wantRowStatus {
				t.Fatalf("expected Home Running[0].Status=%v, got %+v", tc.wantRowStatus, homeVM.Running)
			}

			in := SessionViewInput{
				Found:               true,
				SessionsListed:      true,
				LiveLoaded:          true,
				Conn:                tc.conn,
				Unlinked:            tc.worldUnlinked,
				ReachabilityUnknown: tc.reachabilityUnknown,
				Session:             s,
				Machine:             m,
			}
			sessVM := BuildSessionView(in, st, nil, nil)
			if sessVM.Header == nil || sessVM.Header.Status != tc.wantRowStatus {
				t.Fatalf("expected Header.Status=%v, got %+v", tc.wantRowStatus, sessVM.Header)
			}
			if sessVM.Header.ComputerReachability != tc.wantReachability {
				t.Fatalf("expected ComputerReachability=%v, got %v", tc.wantReachability, sessVM.Header.ComputerReachability)
			}
			if sessVM.Header.ComputerOnline {
				t.Fatalf("expected ComputerOnline=false, got true")
			}
			if sessVM.Header.ComputerUnlinked != tc.wantUnlinked {
				t.Fatalf("expected ComputerUnlinked=%v, got %v", tc.wantUnlinked, sessVM.Header.ComputerUnlinked)
			}
			if tc.reachabilityUnknown && sessVM.Header.OfflineText != nil {
				t.Fatalf("expected nil OfflineText during ReachabilityUnknown, got %+v", sessVM.Header.OfflineText)
			}
			if sessVM.Composer == nil || sessVM.Composer.Enabled != tc.wantEnabled {
				t.Fatalf("expected Composer.Enabled=%v, got %+v", tc.wantEnabled, sessVM.Composer)
			}
			gotKey := ""
			if sessVM.Composer.DisabledReason != nil {
				gotKey = sessVM.Composer.DisabledReason.Key
			}
			if gotKey != tc.wantDisabledKey {
				t.Fatalf("expected DisabledReason.Key=%q, got %q", tc.wantDisabledKey, gotKey)
			}
		})
	}
}
