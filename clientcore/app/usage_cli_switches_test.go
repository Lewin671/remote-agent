package app

import (
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

func seedDualCLIMachine(t *testing.T, c *Core) {
	t.Helper()
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_000,
		Event: &corev1.Event_EntropyProvided{
			EntropyProvided: &corev1.EntropyProvided{
				RandomIds:  []string{"id-1", "id-2", "id-3", "id-4", "id-5", "id-6", "id-7", "id-8", "id-9", "id-10", "id-11", "id-12"},
				UnitFloats: []float64{0.5, 0.5, 0.5, 0.5, 0.5, 0.5},
			},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_000,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_010,
		Event: &corev1.Event_SecuritySnapshot{
			SecuritySnapshot: &corev1.SecuritySnapshotEvent{
				OpId:              3,
				IsMember:          true,
				SelfDeviceId:      "dev-1",
				AuthorityDeviceId: "dev-1",
				LeaseEndMs:        1_891_633_600_000,
				TrustedMachineIds: []string{"m-1"},
			},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_020,
		Event: &corev1.Event_TokenResult{
			TokenResult: &corev1.TokenResultEvent{OpId: 2, Token: "tok-1"},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_030,
		Event: &corev1.Event_StreamReady{
			StreamReady: &corev1.StreamReadyEvent{LegId: 1, ServerTimeMs: 1_791_633_600_030},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_040,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				OpId: 4,
				Machines: []*corev1.PlainMachine{
					{
						MachineId: "m-1",
						Online:    true,
						Meta: &v1.MachineMeta{
							Hostname:        "MacStudio",
							ProtocolVersion: 17,
							Capabilities: &v1.MachineCapabilities{
								MachineStatus:  true,
								ScheduledTasks: true,
								CliSwitches:    true,
								UsageStats:     true,
							},
							Agents: []*v1.AgentInfo{
								{Agent: v1.Agent_AGENT_CODEX, Version: "0.90.0", Verified: true},
								{Agent: v1.Agent_AGENT_CLAUDE, Version: "2.1.0", Verified: true},
								{Agent: v1.Agent_AGENT_ANTIGRAVITY, Version: "1.4.0", Verified: true},
							},
							Quotas: []*v1.AgentQuota{
								{Agent: v1.Agent_AGENT_CODEX, State: v1.QuotaState_QUOTA_STATE_OK},
								{Agent: v1.Agent_AGENT_CLAUDE, State: v1.QuotaState_QUOTA_STATE_OK},
							},
						},
					},
				},
				Sessions: []*corev1.PlainSession{
					{
						SessionId: "s-1",
						MachineId: "m-1",
						UpdatedMs: 1_791_633_500_000,
						Meta:      &v1.Session{Id: "s-1", Cwd: "/Users/me/proj", Agent: v1.Agent_AGENT_CLAUDE, Model: "claude-sonnet-4-6"},
					},
				},
			},
		},
	})
}

func settleUpdateSettings(c *Core, dc *corev1.DispatchCommandEffect, nowMs int64, res *v1.CommandResult, errCode string) *corev1.EffectBatch {
	outerOutcome := "executed"
	outerReason := "ok"
	if res != nil && errCode == "" && res.Reason == "" {
		res.Reason = "ok"
	}
	if errCode != "" {
		outerOutcome = "rejected"
		outerReason = errCode
		if res != nil && res.Reason == "" {
			res.Reason = errCode
		}
	}
	return c.Dispatch(&corev1.Event{
		NowMs: nowMs,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:         dc.RequestId,
				AttemptId:         dc.AttemptId,
				Source:            corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				AttemptBound:      true,
				Signed:            true,
				HashesMatch:       true,
				OuterMatchesInner: true,
				OuterOutcome:      outerOutcome,
				OuterReason:       outerReason,
				Result:            res,
			},
		},
	})
}

func TestCliSwitchesToggleConflictAndDraftFallback(t *testing.T) {
	c := New()
	defer c.Close()
	seedDualCLIMachine(t, c)

	// Open NewSession with Claude selected and user typing a prompt.
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_050,
		Event: &corev1.Event_NewSessionOpen{NewSessionOpen: &corev1.NewSessionOpenAction{ProjectKey: "/Users/me/proj"}},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_055,
		Event: &corev1.Event_NewSessionSetPrompt{NewSessionSetPrompt: &corev1.NewSessionSetPromptAction{Prompt: "unsent draft text"}},
	})

	// Open MachineSettings and verify CliSwitches visible with 3 CLIs enabled.
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_060,
		Event: &corev1.Event_MachineSettingsOpen{MachineSettingsOpen: &corev1.MachineSettingsOpenEvent{MachineId: "m-1"}},
	})
	msVM, err := c.View(&corev1.Screen{Screen: &corev1.Screen_MachineSettings{MachineSettings: &corev1.MachineSettingsScreenRequest{MachineId: "m-1"}}})
	if err != nil {
		t.Fatalf("view machine settings: %v", err)
	}
	ms := msVM.GetMachineSettings()
	if !ms.GetShowCliSwitches() || !ms.GetCliSwitchesEditable() || len(ms.GetCliVersions()) != 3 {
		t.Fatalf("expected ShowCliSwitches & 3 versions, got show=%v edit=%v len=%d", ms.GetShowCliSwitches(), ms.GetCliSwitchesEditable(), len(ms.GetCliVersions()))
	}

	// Toggle Claude OFF -> optimistic saving -> settled.
	tBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_070,
		Event: &corev1.Event_ToggleMachineAgent{
			ToggleMachineAgent: &corev1.ToggleMachineAgentEvent{
				MachineId: "m-1",
				Agent:     v1.Agent_AGENT_CLAUDE,
				Enabled:   false,
			},
		},
	})
	var dc *corev1.DispatchCommandEffect
	var toggleReq *v1.UpdateSettingsRequest
	for _, eff := range tBatch.Effects {
		if eff.GetDispatchCommand() != nil {
			var cmd v1.Command
			if err := cmd.Unmarshal(eff.GetDispatchCommand().CommandBytes); err == nil && cmd.GetUpdateSettings() != nil {
				dc = eff.GetDispatchCommand()
				toggleReq = cmd.GetUpdateSettings()
			}
		}
	}
	if dc == nil || toggleReq == nil {
		t.Fatal("expected UpdateSettings DispatchCommand for ToggleMachineAgent")
	}
	if toggleReq.Settings != nil || toggleReq.ToggleAgent == nil || toggleReq.ToggleEnabled == nil || *toggleReq.ToggleAgent != v1.Agent_AGENT_CLAUDE || *toggleReq.ToggleEnabled {
		t.Fatalf("expected atomic ToggleMachineAgent request with nil Settings, got %+v", toggleReq)
	}
	settleUpdateSettings(c, dc, 1_791_633_600_080, &v1.CommandResult{
		Outcome:         "executed",
		ResultAvailable: true,
		Kind: &v1.CommandResult_UpdateSettings{
			UpdateSettings: &v1.UpdateSettingsResponse{
				Settings: &v1.DaemonSettings{
					Revision:       2,
					DisabledAgents: []v1.Agent{v1.Agent_AGENT_CLAUDE},
				},
			},
		},
	}, "")

	// Stale MachineUpdated/ListsFetched snapshot with lower SettingsRevision must not revert disabled Claude.
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_085,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				OpId: 4,
				Machines: []*corev1.PlainMachine{
					{
						MachineId: "m-1",
						Online:    true,
						MetaRev:   10,
						Meta: &v1.MachineMeta{
							Hostname:         "MacStudio",
							ProtocolVersion:  17,
							SettingsRevision: 1,
							Capabilities:     &v1.MachineCapabilities{CliSwitches: true, UsageStats: true},
							Agents: []*v1.AgentInfo{
								{Agent: v1.Agent_AGENT_CODEX, Version: "0.90.0", Verified: true},
								{Agent: v1.Agent_AGENT_CLAUDE, Version: "2.1.0", Verified: true},
								{Agent: v1.Agent_AGENT_ANTIGRAVITY, Version: "1.4.0", Verified: true},
							},
						},
					},
				},
			},
		},
	})

	// Mid-draft NewSession preserves prompt text and surfaces NOTICE_KIND_AGENT_DISABLED_FALLBACK.
	nsVM, err := c.View(&corev1.Screen{Screen: &corev1.Screen_NewSession{NewSession: &corev1.NewSessionScreenRequest{ProjectKey: "/Users/me/proj"}}})
	if err != nil {
		t.Fatalf("view new session: %v", err)
	}
	ns := nsVM.GetNewSession()
	if ns.GetPrompt() != "unsent draft text" {
		t.Fatalf("expected preserved draft prompt, got %q", ns.GetPrompt())
	}
	if ns.GetConfig().GetAgent() == v1.Agent_AGENT_CLAUDE {
		t.Fatal("expected disabled Claude excluded from NewSession active config")
	}
	var hasDisabledNotice bool
	for _, n := range ns.GetNotices() {
		if n.Kind == corev1.NoticeKind_NOTICE_KIND_AGENT_DISABLED_FALLBACK &&
			n.Action == corev1.NoticeActionKind_NOTICE_ACTION_KIND_ENABLE_AGENT &&
			n.TargetAgent == v1.Agent_AGENT_CLAUDE {
			hasDisabledNotice = true
		}
	}
	if !hasDisabledNotice {
		t.Fatalf("expected NOTICE_KIND_AGENT_DISABLED_FALLBACK with TargetAgent=AGENT_CLAUDE in notices, got %+v", ns.GetNotices())
	}

	// Quota screen excludes disabled Claude.
	qVM, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Quota{Quota: &corev1.QuotaScreenRequest{MachineId: "m-1"}}})
	for _, sec := range qVM.GetQuota().GetSections() {
		if sec.Agent == v1.Agent_AGENT_CLAUDE {
			t.Fatal("expected disabled Claude excluded from Quota sections")
		}
	}

	// Simulate concurrent toggle conflict on Antigravity -> shows conflict banner & requests GetSettings.
	cBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_090,
		Event: &corev1.Event_ToggleMachineAgent{
			ToggleMachineAgent: &corev1.ToggleMachineAgentEvent{
				MachineId: "m-1",
				Agent:     v1.Agent_AGENT_ANTIGRAVITY,
				Enabled:   false,
			},
		},
	})
	var dc2 *corev1.DispatchCommandEffect
	for _, eff := range cBatch.Effects {
		if eff.GetDispatchCommand() != nil {
			var cmd v1.Command
			if err := cmd.Unmarshal(eff.GetDispatchCommand().CommandBytes); err == nil && cmd.GetUpdateSettings() != nil {
				dc2 = eff.GetDispatchCommand()
			}
		}
	}
	if dc2 == nil {
		t.Fatal("expected second UpdateSettings dispatch")
	}
	rBatch := settleUpdateSettings(c, dc2, 1_791_633_600_100, &v1.CommandResult{
		Outcome:   "rejected",
		ErrorCode: "failed_precondition",
		Reason:    "cannot_disable_last_agent",
	}, "cannot_disable_last_agent")
	var gotRefresh bool
	for _, eff := range rBatch.Effects {
		if eff.GetDispatchCommand() != nil {
			var cmd v1.Command
			if err := cmd.Unmarshal(eff.GetDispatchCommand().CommandBytes); err == nil && cmd.GetGetSettings() != nil {
				gotRefresh = true
			}
		}
	}
	if !gotRefresh {
		t.Fatal("expected GetSettings resync dispatch on toggle conflict")
	}
	msVM2, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_MachineSettings{MachineSettings: &corev1.MachineSettingsScreenRequest{MachineId: "m-1"}}})
	if msVM2.GetMachineSettings().GetCliSwitchesConflictBanner() == nil {
		t.Fatal("expected conflict banner on MachineSettings")
	}

	// Simulate daemon reporting only Codex left enabled (Claude + Antigravity disabled):
	// Codex row must report LockedLastEnabled=true, and trying to toggle it OFF locally must be ignored.
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_110,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: 1,
				Frame: &corev1.StreamFrameEvent_MachineUpdated{
					MachineUpdated: &corev1.PlainMachine{
						MachineId: "m-1",
						Online:    true,
						Meta: &v1.MachineMeta{
							Hostname:        "MacStudio",
							ProtocolVersion: 17,
							SettingsRevision: 5,
							DisabledAgents:  []v1.Agent{v1.Agent_AGENT_CLAUDE, v1.Agent_AGENT_ANTIGRAVITY},
							Capabilities: &v1.MachineCapabilities{
								MachineStatus:  true,
								ScheduledTasks: true,
								CliSwitches:    true,
								UsageStats:     true,
							},
							Agents: []*v1.AgentInfo{
								{Agent: v1.Agent_AGENT_CODEX, Version: "0.90.0", Verified: true},
								{Agent: v1.Agent_AGENT_CLAUDE, Version: "2.1.0", Verified: true},
								{Agent: v1.Agent_AGENT_ANTIGRAVITY, Version: "1.4.0", Verified: true},
							},
						},
					},
				},
			},
		},
	})
	msVM3, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_MachineSettings{MachineSettings: &corev1.MachineSettingsScreenRequest{MachineId: "m-1"}}})
	var codexLocked bool
	for _, cv := range msVM3.GetMachineSettings().GetCliVersions() {
		if cv.Agent == v1.Agent_AGENT_CODEX && cv.LockedLastEnabled && cv.Enabled {
			codexLocked = true
		}
	}
	if !codexLocked {
		t.Fatalf("expected Codex LockedLastEnabled=true when other CLIs disabled, got %+v", msVM3.GetMachineSettings().GetCliVersions())
	}
	lastBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_120,
		Event: &corev1.Event_ToggleMachineAgent{
			ToggleMachineAgent: &corev1.ToggleMachineAgentEvent{
				MachineId: "m-1",
				Agent:     v1.Agent_AGENT_CODEX,
				Enabled:   false,
			},
		},
	})
	for _, eff := range lastBatch.Effects {
		if eff.GetDispatchCommand() != nil {
			t.Fatal("expected last-CLI guard to prevent UpdateSettings dispatch when only 1 enabled CLI remains")
		}
	}
}

func TestUsageScreenFetchCacheWriteAndSessionEntry(t *testing.T) {
	c := New()
	defer c.Close()
	seedDualCLIMachine(t, c)

	// Open Usage screen -> submits GetUsageStats.
	uBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_050,
		Event: &corev1.Event_UsageScreenOpen{
			UsageScreenOpen: &corev1.UsageScreenOpenEvent{
				MachineId:       "m-1",
				Preset:          v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS,
				Timezone:        "Asia/Shanghai",
				TzOffsetMinutes: 480,
				BucketBoundsMs:  []int64{1_791_000_000_000, 1_791_086_400_000},
				PrevStartMs:     1_790_400_000_000,
			},
		},
	})
	var dc *corev1.DispatchCommandEffect
	for _, eff := range uBatch.Effects {
		if eff.GetDispatchCommand() != nil {
			var cmd v1.Command
			if err := cmd.Unmarshal(eff.GetDispatchCommand().CommandBytes); err == nil && cmd.GetGetUsageStats() != nil {
				dc = eff.GetDispatchCommand()
			}
		}
	}
	if dc == nil {
		t.Fatal("expected GetUsageStats DispatchCommand")
	}

	sBatch := settleUpdateSettings(c, dc, 1_791_633_600_060, &v1.CommandResult{
		Outcome:         "executed",
		ResultAvailable: true,
		Kind: &v1.CommandResult_UsageStats{
			UsageStats: &v1.UsageStatsResponse{
				Preset:        v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS,
				Granularity:   v1.UsageBucketGranularity_USAGE_BUCKET_GRANULARITY_DAY,
				GeneratedAtMs: 1_791_633_600_055,
				Sources: []*v1.UsageAgentSource{
					{Agent: v1.Agent_AGENT_CLAUDE, State: v1.UsageSourceState_USAGE_SOURCE_STATE_OK},
					{Agent: v1.Agent_AGENT_ANTIGRAVITY, State: v1.UsageSourceState_USAGE_SOURCE_STATE_UNAVAILABLE},
				},
				Series: []*v1.UsageSeries{
					{
						Agent:               v1.Agent_AGENT_CLAUDE,
						Model:               "claude-sonnet-4-6",
						CacheWriteSupported: true,
						Current: &v1.UsagePeriodSummary{
							UncachedInputTokens: 100_000,
							CacheReadTokens:     900_000,
							CacheWriteTokens:    200_000,
							OutputTokens:        80_000,
							UsageRequestCount:   10,
							SpeedRequestCount:   2,
							HasCacheHitRate:     true,
							CacheHitRate:        0.75,
							HasSpeed:            true,
							SpeedP50:            92,
							Recent10SpeedCount:  2,
							Recent10SpeedP50:    92,
						},
					},
				},
			},
		},
	}, "")

	var hasCacheWrite bool
	for _, eff := range sBatch.Effects {
		if eff.GetUsageCacheWrite() != nil && eff.GetUsageCacheWrite().MachineId == "m-1" {
			hasCacheWrite = true
		}
	}
	if !hasCacheWrite {
		t.Fatal("expected UsageCacheWriteEffect on 7d GetUsageStats settle")
	}

	uVM, err := c.View(&corev1.Screen{
		Screen: &corev1.Screen_Usage{
			Usage: &corev1.UsageScreenRequest{
				MachineId:  "m-1",
				Preset:     v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS,
				FocusAgent: v1.Agent_AGENT_CLAUDE,
				FocusModel: "claude-sonnet-4-6",
			},
		},
	})
	if err != nil {
		t.Fatalf("View(Usage): %v", err)
	}
	u := uVM.GetUsage()
	if !u.GetHasAnyUsage() || len(u.GetSeries()) != 1 || !u.GetSeries()[0].GetFewSpeedSamples() || u.GetUnsupportedFootnote() == nil {
		t.Fatalf("unexpected UsageViewModel: hasUsage=%v len=%d footnote=%v", u.GetHasAnyUsage(), len(u.GetSeries()), u.GetUnsupportedFootnote())
	}

	// Verify Session Settings UsageEntry picks up cached summary.
	sessVM, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-1"}}})
	if err != nil {
		t.Fatalf("View(Session): %v", err)
	}
	ue := sessVM.GetSession().GetActions().GetUsageEntry()
	if ue == nil || !ue.GetVisible() || !ue.GetSupported() || !ue.GetHasData() || ue.GetTotalTokens() != 1_280_000 || !ue.GetFewSpeedSamples() {
		t.Fatalf("unexpected Session UsageEntry: %+v", ue)
	}
}

func TestUsageOfflineCacheAndMachineStatus30dFallback(t *testing.T) {
	c := New()
	defer c.Close()
	seedDualCLIMachine(t, c)

	// Mark machine offline and inject 7d empty + 30d non-empty cache via UsageCacheLoadedEvent.
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_050,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: 1,
				Frame: &corev1.StreamFrameEvent_MachineUpdated{
					MachineUpdated: &corev1.PlainMachine{
						MachineId: "m-1",
						Online:    false,
						Meta: &v1.MachineMeta{
							Hostname:        "MacStudio",
							ProtocolVersion: 17,
							Capabilities: &v1.MachineCapabilities{
								MachineStatus: true,
								CliSwitches:   true,
								UsageStats:    true,
							},
							Agents: []*v1.AgentInfo{
								{Agent: v1.Agent_AGENT_CODEX, Version: "0.90.0", Verified: true},
								{Agent: v1.Agent_AGENT_CLAUDE, Version: "2.1.0", Verified: true},
							},
						},
					},
				},
			},
		},
	})

	c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_060,
		Event: &corev1.Event_UsageCacheLoaded{
			UsageCacheLoaded: &corev1.UsageCacheLoadedEvent{
				MachineId: "m-1",
				Snapshot: &v1.UsageStatsResponse{
					Preset:        v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS,
					GeneratedAtMs: 1_791_633_000_000,
				},
			},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_070,
		Event: &corev1.Event_UsageCacheLoaded{
			UsageCacheLoaded: &corev1.UsageCacheLoadedEvent{
				MachineId: "m-1",
				Snapshot: &v1.UsageStatsResponse{
					Preset:        v1.UsageRangePreset_USAGE_RANGE_PRESET_THIRTY_DAYS,
					GeneratedAtMs: 1_791_633_100_000,
					Series: []*v1.UsageSeries{
						{
							Agent: v1.Agent_AGENT_CODEX,
							Model: "gpt-5.4",
							Current: &v1.UsagePeriodSummary{
								UncachedInputTokens: 200_000,
								OutputTokens:        50_000,
								UsageRequestCount:   5,
								SpeedRequestCount:   4,
								HasCacheHitRate:     true,
								CacheHitRate:        0.5,
								HasSpeed:            true,
								SpeedP50:            70,
							},
						},
					},
				},
			},
		},
	})

	msVM, err := c.View(&corev1.Screen{Screen: &corev1.Screen_MachineStatus{MachineStatus: &corev1.MachineStatusScreenRequest{MachineId: "m-1"}}})
	if err != nil {
		t.Fatalf("View(MachineStatus): %v", err)
	}
	mu := msVM.GetMachineStatus().GetModelUsage()
	if mu == nil || !mu.GetSupported() || !mu.GetFallbackThirtyDays() || !mu.GetCachedOffline() || !mu.GetHasData() || len(mu.GetRows()) != 1 {
		t.Fatalf("unexpected MachineStatus ModelUsage preview: %+v", mu)
	}

	uVM, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Usage{Usage: &corev1.UsageScreenRequest{MachineId: "m-1", Preset: v1.UsageRangePreset_USAGE_RANGE_PRESET_THIRTY_DAYS}}})
	u := uVM.GetUsage()
	if !u.GetCachedOffline() || u.GetCachedAtMs() != 1_791_633_100_000 || len(u.GetSeries()) != 1 {
		t.Fatalf("unexpected offline UsageViewModel: offline=%v cachedAt=%d series=%d", u.GetCachedOffline(), u.GetCachedAtMs(), len(u.GetSeries()))
	}
}

func TestAntigravityUsageWithoutSpeedInUsageMachineAndSession(t *testing.T) {
	c := New()
	defer c.Close()
	seedDualCLIMachine(t, c)

	// Switch session s-1 to Antigravity gemini-4-hydrogen (matching normalized transcript model).
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_045,
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: 1,
				Frame: &corev1.StreamFrameEvent_SessionUpdated{
					SessionUpdated: &corev1.PlainSession{
						SessionId: "s-1",
						MachineId: "m-1",
						UpdatedMs: 1_791_633_500_100,
						Meta:      &v1.Session{Id: "s-1", Cwd: "/Users/me/proj", Agent: v1.Agent_AGENT_ANTIGRAVITY, Model: "gemini-4-hydrogen"},
					},
				},
			},
		},
	})

	// Before any snapshot arrives, Usage view must not hardcode Antigravity as unavailable.
	preVM, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Usage{Usage: &corev1.UsageScreenRequest{MachineId: "m-1"}}})
	if err != nil {
		t.Fatalf("View(Usage before snapshot): %v", err)
	}
	if preVM.GetUsage().GetUnsupportedFootnote() != nil {
		t.Fatalf("expected no hardcoded unsupported footnote before snapshot, got %+v", preVM.GetUsage().GetUnsupportedFootnote())
	}

	uBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_050,
		Event: &corev1.Event_UsageScreenOpen{
			UsageScreenOpen: &corev1.UsageScreenOpenEvent{
				MachineId:       "m-1",
				Preset:          v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS,
				Timezone:        "Asia/Shanghai",
				TzOffsetMinutes: 480,
				BucketBoundsMs:  []int64{1_791_000_000_000, 1_791_086_400_000},
				PrevStartMs:     1_790_400_000_000,
			},
		},
	})
	var dc *corev1.DispatchCommandEffect
	for _, eff := range uBatch.Effects {
		if eff.GetDispatchCommand() != nil {
			var cmd v1.Command
			if err := cmd.Unmarshal(eff.GetDispatchCommand().CommandBytes); err == nil && cmd.GetGetUsageStats() != nil {
				dc = eff.GetDispatchCommand()
			}
		}
	}
	if dc == nil {
		t.Fatal("expected GetUsageStats DispatchCommand")
	}

	settleUpdateSettings(c, dc, 1_791_633_600_060, &v1.CommandResult{
		Outcome:         "executed",
		ResultAvailable: true,
		Kind: &v1.CommandResult_UsageStats{
			UsageStats: &v1.UsageStatsResponse{
				Preset:        v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS,
				Granularity:   v1.UsageBucketGranularity_USAGE_BUCKET_GRANULARITY_DAY,
				GeneratedAtMs: 1_791_633_600_055,
				Sources: []*v1.UsageAgentSource{
					{Agent: v1.Agent_AGENT_ANTIGRAVITY, State: v1.UsageSourceState_USAGE_SOURCE_STATE_OK},
				},
				Series: []*v1.UsageSeries{
					{
						Agent:               v1.Agent_AGENT_ANTIGRAVITY,
						Model:               "gemini-4-hydrogen",
						CacheWriteSupported: false,
						Current: &v1.UsagePeriodSummary{
							UncachedInputTokens: 30_000,
							CacheReadTokens:     70_000,
							CacheWriteTokens:    0,
							OutputTokens:        10_000,
							UsageRequestCount:   5,
							SpeedRequestCount:   0,
							HasCacheHitRate:     true,
							CacheHitRate:        0.7,
							HasSpeed:            false,
							HasTtft:             false,
						},
					},
				},
			},
		},
	}, "")

	uVM, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Usage{Usage: &corev1.UsageScreenRequest{MachineId: "m-1", Preset: v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS}}})
	if err != nil {
		t.Fatalf("View(Usage): %v", err)
	}
	u := uVM.GetUsage()
	if !u.GetHasAnyUsage() || u.GetHasAnySpeed() || !u.GetShowCacheWriteFootnote() || u.GetUnsupportedFootnote() != nil || len(u.GetSeries()) != 1 {
		t.Fatalf("unexpected Antigravity UsageViewModel: hasUsage=%v hasSpeed=%v cwFoot=%v unavail=%+v len=%d", u.GetHasAnyUsage(), u.GetHasAnySpeed(), u.GetShowCacheWriteFootnote(), u.GetUnsupportedFootnote(), len(u.GetSeries()))
	}

	msVM, err := c.View(&corev1.Screen{Screen: &corev1.Screen_MachineStatus{MachineStatus: &corev1.MachineStatusScreenRequest{MachineId: "m-1"}}})
	if err != nil {
		t.Fatalf("View(MachineStatus): %v", err)
	}
	mu := msVM.GetMachineStatus().GetModelUsage()
	if mu == nil || !mu.GetHasData() || mu.GetUnsupportedFootnote() != nil || len(mu.GetRows()) != 1 || mu.GetRows()[0].GetHasSpeed() {
		t.Fatalf("unexpected Antigravity MachineStatus preview: %+v", mu)
	}

	sessVM, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-1"}}})
	if err != nil {
		t.Fatalf("View(Session): %v", err)
	}
	ue := sessVM.GetSession().GetActions().GetUsageEntry()
	if ue == nil || ue.GetSourceUnavailable() || !ue.GetHasData() || ue.GetTotalTokens() != 110_000 || !ue.GetHasCacheHit() || ue.GetHasSpeed() {
		t.Fatalf("unexpected Antigravity Session UsageEntry: %+v", ue)
	}
}

func TestMachineSettingsOpenPrefetchesAllAgentsAndUsage(t *testing.T) {
	c := New()
	defer c.Close()
	seedDualCLIMachine(t, c)

	batch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_050,
		Event: &corev1.Event_MachineSettingsOpen{
			MachineSettingsOpen: &corev1.MachineSettingsOpenEvent{MachineId: "m-1"},
		},
	})

	listModelsAgents := map[v1.Agent]bool{}
	var gotSettings, gotUsage7d bool
	for _, eff := range batch.Effects {
		dc := eff.GetDispatchCommand()
		if dc == nil {
			continue
		}
		var cmd v1.Command
		if err := cmd.Unmarshal(dc.CommandBytes); err != nil {
			t.Fatalf("unmarshal command: %v", err)
		}
		if lm := cmd.GetListModels(); lm != nil {
			listModelsAgents[lm.Agent] = true
		}
		if cmd.GetGetSettings() != nil {
			gotSettings = true
		}
		if u := cmd.GetGetUsageStats(); u != nil && u.Preset == v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS {
			gotUsage7d = true
		}
	}
	for _, ag := range []v1.Agent{v1.Agent_AGENT_CODEX, v1.Agent_AGENT_CLAUDE, v1.Agent_AGENT_ANTIGRAVITY} {
		if !listModelsAgents[ag] {
			t.Fatalf("expected MachineSettingsOpen to prefetch ListModels for %v, got %+v", ag, listModelsAgents)
		}
	}
	if !gotSettings || !gotUsage7d {
		t.Fatalf("expected GetSettings (%v) and 7d GetUsageStats (%v)", gotSettings, gotUsage7d)
	}
}
