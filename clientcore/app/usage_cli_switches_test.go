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
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Security: &corev1.DemoSecurity{
					IsMember:          true,
					SelfDeviceId:      "dev-1",
					AuthorityDeviceId: "dev-1",
					LeaseEndMs:        1_891_633_600_000,
					TrustedMachineIds: []string{"m-1"},
				},
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
				OpId: 3,
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
		Event: &corev1.Event_DraftSetText{DraftSetText: &corev1.DraftSetTextAction{
			Scope: &corev1.DraftScope{Kind: corev1.DraftScopeKind_DRAFT_SCOPE_KIND_NEW_SESSION, Id: "/Users/me/proj"},
			Text:  "unsent draft text",
		}},
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
				OpId: 3,
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
							Hostname:         "MacStudio",
							ProtocolVersion:  17,
							SettingsRevision: 5,
							DisabledAgents:   []v1.Agent{v1.Agent_AGENT_CLAUDE, v1.Agent_AGENT_ANTIGRAVITY},
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
	if !u.GetHasAnyUsage() || len(u.GetRows()) != 1 || !u.GetHero().GetHasSelectedModel() || u.GetUnsupportedFootnote() == nil {
		t.Fatalf("unexpected UsageViewModel: hasUsage=%v rows=%d sel=%v footnote=%v", u.GetHasAnyUsage(), len(u.GetRows()), u.GetHero().GetHasSelectedModel(), u.GetUnsupportedFootnote())
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

	raw7d := (&v1.UsageStatsResponse{
		Preset:        v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS,
		GeneratedAtMs: 1_791_633_000_000,
	}).AppendMarshal(nil)
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_060,
		Event: &corev1.Event_UsageCacheLoaded{
			UsageCacheLoaded: &corev1.UsageCacheLoadedEvent{
				MachineId:   "m-1",
				Preset:      v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS,
				RawSnapshot: raw7d,
			},
		},
	})
	raw30d := (&v1.UsageStatsResponse{
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
	}).AppendMarshal(nil)
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_070,
		Event: &corev1.Event_UsageCacheLoaded{
			UsageCacheLoaded: &corev1.UsageCacheLoadedEvent{
				MachineId:   "m-1",
				Preset:      v1.UsageRangePreset_USAGE_RANGE_PRESET_THIRTY_DAYS,
				RawSnapshot: raw30d,
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
	if !u.GetCachedOffline() || u.GetCachedAtMs() != 1_791_633_100_000 || len(u.GetRows()) != 1 {
		t.Fatalf("unexpected offline UsageViewModel: offline=%v cachedAt=%d rows=%d", u.GetCachedOffline(), u.GetCachedAtMs(), len(u.GetRows()))
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
	if !u.GetHasAnyUsage() || u.GetOutputSpeedStrip().GetHasValue() || u.GetRows()[0].GetHasSpeed() || u.GetUnsupportedFootnote() != nil || len(u.GetRows()) != 1 {
		t.Fatalf("unexpected Antigravity UsageViewModel: hasUsage=%v stripHasSpeed=%v rowHasSpeed=%v unavail=%+v len=%d", u.GetHasAnyUsage(), u.GetOutputSpeedStrip().GetHasValue(), u.GetRows()[0].GetHasSpeed(), u.GetUnsupportedFootnote(), len(u.GetRows()))
	}
	if u.GetEmptyKind() != corev1.UsageEmptyKind_USAGE_EMPTY_KIND_NONE || u.GetHero().GetTotalTokensText().GetLiteral() != "11万" {
		t.Fatalf("unexpected formatted UsageViewModel hero/empty: empty=%v tok=%+v", u.GetEmptyKind(), u.GetHero().GetTotalTokensText())
	}
	if u.GetHero().GetSublineText() == nil || u.GetChartsA11YLabel().GetKey() != "usage.charts_a11y" || u.GetRows()[0].GetCacheHitText().GetLiteral() == "" {
		t.Fatalf("expected formatted subline/a11y/row cache hit on UsageViewModel")
	}

	msVM, err := c.View(&corev1.Screen{Screen: &corev1.Screen_MachineStatus{MachineStatus: &corev1.MachineStatusScreenRequest{MachineId: "m-1"}}})
	if err != nil {
		t.Fatalf("View(MachineStatus): %v", err)
	}
	mu := msVM.GetMachineStatus().GetModelUsage()
	if mu == nil || !mu.GetHasData() || mu.GetUnsupportedFootnote() != nil || len(mu.GetRows()) != 1 || mu.GetRows()[0].GetHasSpeed() {
		t.Fatalf("unexpected Antigravity MachineStatus preview: %+v", mu)
	}
	if mu.GetRows()[0].GetTotalTokensText().GetLiteral() != "11万" {
		t.Fatalf("expected zh-CN MachineStatus row tokens 11万, got %+v", mu.GetRows()[0].GetTotalTokensText())
	}

	msetVM, err := c.View(&corev1.Screen{Screen: &corev1.Screen_MachineSettings{MachineSettings: &corev1.MachineSettingsScreenRequest{MachineId: "m-1"}}})
	if err != nil {
		t.Fatalf("View(MachineSettings): %v", err)
	}
	if msetVM.GetMachineSettings().GetUsageTokensText().GetLiteral() != "11万" {
		t.Fatalf("expected zh-CN MachineSettings usage tokens 11万, got %+v", msetVM.GetMachineSettings().GetUsageTokensText())
	}

	sessVM, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-1"}}})
	if err != nil {
		t.Fatalf("View(Session): %v", err)
	}
	ue := sessVM.GetSession().GetActions().GetUsageEntry()
	if ue == nil || ue.GetSourceUnavailable() || !ue.GetHasData() || ue.GetTotalTokens() != 110_000 || !ue.GetHasCacheHit() || ue.GetHasSpeed() {
		t.Fatalf("unexpected Antigravity Session UsageEntry: %+v", ue)
	}
	if ue.GetValueText().GetLiteral() != "11万" || ue.GetSubtitle().GetKey() != "usage.entry_no_speed" {
		t.Fatalf("unexpected formatted Session UsageEntry: val=%+v sub=%+v", ue.GetValueText(), ue.GetSubtitle())
	}

	// Sending the default locale "zh-CN" on startup is a no-op (no redundant RenderEffect).
	noopBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_070,
		Event: &corev1.Event_LocaleChanged{LocaleChanged: &corev1.LocaleChangedEvent{UiLocale: "zh-CN"}},
	})
	for _, eff := range noopBatch.Effects {
		if eff.GetRender() != nil {
			t.Fatalf("expected no RenderEffect when locale stays zh-CN, got %+v", eff.GetRender())
		}
	}

	// Switching between language families refreshes Usage/MachineStatus/MachineSettings/Session,
	// while equivalent region/script variants (e.g. "en" -> "en-US", "zh-CN" -> "zh-Hans") are no-ops.
	enSteps := []struct {
		locale     string
		wantRender bool
		wantTokens string
	}{
		{"zh-Hans", false, "11万"},
		{"en", true, "110k"},
		{"en", false, "110k"},
		{"en-US", false, "110k"},
		{"zh-CN", true, "11万"},
		{"en-US", true, "110k"},
		{"", true, "11万"},
	}
	for i, tc := range enSteps {
		batch := c.Dispatch(&corev1.Event{
			NowMs: 1_791_633_600_080 + int64(i)*10,
			Event: &corev1.Event_LocaleChanged{LocaleChanged: &corev1.LocaleChangedEvent{UiLocale: tc.locale}},
		})
		var rendered []corev1.ScreenTarget
		for _, eff := range batch.Effects {
			if r := eff.GetRender(); r != nil {
				rendered = append(rendered, r.Screens...)
			}
		}
		if tc.wantRender {
			for _, wantTarget := range []corev1.ScreenTarget{
				corev1.ScreenTarget_SCREEN_TARGET_USAGE,
				corev1.ScreenTarget_SCREEN_TARGET_MACHINE_STATUS,
				corev1.ScreenTarget_SCREEN_TARGET_MACHINE_SETTINGS,
				corev1.ScreenTarget_SCREEN_TARGET_SESSION,
			} {
				found := false
				for _, got := range rendered {
					if got == wantTarget {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("step %d (locale=%q): missing render target %v in %v", i, tc.locale, wantTarget, rendered)
				}
			}
		} else if len(rendered) != 0 {
			t.Fatalf("step %d (locale=%q): expected no render targets, got %v", i, tc.locale, rendered)
		}

		u2, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Usage{Usage: &corev1.UsageScreenRequest{MachineId: "m-1", Preset: v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS}}})
		if got := u2.GetUsage().GetHero().GetTotalTokensText().GetLiteral(); got != tc.wantTokens {
			t.Fatalf("step %d (locale=%q): Usage hero tokens = %q, want %q", i, tc.locale, got, tc.wantTokens)
		}
		ms2, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_MachineStatus{MachineStatus: &corev1.MachineStatusScreenRequest{MachineId: "m-1"}}})
		if got := ms2.GetMachineStatus().GetModelUsage().GetRows()[0].GetTotalTokensText().GetLiteral(); got != tc.wantTokens {
			t.Fatalf("step %d (locale=%q): MachineStatus row tokens = %q, want %q", i, tc.locale, got, tc.wantTokens)
		}
		mset2, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_MachineSettings{MachineSettings: &corev1.MachineSettingsScreenRequest{MachineId: "m-1"}}})
		if got := mset2.GetMachineSettings().GetUsageTokensText().GetLiteral(); got != tc.wantTokens {
			t.Fatalf("step %d (locale=%q): MachineSettings usage tokens = %q, want %q", i, tc.locale, got, tc.wantTokens)
		}
		sess2, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Session{Session: &corev1.SessionScreenRequest{SessionId: "s-1"}}})
		if got := sess2.GetSession().GetActions().GetUsageEntry().GetValueText().GetLiteral(); got != tc.wantTokens {
			t.Fatalf("step %d (locale=%q): Session usage entry tokens = %q, want %q", i, tc.locale, got, tc.wantTokens)
		}
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

func TestUpdateMachineAgentDefaultsClearsEffortOnModelChangeWithoutEffort(t *testing.T) {
	c := New()
	defer c.Close()
	seedDualCLIMachine(t, c)
	openBatch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_050,
		Event: &corev1.Event_MachineSettingsOpen{
			MachineSettingsOpen: &corev1.MachineSettingsOpenEvent{MachineId: "m-1"},
		},
	})
	var getDC *corev1.DispatchCommandEffect
	for _, eff := range openBatch.Effects {
		if dc := eff.GetDispatchCommand(); dc != nil {
			var cmd v1.Command
			if err := cmd.Unmarshal(dc.CommandBytes); err == nil && cmd.GetGetSettings() != nil {
				getDC = dc
			}
		}
	}
	if getDC == nil {
		t.Fatal("expected GetSettings dispatch")
	}
	settleUpdateSettings(c, getDC, 1_791_633_600_055, &v1.CommandResult{
		Outcome:         "executed",
		ResultAvailable: true,
		Kind: &v1.CommandResult_GetSettings{
			GetSettings: &v1.GetSettingsResponse{
				Settings: &v1.DaemonSettings{
					DefaultModel:  "gpt-5",
					DefaultEffort: "high",
				},
			},
		},
	}, "")

	modelB := "gpt-5-mini"
	batch := c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_060,
		Event: &corev1.Event_UpdateMachineAgentDefaults{
			UpdateMachineAgentDefaults: &corev1.UpdateMachineAgentDefaultsEvent{
				MachineId: "m-1",
				Agent:     v1.Agent_AGENT_CODEX,
				Model:     &modelB,
			},
		},
	})
	var updated *v1.DaemonSettings
	for _, eff := range batch.Effects {
		if dc := eff.GetDispatchCommand(); dc != nil {
			var cmd v1.Command
			if err := cmd.Unmarshal(dc.CommandBytes); err == nil && cmd.GetUpdateSettings() != nil {
				updated = cmd.GetUpdateSettings().GetSettings()
			}
		}
	}
	if updated == nil {
		t.Fatalf("expected UpdateSettings command")
	}
	if got := updated.GetDefaultModel(); got != modelB {
		t.Fatalf("expected model %q, got %q", modelB, got)
	}
	if got := updated.GetDefaultEffort(); got != "" {
		t.Fatalf("expected stale effort cleared when model switches without effort, got %q", got)
	}
}

func TestUsageScreenSortSeriesToggleAndScrubEvents(t *testing.T) {
	c := New()
	defer c.Close()
	seedDualCLIMachine(t, c)

	bOpen := c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_050,
		Event: &corev1.Event_UsageScreenOpen{
			UsageScreenOpen: &corev1.UsageScreenOpenEvent{
				MachineId:  "m-1",
				Preset:     v1.UsageRangePreset_USAGE_RANGE_PRESET_TODAY,
				ApplyFocus: true,
			},
		},
	})
	var dc *corev1.DispatchCommandEffect
	for _, eff := range bOpen.Effects {
		if eff.GetDispatchCommand() != nil {
			var cmd v1.Command
			if err := cmd.Unmarshal(eff.GetDispatchCommand().CommandBytes); err == nil && cmd.GetGetUsageStats() != nil {
				dc = eff.GetDispatchCommand()
			}
		}
	}
	if dc == nil {
		t.Fatal("expected GetUsageStats dispatch")
	}
	settleUpdateSettings(c, dc, 1_791_633_600_060, &v1.CommandResult{
		Outcome:         "executed",
		ResultAvailable: true,
		Kind: &v1.CommandResult_UsageStats{
			UsageStats: &v1.UsageStatsResponse{
				Preset:      v1.UsageRangePreset_USAGE_RANGE_PRESET_TODAY,
				Granularity: v1.UsageBucketGranularity_USAGE_BUCKET_GRANULARITY_HOUR,
				StartMs:     1_791_590_400_000,
				EndMs:       1_791_676_800_000,
				Series: []*v1.UsageSeries{
					{
						Agent: v1.Agent_AGENT_CLAUDE,
						Model: "claude-opus-4-6",
						Current: &v1.UsagePeriodSummary{
							UncachedInputTokens: 200_000,
							OutputTokens:        50_000,
							UsageRequestCount:   10,
							SpeedRequestCount:   10,
							HasSpeed:            true,
							SpeedP50:            50,
							HasTtft:             true,
							TtftP50Ms:           1200,
						},
						Buckets: []*v1.UsageBucketPoint{
							{StartMs: 1_791_590_400_000, EndMs: 1_791_594_000_000, UncachedInputTokens: 150_000, OutputTokens: 30_000, UsageRequestCount: 6, SpeedRequestCount: 6, SpeedP50: 48, TtftP50Ms: 1300},
							{StartMs: 1_791_594_000_000, EndMs: 1_791_597_600_000, UncachedInputTokens: 50_000, OutputTokens: 20_000, UsageRequestCount: 4, SpeedRequestCount: 4, SpeedP50: 54, TtftP50Ms: 1100},
						},
					},
					{
						Agent: v1.Agent_AGENT_CODEX,
						Model: "gpt-5.4",
						Current: &v1.UsagePeriodSummary{
							UncachedInputTokens: 100_000,
							OutputTokens:        20_000,
							UsageRequestCount:   5,
							SpeedRequestCount:   5,
							HasSpeed:            true,
							SpeedP50:            120,
							HasTtft:             true,
							TtftP50Ms:           400,
						},
						Buckets: []*v1.UsageBucketPoint{
							{StartMs: 1_791_590_400_000, EndMs: 1_791_594_000_000, UncachedInputTokens: 100_000, OutputTokens: 20_000, UsageRequestCount: 5, SpeedRequestCount: 5, SpeedP50: 120, TtftP50Ms: 400},
							{StartMs: 1_791_594_000_000, EndMs: 1_791_597_600_000},
						},
					},
				},
			},
		},
	}, "")

	// Sort by Speed desc -> Codex first.
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_070,
		Event: &corev1.Event_UsageScreenSelectSort{
			UsageScreenSelectSort: &corev1.UsageScreenSelectSortEvent{
				MachineId: "m-1",
				SortBy:    corev1.UsageSortColumn_USAGE_SORT_COLUMN_SPEED,
			},
		},
	})
	vm1, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Usage{Usage: &corev1.UsageScreenRequest{MachineId: "m-1", Preset: v1.UsageRangePreset_USAGE_RANGE_PRESET_TODAY}}})
	if vm1.GetUsage().GetSortBy() != corev1.UsageSortColumn_USAGE_SORT_COLUMN_SPEED || vm1.GetUsage().GetRows()[0].GetModel() != "gpt-5.4" {
		t.Fatalf("expected speed sort with gpt-5.4 first, got %+v", vm1.GetUsage().GetRows())
	}

	// Select Codex series -> Hero switches to Codex.
	codexKey := vm1.GetUsage().GetRows()[0].GetSeriesKey()
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_080,
		Event: &corev1.Event_UsageScreenSelectSeries{
			UsageScreenSelectSeries: &corev1.UsageScreenSelectSeriesEvent{
				MachineId: "m-1",
				SeriesKey: codexKey,
				Toggle:    true,
			},
		},
	})
	vm2, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Usage{Usage: &corev1.UsageScreenRequest{MachineId: "m-1", Preset: v1.UsageRangePreset_USAGE_RANGE_PRESET_TODAY}}})
	if !vm2.GetUsage().GetHero().GetHasSelectedModel() || vm2.GetUsage().GetHero().GetSelectedModel() != "gpt-5.4" || vm2.GetUsage().GetHero().GetTotalTokens() != 120_000 {
		t.Fatalf("expected selected gpt-5.4 hero, got %+v", vm2.GetUsage().GetHero())
	}

	// Scrub bucket 0, then clear scrub.
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_090,
		Event: &corev1.Event_UsageScreenScrub{
			UsageScreenScrub: &corev1.UsageScreenScrubEvent{
				MachineId:   "m-1",
				BucketIndex: 0,
			},
		},
	})
	vm3, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Usage{Usage: &corev1.UsageScreenRequest{MachineId: "m-1", Preset: v1.UsageRangePreset_USAGE_RANGE_PRESET_TODAY}}})
	if !vm3.GetUsage().GetHero().GetScrubbing() || vm3.GetUsage().GetHero().GetTotalTokens() != 120_000 || vm3.GetUsage().GetOutputSpeedStrip().GetCurrentValue() != 120 {
		t.Fatalf("unexpected scrub state: hero=%+v speed=%+v", vm3.GetUsage().GetHero(), vm3.GetUsage().GetOutputSpeedStrip())
	}
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_100,
		Event: &corev1.Event_UsageScreenScrub{
			UsageScreenScrub: &corev1.UsageScreenScrubEvent{
				MachineId: "m-1",
				Clear:     true,
			},
		},
	})

	// Toggle same series again -> clears selection back to All.
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_633_600_110,
		Event: &corev1.Event_UsageScreenSelectSeries{
			UsageScreenSelectSeries: &corev1.UsageScreenSelectSeriesEvent{
				MachineId: "m-1",
				SeriesKey: codexKey,
				Toggle:    true,
			},
		},
	})
	vm4, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_Usage{Usage: &corev1.UsageScreenRequest{MachineId: "m-1", Preset: v1.UsageRangePreset_USAGE_RANGE_PRESET_TODAY}}})
	if vm4.GetUsage().GetHero().GetHasSelectedModel() || vm4.GetUsage().GetHero().GetTotalTokens() != 370_000 {
		t.Fatalf("expected toggle off back to all models, got %+v", vm4.GetUsage().GetHero())
	}
}
