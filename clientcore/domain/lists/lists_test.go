package lists

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/outcome"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	stdv1 "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func stdToLite[Std any, PStd interface {
	*Std
	proto.Message
}, Lite any, PLite interface {
	*Lite
	Unmarshal([]byte) error
}](t testing.TB, raw json.RawMessage) PLite {
	t.Helper()
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var std Std
	if err := protojson.Unmarshal(raw, PStd(&std)); err != nil {
		t.Fatalf("protojson unmarshal %T: %v", std, err)
	}
	wire, err := proto.Marshal(PStd(&std))
	if err != nil {
		t.Fatalf("proto marshal %T: %v", std, err)
	}
	var lite Lite
	if err := PLite(&lite).Unmarshal(wire); err != nil {
		t.Fatalf("lite unmarshal %T: %v", lite, err)
	}
	return &lite
}

type vecPlace struct {
	EndedMs   string `json:"endedMs"`
	SessionID string `json:"sessionId"`
}

func (p *vecPlace) toPlace() *SessionPlace {
	if p == nil {
		return nil
	}
	ms, _ := strconv.ParseInt(p.EndedMs, 10, 64)
	return &SessionPlace{EndedMs: ms, SessionID: p.SessionID}
}

type vecSnapshot struct {
	Loaded         bool            `json:"loaded"`
	Synced         bool            `json:"synced"`
	Conn           int32           `json:"conn"`
	Link           int32           `json:"link"`
	SessionsListed bool            `json:"sessionsListed"`
	SessionsNext   *vecPlace       `json:"sessionsNext"`
	SessionsError  string          `json:"sessionsError"`
	Unlinked       *bool           `json:"unlinked"`
	Machines       []struct {
		ID              string          `json:"id"`
		Online          bool            `json:"online"`
		Reachable       *bool           `json:"reachable"`
		Unlinked        *bool           `json:"unlinked"`
		ProtocolVersion uint32          `json:"protocolVersion"`
		Meta            json.RawMessage `json:"meta"`
	} `json:"machines"`
	Sessions []struct {
		ID                string          `json:"id"`
		MachineID         string          `json:"machineId"`
		UpdatedMs         string          `json:"updatedMs"`
		ListedEndedMs     string          `json:"listedEndedMs"`
		EndedLocalYear    int32           `json:"endedLocalYear"`
		EndedLocalMonth   int32           `json:"endedLocalMonth"`
		CreatedLocalHhmm  string          `json:"createdLocalHhmm"`
		Info              json.RawMessage `json:"info"`
	} `json:"sessions"`
	Prefs json.RawMessage `json:"prefs"`
}

func (s *vecSnapshot) toWorld(t testing.TB) *World {
	t.Helper()
	if s == nil {
		return nil
	}
	w := &World{
		LiveLoaded:       s.Loaded,
		SessionsListed:   s.SessionsListed,
		SessionsNext:     s.SessionsNext.toPlace(),
		SessionListError: s.SessionsError,
		Unlinked:         s.Unlinked,
		Connection:       ConnConnected,
	}
	switch s.Link {
	case 2:
		w.Connection = ConnConnecting
		if s.Synced {
			w.Connection = ConnReconnecting
		}
	case 3:
		w.Connection = ConnNoNetwork
	case 4:
		w.Connection = ConnOffline
	}
	for _, m := range s.Machines {
		var meta *v1.MachineMeta
		if len(m.Meta) > 0 {
			meta = stdToLite[stdv1.MachineMeta, *stdv1.MachineMeta, v1.MachineMeta, *v1.MachineMeta](t, m.Meta)
		}
		w.Machines = append(w.Machines, &Machine{
			ID:              m.ID,
			Online:          m.Online,
			Reachable:       m.Reachable,
			Unlinked:        m.Unlinked,
			ProtocolVersion: m.ProtocolVersion,
			Meta:            meta,
		})
	}
	for _, sess := range s.Sessions {
		up, _ := strconv.ParseInt(sess.UpdatedMs, 10, 64)
		le, _ := strconv.ParseInt(sess.ListedEndedMs, 10, 64)
		var info *v1.Session
		if len(sess.Info) > 0 {
			info = stdToLite[stdv1.Session, *stdv1.Session, v1.Session, *v1.Session](t, sess.Info)
		}
		w.Sessions = append(w.Sessions, &Session{
			ID:               sess.ID,
			MachineID:        sess.MachineID,
			UpdatedMs:        up,
			ListedEndedMs:    le,
			EndedLocalYear:   sess.EndedLocalYear,
			EndedLocalMonth:  sess.EndedLocalMonth,
			CreatedLocalHhmm: sess.CreatedLocalHhmm,
			Info:             info,
		})
	}
	if len(s.Prefs) > 0 {
		w.Prefs = stdToLite[stdv1.ProjectPrefs, *stdv1.ProjectPrefs, v1.ProjectPrefs, *v1.ProjectPrefs](t, s.Prefs)
	}
	return w
}

type vecScope struct {
	All bool   `json:"all"`
	Key string `json:"key"`
}

func (s *vecScope) toScope() *Scope {
	if s == nil {
		return nil
	}
	return &Scope{AllProjects: s.All, Key: s.Key}
}

func TestSharedListsVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "lists", "vectors.json"))
	if err != nil {
		t.Fatalf("read vectors.json: %v", err)
	}
	var doc struct {
		ListsVersion int `json:"listsVersion"`
		Projects     []struct {
			Name    string `json:"name"`
			Request struct {
				Snapshot        *vecSnapshot `json:"snapshot"`
				RememberedScope *vecScope    `json:"rememberedScope"`
				ScopeHmacKey    string       `json:"scopeHmacKey"`
				Search          string       `json:"search"`
			} `json:"request"`
			Expected struct {
				AllKeys            []string         `json:"allKeys"`
				PinnedKeys         []string         `json:"pinnedKeys"`
				OthersKeys         []string         `json:"othersKeys"`
				HiddenKeys         []string         `json:"hiddenKeys"`
				ListedKeys         []string         `json:"listedKeys"`
				EndedCounts        map[string]int32 `json:"endedCounts"`
				AMachineIDs        []string         `json:"aMachineIds"`
				AMachineNames      []string         `json:"aMachineNames"`
				DefaultScopeKey    string           `json:"defaultScopeKey"`
				ResolvedScopeAll   bool             `json:"resolvedScopeAll"`
				ResolvedScopeKey   string           `json:"resolvedScopeKey"`
				ResolvedScopeToken string           `json:"resolvedScopeToken"`
				WaitingTotal       int32            `json:"waitingTotal"`
			} `json:"expected"`
		} `json:"projects"`
		HomeList []struct {
			Name    string `json:"name"`
			Request struct {
				Snapshot *vecSnapshot `json:"snapshot"`
				Scope    *vecScope    `json:"scope"`
				Calendar struct {
					NowMs            string `json:"nowMs"`
					UTCOffsetMinutes int32  `json:"utcOffsetMinutes"`
				} `json:"calendar"`
			} `json:"request"`
			Expected struct {
				RowIDs               []string `json:"rowIds"`
				HeadlineKind         int32    `json:"headlineKind"`
				HeadlineWaitingCount int32    `json:"headlineWaitingCount"`
				HeadlineRunningCount int32    `json:"headlineRunningCount"`
				HeadlineIdleCount    int32    `json:"headlineIdleCount"`
				WeekWeekdayIndex     int32    `json:"weekWeekdayIndex"`
				AttentionCardCount   *int     `json:"attentionCardCount"`
				ApprovalRawStatus    int32    `json:"approvalRawStatus"`
				ApprovalOutsideScope bool     `json:"approvalOutsideScope"`
			} `json:"expected"`
		} `json:"homeList"`
		NewConfig []struct {
			Name    string `json:"name"`
			Request struct {
				Snapshot   *vecSnapshot `json:"snapshot"`
				ProjectKey string       `json:"projectKey"`
				PickAgent  int32        `json:"pickAgent"`
			} `json:"request"`
			Expected struct {
				Found            bool   `json:"found"`
				MachineID        string `json:"machineId"`
				Agent            int32  `json:"agent"`
				Model            string `json:"model"`
				Effort           string `json:"effort"`
				Mode             int32  `json:"mode"`
				Settled          bool   `json:"settled"`
				DefaultMode      int32  `json:"defaultMode"`
				DefaultModeKnown bool   `json:"defaultModeKnown"`
				WantedAgent      int32  `json:"wantedAgent"`
				MissingAgent     int32  `json:"missingAgent"`
			} `json:"expected"`
		} `json:"newConfig"`
		ModelsModes []struct {
			Name    string `json:"name"`
			Request struct {
				Config struct {
					MachineID string `json:"machineId"`
					Cwd       string `json:"cwd"`
					Agent     int32  `json:"agent"`
					Model     string `json:"model"`
					Mode      int32  `json:"mode"`
				} `json:"config"`
				HasModels   bool            `json:"hasModels"`
				Models      json.RawMessage `json:"models"`
				DefaultMode struct {
					Mode  int32 `json:"mode"`
					Known bool  `json:"known"`
				} `json:"defaultMode"`
				SessionAgent                 int32  `json:"sessionAgent"`
				SessionModel                 string `json:"sessionModel"`
				SessionModeLocked            bool   `json:"sessionModeLocked"`
				SessionFullAccessUnavailable bool   `json:"sessionFullAccessUnavailable"`
			} `json:"request"`
			Expected struct {
				PickedSlug             string `json:"pickedSlug"`
				ModelLabel             string `json:"modelLabel"`
				AutoIssue              int32  `json:"autoIssue"`
				SelectedEffort         string `json:"selectedEffort"`
				ContextFixed           bool   `json:"contextFixed"`
				AllAutoUnavailable     bool   `json:"allAutoUnavailable"`
				SettingsModeRed        bool   `json:"settingsModeRed"`
				SessionFullUnavailable int32  `json:"sessionFullUnavailable"`
				SessionAutoUnavailable int32  `json:"sessionAutoUnavailable"`
			} `json:"expected"`
		} `json:"modelsModes"`
		PrefsOps []struct {
			Name  string          `json:"name"`
			Base  json.RawMessage `json:"base"`
			Steps []struct {
				OpID  string `json:"opId"`
				NowMs string `json:"nowMs"`
				Op    struct {
					Pin         *struct{ Path string `json:"path"` } `json:"pin"`
					Hide        *struct{ Path string `json:"path"` } `json:"hide"`
					SetSettings *struct {
						Path      string `json:"path"`
						MachineID string `json:"machineId"`
						Moved     bool   `json:"moved"`
						Agent     int32  `json:"agent"`
					} `json:"setSettings"`
				} `json:"op"`
			} `json:"steps"`
			Expected struct {
				Pinned             []string `json:"pinned"`
				Hidden             []string `json:"hidden"`
				RecentOps          []string `json:"recentOps"`
				SettingsMachineID  string   `json:"settingsMachineId"`
				SettingsAgent      int32    `json:"settingsAgent"`
				SettingsAgentCount int      `json:"settingsAgentCount"`
			} `json:"expected"`
		} `json:"prefsOps"`
		Pagination []struct {
			Name    string `json:"name"`
			Request struct {
				Snapshot  *vecSnapshot `json:"snapshot"`
				HeldEnded []struct {
					ID            string `json:"id"`
					MachineID     string `json:"machineId"`
					ListedEndedMs string `json:"listedEndedMs"`
				} `json:"heldEnded"`
				Want              int                     `json:"want"`
				Total             int                     `json:"total"`
				ReconcileSessions []struct {
					ID            string `json:"id"`
					MachineID     string `json:"machineId"`
					ListedEndedMs string `json:"listedEndedMs"`
				} `json:"reconcileSessions"`
				PageSessionIDs   []string  `json:"pageSessionIds"`
				PushedSessionIDs []string  `json:"pushedSessionIds"`
				KnownMachineIDs  []string  `json:"knownMachineIds"`
				PageNext         *vecPlace `json:"pageNext"`
			} `json:"request"`
			Expected struct {
				NeedsEndedPage   bool     `json:"needsEndedPage"`
				PastFetchedIDs   []string `json:"pastFetchedIds"`
				RemoveSessionIDs []string `json:"removeSessionIds"`
				UnlistSessionIDs []string `json:"unlistSessionIds"`
			} `json:"expected"`
		} `json:"pagination"`
		PathsDisplay struct {
			ShortenPaths []struct {
				Text     string `json:"text"`
				Cwd      string `json:"cwd"`
				Home     string `json:"home"`
				Expected string `json:"expected"`
			} `json:"shortenPaths"`
			ShortenPath []struct {
				Path     string `json:"path"`
				Cwd      string `json:"cwd"`
				Home     string `json:"home"`
				Expected string `json:"expected"`
			} `json:"shortenPath"`
			ApprovalDisplay []struct {
				Input    string `json:"input"`
				Expected string `json:"expected"`
			} `json:"approvalDisplay"`
			ApprovalAlways []struct {
				Input string `json:"input"`
				Kind  int32  `json:"kind"`
				Arg   string `json:"arg"`
			} `json:"approvalAlways"`
		} `json:"pathsDisplay"`
		Outcome []struct {
			Name         string          `json:"name"`
			Result       json.RawMessage `json:"result"`
			KeyByte      byte            `json:"keyByte"`
			PayloadByte  byte            `json:"payloadByte"`
			OuterOutcome string          `json:"outerOutcome"`
			OuterReason  string          `json:"outerReason"`
			Prior        bool            `json:"prior"`
			Expected     int32           `json:"expected"`
		} `json:"outcome"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse vectors.json: %v", err)
	}

	projectKeys := func(xs []*ProjectSummary) []string {
		out := make([]string, len(xs))
		for i, x := range xs {
			out[i] = x.Key
		}
		return out
	}

	for _, tc := range doc.Projects {
		t.Run("projects/"+tc.Name, func(t *testing.T) {
			w := tc.Request.Snapshot.toWorld(t)
			var hmacKey []byte
			if tc.Request.ScopeHmacKey != "" {
				hmacKey, err = base64.StdEncoding.DecodeString(tc.Request.ScopeHmacKey)
				if err != nil {
					t.Fatalf("decode scopeHmacKey: %v", err)
				}
			}
			resp := BuildProjects(w, tc.Request.Search, tc.Request.RememberedScope.toScope(), "", hmacKey, nil)
			if got := projectKeys(resp.All); !slices.Equal(got, tc.Expected.AllKeys) {
				t.Fatalf("allKeys: got %v, want %v", got, tc.Expected.AllKeys)
			}
			if tc.Expected.PinnedKeys != nil {
				if got := projectKeys(resp.Pinned); !slices.Equal(got, tc.Expected.PinnedKeys) {
					t.Fatalf("pinnedKeys: got %v, want %v", got, tc.Expected.PinnedKeys)
				}
			}
			if tc.Expected.OthersKeys != nil {
				if got := projectKeys(resp.Others); !slices.Equal(got, tc.Expected.OthersKeys) {
					t.Fatalf("othersKeys: got %v, want %v", got, tc.Expected.OthersKeys)
				}
			}
			if tc.Expected.HiddenKeys != nil {
				if got := projectKeys(resp.Hidden); !slices.Equal(got, tc.Expected.HiddenKeys) {
					t.Fatalf("hiddenKeys: got %v, want %v", got, tc.Expected.HiddenKeys)
				}
			}
			if got := projectKeys(resp.Listed); !slices.Equal(got, tc.Expected.ListedKeys) {
				t.Fatalf("listedKeys: got %v, want %v", got, tc.Expected.ListedKeys)
			}
			for k, wantEnded := range tc.Expected.EndedCounts {
				var found *ProjectSummary
				for _, p := range resp.All {
					if p.Key == k {
						found = p
						break
					}
				}
				if found == nil || found.Ended != wantEnded {
					t.Fatalf("ended[%s]: got %v, want %d", k, found, wantEnded)
				}
			}
			if tc.Expected.AMachineIDs != nil {
				for _, p := range resp.All {
					if p.Key == "/Code/a" {
						if !slices.Equal(p.MachineIDs, tc.Expected.AMachineIDs) || !slices.Equal(p.MachineNames, tc.Expected.AMachineNames) {
							t.Fatalf("/Code/a machines: got %v / %v, want %v / %v", p.MachineIDs, p.MachineNames, tc.Expected.AMachineIDs, tc.Expected.AMachineNames)
						}
					}
				}
			}
			if resp.ResolvedScope.AllProjects != tc.Expected.ResolvedScopeAll || resp.ResolvedScope.Key != tc.Expected.ResolvedScopeKey {
				t.Fatalf("resolvedScope: got %+v, want all=%v key=%q", resp.ResolvedScope, tc.Expected.ResolvedScopeAll, tc.Expected.ResolvedScopeKey)
			}
			if tc.Expected.DefaultScopeKey != "" && resp.DefaultScope.Key != tc.Expected.DefaultScopeKey {
				t.Fatalf("defaultScope: got %+v, want key=%q", resp.DefaultScope, tc.Expected.DefaultScopeKey)
			}
			if tc.Expected.ResolvedScopeToken != "" && resp.ResolvedToken != tc.Expected.ResolvedScopeToken {
				t.Fatalf("resolvedScopeToken: got %q, want %q", resp.ResolvedToken, tc.Expected.ResolvedScopeToken)
			}
			if resp.WaitingTotal != tc.Expected.WaitingTotal {
				t.Fatalf("waitingTotal: got %d, want %d", resp.WaitingTotal, tc.Expected.WaitingTotal)
			}
		})
	}

	// Map p3 HomeHeadlineKind numbers (5 = WAITING)
	headlineToVecNum := func(k HomeHeadlineKind) int32 {
		switch k {
		case HeadlineWaiting:
			return 5
		case HeadlineRunning:
			return 6
		case HeadlineEmptyProject:
			return 7
		case HeadlineUnreadable:
			return 8
		case HeadlineNoSessions:
			return 9
		case HeadlineNothingWaits:
			return 10
		default:
			return int32(k)
		}
	}
	// Map domain Status to p3 Status numeric constants for approvalRawStatus vector check (Offline = 3).
	statusToVecNum := func(s SessionDisplayStatus) int32 {
		switch s {
		case StatusOffline:
			return 3
		case StatusApproval:
			return 4
		case StatusQuestion:
			return 5
		case StatusPlan:
			return 6
		case StatusPopup:
			return 7
		default:
			return int32(s)
		}
	}

	for _, tc := range doc.HomeList {
		t.Run("homeList/"+tc.Name, func(t *testing.T) {
			w := tc.Request.Snapshot.toWorld(t)
			nowMs, _ := strconv.ParseInt(tc.Request.Calendar.NowMs, 10, 64)
			cal := Calendar{NowMs: nowMs, UTCOffsetMinutes: tc.Request.Calendar.UTCOffsetMinutes}
			resp := BuildHome(w, tc.Request.Scope.toScope(), false, "", cal, HomeShown{})
			rowIDs := make([]string, len(resp.Items))
			cardCount := 0
			var approvalItem *HomeRowData
			var weekLabel *HomeRowData
			for i, item := range resp.Items {
				rowIDs[i] = item.RowID
				if item.Kind == HomeRowAttentionCard {
					cardCount++
				}
				if item.RowID == "s:approval" {
					approvalItem = item
				}
				if item.RowID == "h:week" {
					weekLabel = item
				}
			}
			if !slices.Equal(rowIDs, tc.Expected.RowIDs) {
				t.Fatalf("rowIds: got %v, want %v", rowIDs, tc.Expected.RowIDs)
			}
			if headlineToVecNum(resp.Headline.Kind) != tc.Expected.HeadlineKind ||
				resp.Headline.WaitingCount != tc.Expected.HeadlineWaitingCount ||
				resp.Headline.RunningCount != tc.Expected.HeadlineRunningCount ||
				resp.Headline.IdleCount != tc.Expected.HeadlineIdleCount {
				t.Fatalf("headline mismatch: got %+v", resp.Headline)
			}
			if tc.Expected.AttentionCardCount != nil && cardCount != *tc.Expected.AttentionCardCount {
				t.Fatalf("attentionCardCount: got %d, want %d", cardCount, *tc.Expected.AttentionCardCount)
			}
			if weekLabel != nil && weekLabel.EndedGroup.WeekdayIndex != tc.Expected.WeekWeekdayIndex {
				t.Fatalf("weekWeekdayIndex: got %d, want %d", weekLabel.EndedGroup.WeekdayIndex, tc.Expected.WeekWeekdayIndex)
			}
			if approvalItem != nil {
				if statusToVecNum(approvalItem.Item.RawStatus) != tc.Expected.ApprovalRawStatus ||
					approvalItem.Item.OutsideScope != tc.Expected.ApprovalOutsideScope {
					t.Fatalf("approvalItem mismatch: %+v", approvalItem.Item)
				}
			}
		})
	}

	for _, tc := range doc.NewConfig {
		t.Run("newConfig/"+tc.Name, func(t *testing.T) {
			w := tc.Request.Snapshot.toWorld(t)
			resp := ResolveProjectStart(w, tc.Request.ProjectKey, "", v1.Agent(tc.Request.PickAgent), nil, false)
			if resp.Found != tc.Expected.Found ||
				resp.Config.MachineID != tc.Expected.MachineID ||
				int32(resp.Config.Agent) != tc.Expected.Agent ||
				resp.Config.Model != tc.Expected.Model ||
				resp.Config.Effort != tc.Expected.Effort ||
				int32(resp.Config.Mode) != tc.Expected.Mode ||
				resp.Settled != tc.Expected.Settled ||
				int32(resp.WantedAgent) != tc.Expected.WantedAgent ||
				int32(resp.MissingAgent) != tc.Expected.MissingAgent {
				t.Fatalf("ResolveProjectStart mismatch: got %+v", resp)
			}
			if tc.Expected.DefaultModeKnown {
				if !resp.DefaultMode.Known || int32(resp.DefaultMode.Mode) != tc.Expected.DefaultMode {
					t.Fatalf("defaultMode mismatch: got %+v", resp.DefaultMode)
				}
			}
		})
	}

	// Map domain ModeUnavailableReason to p3 enum numbers for vector check
	modeReasonToVecNum := func(r ModeUnavailableReason) int32 {
		switch r {
		case ModeLockedFullAccess:
			return 1
		case ModeFullAccessRequiresStart:
			return 2
		case ModeAccountNoAuto:
			return 3
		case ModeModelNoAuto:
			return 4
		default:
			return 0
		}
	}

	for _, tc := range doc.ModelsModes {
		t.Run("modelsModes/"+tc.Name, func(t *testing.T) {
			c := StartConfig{
				MachineID: tc.Request.Config.MachineID,
				Cwd:       tc.Request.Config.Cwd,
				Agent:     v1.Agent(tc.Request.Config.Agent),
				Model:     tc.Request.Config.Model,
				Mode:      v1.ApprovalMode(tc.Request.Config.Mode),
			}
			models := stdToLite[stdv1.ListModelsResponse, *stdv1.ListModelsResponse, v1.ListModelsResponse, *v1.ListModelsResponse](t, tc.Request.Models)
			var modelList []*v1.ModelInfo
			if tc.Request.HasModels && models != nil {
				modelList = models.Models
			}
			picked := ProjectModel(c, models, nil)
			pickedSlug := ""
			if picked != nil {
				pickedSlug = picked.Slug
			}
			label := ModelLabel(modelList, tc.Request.HasModels, c.Model, c.Agent)
			effMode := c.Mode
			if effMode == v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED && tc.Request.DefaultMode.Known {
				effMode = v1.ApprovalMode(tc.Request.DefaultMode.Mode)
			}
			autoIss := AutoIssue(c.Agent, c.Model, effMode, modelList, tc.Request.HasModels)
			_, selectedEffort := EffortChoices(c, models, nil)
			ctxFixed := AgentInfo(c.Agent).StandardContextOption && picked != nil && picked.ContextWindow <= StandardContextWindow
			allAutoUnavail := AllAutoUnavailable(modelList)
			settingsRed := c.Mode == v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS || (!tc.Request.DefaultMode.Known && c.Mode == v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS) || (c.Mode == v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED && v1.ApprovalMode(tc.Request.DefaultMode.Mode) == v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS)

			if pickedSlug != tc.Expected.PickedSlug ||
				label != tc.Expected.ModelLabel ||
				int32(autoIss) != tc.Expected.AutoIssue ||
				selectedEffort != tc.Expected.SelectedEffort ||
				ctxFixed != tc.Expected.ContextFixed ||
				allAutoUnavail != tc.Expected.AllAutoUnavailable ||
				settingsRed != tc.Expected.SettingsModeRed {
				t.Fatalf("ModelsModes mismatch: picked=%q label=%q autoIssue=%d effort=%q ctxFixed=%v allAutoUnavail=%v red=%v",
					pickedSlug, label, autoIss, selectedEffort, ctxFixed, allAutoUnavail, settingsRed)
			}
			if tc.Expected.SessionFullUnavailable != 0 {
				got := modeReasonToVecNum(SessionModeUnavailableReason(v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS, tc.Request.SessionModeLocked, tc.Request.SessionFullAccessUnavailable, tc.Request.SessionModel, modelList, tc.Request.HasModels))
				if got != tc.Expected.SessionFullUnavailable {
					t.Fatalf("session full unavailable: got %d, want %d", got, tc.Expected.SessionFullUnavailable)
				}
			}
			if tc.Expected.SessionAutoUnavailable != 0 {
				got := modeReasonToVecNum(SessionModeUnavailableReason(v1.ApprovalMode_APPROVAL_MODE_AUTO, tc.Request.SessionModeLocked, tc.Request.SessionFullAccessUnavailable, tc.Request.SessionModel, modelList, tc.Request.HasModels))
				if got != tc.Expected.SessionAutoUnavailable {
					t.Fatalf("session auto unavailable: got %d, want %d", got, tc.Expected.SessionAutoUnavailable)
				}
			}
		})
	}

	for _, tc := range doc.PrefsOps {
		t.Run("prefsOps/"+tc.Name, func(t *testing.T) {
			cur := stdToLite[stdv1.ProjectPrefs, *stdv1.ProjectPrefs, v1.ProjectPrefs, *v1.ProjectPrefs](t, tc.Base)
			for _, st := range tc.Steps {
				var op *FullPrefsOp
				switch {
				case st.Op.Pin != nil:
					op = &FullPrefsOp{Kind: PrefsOpPin, Path: st.Op.Pin.Path}
				case st.Op.Hide != nil:
					op = &FullPrefsOp{Kind: PrefsOpHide, Path: st.Op.Hide.Path}
				case st.Op.SetSettings != nil:
					ag := v1.Agent(st.Op.SetSettings.Agent)
					op = &FullPrefsOp{
						Kind: PrefsOpSetSettings,
						Path: st.Op.SetSettings.Path,
						SetSettings: &PrefsSetSettingsOp{
							Path:      st.Op.SetSettings.Path,
							MachineID: st.Op.SetSettings.MachineID,
							Moved:     st.Op.SetSettings.Moved,
							Agent:     &ag,
						},
					}
				}
				nowMs, _ := strconv.ParseInt(st.NowMs, 10, 64)
				cur = ApplyPrefsOp(cur, op, st.OpID, nowMs)
			}
			var gotPinned, gotHidden []string
			for _, p := range cur.Pinned {
				gotPinned = append(gotPinned, p.Path)
			}
			for _, h := range cur.Hidden {
				gotHidden = append(gotHidden, h.Path)
			}
			if !slices.Equal(gotPinned, tc.Expected.Pinned) ||
				!slices.Equal(gotHidden, tc.Expected.Hidden) ||
				!slices.Equal(cur.RecentOps, tc.Expected.RecentOps) {
				t.Fatalf("prefs mismatch: pinned=%v hidden=%v ops=%v", gotPinned, gotHidden, cur.RecentOps)
			}
			saved := SavedSettings(cur, "/b")
			if saved == nil || saved.MachineId != tc.Expected.SettingsMachineID || int32(saved.Agent) != tc.Expected.SettingsAgent || len(saved.Agents) != tc.Expected.SettingsAgentCount {
				t.Fatalf("saved settings mismatch: %+v", saved)
			}
		})
	}

	for _, tc := range doc.Pagination {
		t.Run("pagination/"+tc.Name, func(t *testing.T) {
			w := tc.Request.Snapshot.toWorld(t)
			var held []*Session
			for _, h := range tc.Request.HeldEnded {
				le, _ := strconv.ParseInt(h.ListedEndedMs, 10, 64)
				held = append(held, &Session{ID: h.ID, MachineID: h.MachineID, ListedEndedMs: le})
			}
			var pastIDs []string
			for _, s := range held {
				if PastFetched(w, s) {
					pastIDs = append(pastIDs, s.ID)
				}
			}
			needs := NeedsEndedPage(w, held, tc.Request.Want, tc.Request.Total)
			var rec []ReconcileSessionEntry
			for _, r := range tc.Request.ReconcileSessions {
				le, _ := strconv.ParseInt(r.ListedEndedMs, 10, 64)
				rec = append(rec, ReconcileSessionEntry{ID: r.ID, MachineID: r.MachineID, ListedEndedMs: le})
			}
			rmIDs, ulIDs := ReconcileEndedSessions(w, rec, tc.Request.PageSessionIDs, tc.Request.PushedSessionIDs, tc.Request.KnownMachineIDs, tc.Request.PageNext.toPlace())
			if needs != tc.Expected.NeedsEndedPage ||
				!slices.Equal(pastIDs, tc.Expected.PastFetchedIDs) ||
				!slices.Equal(rmIDs, tc.Expected.RemoveSessionIDs) ||
				!slices.Equal(ulIDs, tc.Expected.UnlistSessionIDs) {
				t.Fatalf("pagination mismatch: needs=%v past=%v rm=%v ul=%v", needs, pastIDs, rmIDs, ulIDs)
			}
		})
	}

	t.Run("pathsDisplay", func(t *testing.T) {
		for _, tc := range doc.PathsDisplay.ShortenPaths {
			if got := ShortenPaths(tc.Text, tc.Cwd, tc.Home); got != tc.Expected {
				t.Fatalf("ShortenPaths(%q): got %q, want %q", tc.Text, got, tc.Expected)
			}
		}
		for _, tc := range doc.PathsDisplay.ShortenPath {
			if got := ShortenPath(tc.Path, tc.Cwd, tc.Home); got != tc.Expected {
				t.Fatalf("ShortenPath(%q): got %q, want %q", tc.Path, got, tc.Expected)
			}
		}
		for _, tc := range doc.PathsDisplay.ApprovalDisplay {
			if got := ApprovalDisplay(tc.Input); got != tc.Expected {
				t.Fatalf("ApprovalDisplay(%q): got %q, want %q", tc.Input, got, tc.Expected)
			}
		}
		for _, tc := range doc.PathsDisplay.ApprovalAlways {
			k, arg := ParseApprovalAlways(tc.Input)
			if int32(k) != tc.Kind || arg != tc.Arg {
				t.Fatalf("ParseApprovalAlways(%q): got (%d, %q), want (%d, %q)", tc.Input, k, arg, tc.Kind, tc.Arg)
			}
		}
	})

	outcomeToVecNum := func(c outcome.Category) int32 {
		switch c {
		case outcome.NotSent:
			return 1
		case outcome.Rejected:
			return 2
		case outcome.Executed:
			return 3
		case outcome.Unknown:
			return 4
		case outcome.Conflict:
			return 5
		default:
			return 0
		}
	}
	for _, tc := range doc.Outcome {
		t.Run("outcome/"+tc.Name, func(t *testing.T) {
			res := stdToLite[stdv1.CommandResult, *stdv1.CommandResult, v1.CommandResult, *v1.CommandResult](t, tc.Result)
			key := bytes.Repeat([]byte{tc.KeyByte}, 32)
			payload := bytes.Repeat([]byte{tc.PayloadByte}, 32)
			res.RequestKeyHash = key
			res.PayloadHash = payload
			got := outcomeToVecNum(outcome.Verify(res, key, payload, tc.OuterOutcome, tc.OuterReason, tc.Prior))
			if got != tc.Expected {
				t.Fatalf("VerifyOutcome: got %d, want %d", got, tc.Expected)
			}
		})
	}
}

func TestShortPathsParity(t *testing.T) {
	cases := [][4]string{
		{"/Users/me/Code/foo/auth/login.go", "/Users/me/Code/foo", "/Users/me", "auth/login.go"},
		{"/private/tmp/ra/proj/a.txt", "/tmp/ra/proj", "/Users/me", "a.txt"},
		{"/tmp/ra/proj/a.txt", "/private/tmp/ra/proj", "/Users/me", "a.txt"},
		{"cd /Users/me/Code/foo && ls", "/Users/me/Code/foo", "/Users/me", "cd . && ls"},
		{"cat /Users/me/.zshrc", "/Users/me/Code/foo", "/Users/me", "cat ~/.zshrc"},
		{"ls /Users/me/Code/foo-bar/x", "/Users/me/Code/foo", "/Users/me", "ls ~/Code/foo-bar/x"},
		{"go build -o=/Users/me/Code/foo/bin/app ./cmd", "/Users/me/Code/foo/", "/Users/me", "go build -o=bin/app ./cmd"},
		{"diff /Users/me/Code/foo/a.go /Users/me/Code/bar/a.go", "/Users/me/Code/foo", "/Users/me", "diff a.go ~/Code/bar/a.go"},
		{"sed -n 1,5p \"/Users/me/Code/foo/main.go\"", "/Users/me/Code/foo", "/Users/me", "sed -n 1,5p \"main.go\""},
		{"Read /Users/me/Code/foo/auth/login.go:58", "/Users/me/Code/foo", "/Users/me", "Read auth/login.go:58"},
		{"cat /etc/hosts /private/etc/hosts", "/Users/me/Code/foo", "/Users/me", "cat /etc/hosts /private/etc/hosts"},
		{"curl https://example.com/Users/me/x", "/Users/me/Code/foo", "/Users/me", "curl https://example.com/Users/me/x"},
		{"grep -rn 'a/b' src/Users/me", "/Users/me/Code/foo", "/Users/me", "grep -rn 'a/b' src/Users/me"},
		{"tail /home/me/app/log.txt", "/srv/app", "/home/me", "tail ~/app/log.txt"},
		{"tail /home/me/app/log.txt", "/srv/app", "", "tail ~/app/log.txt"},
		{"cat /Users/me/notes.md", "", "/Users/me", "cat ~/notes.md"},
		{"/Users/me", "/Users/me/Code/foo", "/Users/me", "~"},
		{"Read /Users/me/Code/foo[archive]/a", "/Users/me/Code/foo", "/Users/me", "Read ~/Code/foo[archive]/a"},
		{"cat \"/Users/me/Code/foo bar/a\"", "/Users/me/Code/foo", "/Users/me", "cat \"~/Code/foo bar/a\""},
		{"cat \"/Users/me/Code/foo/a b.go\"", "/Users/me/Code/foo", "/Users/me", "cat \"a b.go\""},
		{"ls /Users/me/Code/foo.bak/x", "/Users/me/Code/foo", "/Users/me", "ls ~/Code/foo.bak/x"},
		{"(cd /Users/me/Code/foo/sub)", "/Users/me/Code/foo", "/Users/me", "(cd sub)"},
		{"cat /Users/me/Code/foo", "/Users/me/Code/foo", "/Users/me", "cat ."},
		{"echo /Users/meow/x", "/Users/me/Code/foo", "/Users/me", "echo /Users/meow/x"},
		{"Read /Users/me/Code/foo[x]", "/Users/me/Code/foo", "", "Read ~/Code/foo[x]"},
		{"tail /home/me/app:12", "/srv/app", "/home/me", "tail ~/app:12"},
	}
	for _, c := range cases {
		if got := ShortenPaths(c[0], c[1], c[2]); got != c[3] {
			t.Fatalf("ShortenPaths(%q, %q, %q) = %q, want %q", c[0], c[1], c[2], got, c[3])
		}
	}

	if got := PathCrumbs("/Users/me/Code/ai-bot"); !reflect.DeepEqual(got, []string{"~", "Code", "ai-bot"}) {
		t.Fatalf("PathCrumbs home: %v", got)
	}
	if got := PathCrumbs("/srv/www"); !reflect.DeepEqual(got, []string{"/srv", "www"}) {
		t.Fatalf("PathCrumbs /srv/www: %v", got)
	}
	if got := PathCrumbs("/"); !reflect.DeepEqual(got, []string{"/"}) {
		t.Fatalf("PathCrumbs /: %v", got)
	}
	if got := TailPath("/Users/me/Code/ai-bot/remote-agent", 24); got != "…/ai-bot/remote-agent" {
		t.Fatalf("TailPath: got %q", got)
	}
}

func TestPreviewPlainVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "preview-plain.json"))
	if err != nil {
		t.Fatalf("read preview-plain.json: %v", err)
	}
	var cases []struct {
		Name     string `json:"name"`
		Input    string `json:"input"`
		Expected string `json:"expected"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("parse preview-plain.json: %v", err)
	}
	for _, c := range cases {
		if got := Plain(c.Input); got != c.Expected {
			t.Fatalf("Plain(%s): got %q, want %q", c.Name, got, c.Expected)
		}
	}
}

func TestTitleInputCodePointsAndAgents(t *testing.T) {
	if got := TitleInput(strings.Repeat("😀", 61)); got != strings.Repeat("😀", 60) {
		t.Fatalf("emoji clamp failed: len=%d", len(got))
	}
	if got := TitleInput("第一行\r\n第二行"); got != "第一行 第二行" {
		t.Fatalf("newline collapse failed: %q", got)
	}
	if got := NormalizePlanName("claude_max"); got != "Max" {
		t.Fatalf("NormalizePlanName: got %q", got)
	}
	if got := PlanName(v1.Agent_AGENT_ANTIGRAVITY, "claude_max"); got != "claude_max" {
		t.Fatalf("Antigravity PlanName: got %q", got)
	}
	if got := SendModeFor(v1.Agent_AGENT_ANTIGRAVITY, v1.SendMode_SEND_MODE_STEER, true); got != v1.SendMode_SEND_MODE_QUEUE {
		t.Fatalf("Antigravity SendModeFor running: got %v", got)
	}
	vers := MachineVersions(&v1.MachineMeta{CodexVersion: "1.2.3", CodexVerified: true})
	if len(vers) != 1 || vers[0].Version != "1.2.3" {
		t.Fatalf("MachineVersions legacy synthesis: %+v", vers)
	}
}
