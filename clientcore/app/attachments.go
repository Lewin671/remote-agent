package app

import (
	"slices"
	"strconv"
	"strings"

	"github.com/Lewin671/remote-agent/clientcore/domain/attachments"
	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/sharedfiles"
	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/sync"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

type attUploadPhase uint8

const (
	attPhaseIdle attUploadPhase = iota
	attPhaseQueuedPrepare
	attPhasePreparing
	attPhaseQueuedUpload
	attPhaseBegin
	attPhaseStatusProbe
	attPhaseReadingChunk
	attPhasePuttingChunk
	attPhaseCompleting
	attPhasePollDelay
	attPhaseWaitingReconnect
	attPhaseWaitingOnline
)

const (
	attOwnerDraft  uint8 = 0
	attOwnerResend uint8 = 1
)

type attPrepareRef struct {
	ownerKind  uint8
	ownerKey   string
	itemID     string
	generation uint64
}

type attActiveUploadMeta struct {
	ownerKind  uint8
	ownerKey   string
	itemID     string
	generation uint64
	step       attUploadPhase
	machineID  string
	transferID string
}

type attTransferRef struct {
	machineID  string
	transferID string
}

type attWriteOpKind uint8

const (
	attWriteKindDraft attWriteOpKind = iota + 1
	attWriteKindPreSendOutbox
	attWriteKindResendOutbox
	attWriteKindFailedResendOutbox
)

type attWriteOpMeta struct {
	kind      attWriteOpKind
	draftKey  string
	sessionID string
	requestID string
	outboxKey string
	writeSeq  uint64
}

type attSlotReservation struct {
	images int32
	files  int32
}

type draftLoadState uint8

const (
	draftLoadUnloaded draftLoadState = iota
	draftLoadLoading
	draftLoadLoaded
	draftLoadFailed
)

type pendingDraftWriteBarrier struct {
	pendingLoad bool
	writeSeq    uint64
	outboxKey   string
	sessionID   string
	requestID   string
}

type pendingLocalReleaseBarrier struct {
	writeSeq uint64
	refs     []string
}

type attPendingSend struct {
	sessionID string
	machineID string
	requestID string
	text      string
	mode      v1.SendMode
	original  bool
	itemIDs   []string
	imageIDs  []string
	fileIDs   []string
	items     []*attItem
	writeOpID uint64
	outboxKey string
}

type attItem struct {
	id          string
	kind        corev1.AttachmentItemKind
	isVideo     bool
	fileName    string
	displayName string
	mimeType    string
	sizeBytes   int64
	sha256      []byte

	imageFormat       v1.ImageFormat
	width             int32
	height            int32
	preparedOriginal  bool
	hasOriginalSource bool
	hasPrepared       bool

	hasDuration bool
	durationMs  int64

	sourceRef   string
	preparedRef string
	thumbRef    string

	state             corev1.AttachmentItemState
	phase             attUploadPhase
	generation        uint64
	transferID        string
	targetMachineID   string
	uploadedMachineID string
	remoteID          string
	expiresMs         int64

	chunkBytes              int32
	persistedBytes          int64
	progress                float64
	progressPct             int32
	beginDone               bool
	stepRetries             int
	completePolls           int
	restartedOnce           bool
	retriedReconnect        bool
	reconnectDeadlineMonoMs int64
	reconnectNotBeforeMs    int64
	pendingChunkOff         int64
	pendingChunkLen         int64
	statusProbeForComplete  bool
	pausedByStop            bool

	activeOpID  uint64
	activeReqID string

	remoteOnly bool
	problem    *corev1.ImageProblemView
	fileError  *corev1.Text
}

type attDraft struct {
	scope           *corev1.DraftScope
	key             string
	targetMachineID string
	text            string
	textRev         uint64
	original        bool
	items           []*attItem
	reservations    map[string]attSlotReservation
	notice          *corev1.Text
	storageError    *corev1.Text

	loadState            draftLoadState
	activated            bool
	hasLocalTextEdit     bool
	activeLoadOpID       uint64
	loadStartTextRev     uint64
	activeWriteOpID      uint64
	writeSeq             uint64
	dirtyWrite           bool
	pendingOutboxDeletes []pendingDraftWriteBarrier
	pendingLocalReleases []pendingLocalReleaseBarrier

	sending     bool
	pendingSend *attPendingSend

	viewerOpen          bool
	viewerIndex         int32
	expiryTimerTargetMs int64
}

type attOutboxRecord struct {
	sessionID         string
	requestID         string
	machineID         string
	text              string
	mode              v1.SendMode
	original          bool
	imageIDs          []string
	fileIDs           []string
	items             []*attItem
	createdMs         int64
	restoredFromCrash bool
	storageKey        string
	persisted         bool
}

type attResendJob struct {
	sessionID       string
	machineID       string
	sourceRequestID string
	newRequestID    string
	text            string
	mode            v1.SendMode
	original        bool
	items           []*attItem
	writeOpID       uint64
	outboxKey       string
	aborted         bool
}

type attNewSessionStart struct {
	projectKey string
	requestID  string
	machineID  string
	prompt     string
	itemIDs    []string
	transfers  []attTransferRef
}

type attManager struct {
	configured               bool
	syncStopped              bool
	persistDrafts            bool
	persistOutbox            bool
	hasPersistedPreparedImgs bool
	maxChunkBytes            int32

	nextOpID       uint64
	nextItemSeq    uint64
	activeListOpID uint64

	drafts            map[string]*attDraft
	forgottenSessions map[string]bool

	outbox           map[string]*attOutboxRecord
	outboxBySession  map[string][]string
	resendJobs       map[string]*attResendJob
	newSessionStarts map[string]*attNewSessionStart

	activePrepareOpID uint64
	activePrepareRef  attPrepareRef
	prepareQueue      []attPrepareRef

	activeFileUpload *attPrepareRef
	fileUploadQueue  []attPrepareRef

	reqToUpload     map[string]attActiveUploadMeta
	readOpToMeta    map[uint64]attPrepareRef
	prepareOpToMeta map[uint64]attPrepareRef
	draftWriteOps   map[uint64]attWriteOpMeta

	protectedTransfers map[attTransferRef]map[string]bool

	dirtyDraftScreen bool
}

func newAttManager() *attManager {
	return &attManager{
		maxChunkBytes:      attachments.DefaultUploadChunkBytes,
		drafts:             make(map[string]*attDraft),
		forgottenSessions:  make(map[string]bool),
		outbox:             make(map[string]*attOutboxRecord),
		outboxBySession:    make(map[string][]string),
		resendJobs:         make(map[string]*attResendJob),
		newSessionStarts:   make(map[string]*attNewSessionStart),
		reqToUpload:        make(map[string]attActiveUploadMeta),
		readOpToMeta:       make(map[uint64]attPrepareRef),
		prepareOpToMeta:    make(map[uint64]attPrepareRef),
		draftWriteOps:      make(map[uint64]attWriteOpMeta),
		protectedTransfers: make(map[attTransferRef]map[string]bool),
	}
}

func (m *Model) ensureAttachments() *attManager {
	if m.attachments == nil {
		m.attachments = newAttManager()
	}
	return m.attachments
}

func (mgr *attManager) allocOpID() uint64 {
	mgr.nextOpID++
	return mgr.nextOpID
}

func (mgr *attManager) allocItemID() string {
	mgr.nextItemSeq++
	return "att-" + strconv.FormatUint(mgr.nextItemSeq, 10)
}

func cloneDraftScope(s *corev1.DraftScope) *corev1.DraftScope {
	if s == nil {
		return nil
	}
	return &corev1.DraftScope{Kind: s.Kind, Id: s.Id}
}

func (c *Core) ensureDraftState(scope *corev1.DraftScope) *attDraft {
	key := attachments.ScopeStorageKey(scope)
	if key == "" {
		return nil
	}
	mgr := c.m.ensureAttachments()
	if d := mgr.drafts[key]; d != nil {
		return d
	}
	loadSt := draftLoadUnloaded
	if mgr.configured && !mgr.persistDrafts {
		loadSt = draftLoadLoaded
	}
	d := &attDraft{
		scope:        cloneDraftScope(scope),
		key:          key,
		reservations: make(map[string]attSlotReservation),
		loadState:    loadSt,
	}
	if scope.Kind == corev1.DraftScopeKind_DRAFT_SCOPE_KIND_SESSION {
		if se := c.m.sync.SessionEntry(scope.Id); se != nil && se.MachineID != "" {
			d.targetMachineID = se.MachineID
		}
	} else if scope.Kind == corev1.DraftScopeKind_DRAFT_SCOPE_KIND_NEW_SESSION {
		if nd := c.m.state.NewSessions[scope.Id]; nd != nil {
			d.targetMachineID = nd.Config.MachineID
			if nd.Prompt != "" {
				d.text = nd.Prompt
			}
		}
	}
	mgr.drafts[key] = d
	return d
}

func (c *Core) resolveDraftMachineID(d *attDraft) string {
	if d == nil || d.scope == nil {
		return ""
	}
	if d.scope.Kind == corev1.DraftScopeKind_DRAFT_SCOPE_KIND_SESSION {
		if se := c.m.sync.SessionEntry(d.scope.Id); se != nil && se.MachineID != "" {
			d.targetMachineID = se.MachineID
			return se.MachineID
		}
		return d.targetMachineID
	}
	if d.scope.Kind == corev1.DraftScopeKind_DRAFT_SCOPE_KIND_NEW_SESSION {
		if nd := c.m.state.NewSessions[d.scope.Id]; nd != nil && nd.Config.MachineID != "" {
			return nd.Config.MachineID
		}
	}
	return d.targetMachineID
}

func (c *Core) resolveAttachmentCaps(machineID string, putBack bool) (attachments.ResolvedCaps, bool) {
	lm := c.m.findMachine(machineID)
	st := c.m.state
	computerOnline := lists.MachineReachable(lm) || (st.DisplayGrace && lm != nil && lm.Online)
	unlinked := !st.DisplayGrace && (lists.WorldUnlinked(&st.World) || lists.MachineUnlinked(lm, &st.World))
	protoMismatch := lm != nil && lists.ProtocolMismatchDirection(lm, st.World.ClientVersion) != 0
	onlineReady := (st.World.Connection == lists.ConnConnected || st.DisplayGrace) &&
		computerOnline && !unlinked && !protoMismatch && !st.World.AccountControlLost && !st.Removed
	if putBack {
		return attachments.ResolveCaps(lm, machineID, true, true), onlineReady
	}
	return attachments.ResolveCaps(lm, machineID, computerOnline, onlineReady), onlineReady
}

func (c *Core) markDraftDirty(ctx *sync.StepContext, scope *corev1.DraftScope) {
	mgr := c.m.ensureAttachments()
	mgr.dirtyDraftScreen = true
	if scope == nil {
		c.m.ensureSessionStore().pendingDirty = true
		if ctx != nil {
			ctx.Dirty.Add(false, false, true)
		}
		return
	}
	switch scope.Kind {
	case corev1.DraftScopeKind_DRAFT_SCOPE_KIND_SESSION:
		c.m.ensureSessionStore().pendingDirty = true
	case corev1.DraftScopeKind_DRAFT_SCOPE_KIND_NEW_SESSION:
		if ctx != nil {
			ctx.Dirty.Add(false, false, true)
		}
	}
}

func (c *Core) appendAttachmentDraftRenderTarget(effects []*corev1.Effect) []*corev1.Effect {
	if c.m.attachments == nil || !c.m.attachments.dirtyDraftScreen {
		return effects
	}
	c.m.attachments.dirtyDraftScreen = false
	for _, eff := range effects {
		if eff != nil && eff.GetRender() != nil {
			r := eff.GetRender()
			if !slices.Contains(r.Screens, corev1.ScreenTarget_SCREEN_TARGET_ATTACHMENT_DRAFT) {
				r.Screens = append(r.Screens, corev1.ScreenTarget_SCREEN_TARGET_ATTACHMENT_DRAFT)
			}
			return effects
		}
	}
	return append(effects, &corev1.Effect{
		Effect: &corev1.Effect_Render{
			Render: &corev1.RenderEffect{
				Screens: []corev1.ScreenTarget{corev1.ScreenTarget_SCREEN_TARGET_ATTACHMENT_DRAFT},
			},
		},
	})
}

func (c *Core) dispatchAttachmentEvent(ctx *sync.StepContext, ev *corev1.Event) ([]*corev1.Effect, bool) {
	switch k := ev.Event.(type) {
	case *corev1.Event_AttachmentConfig:
		return c.handleAttachmentConfig(ctx, k.AttachmentConfig), true
	case *corev1.Event_DraftStorageListed:
		return c.handleDraftStorageListed(ctx, k.DraftStorageListed), true
	case *corev1.Event_DraftStorageReadResult:
		return c.handleDraftStorageReadResult(ctx, k.DraftStorageReadResult), true
	case *corev1.Event_DraftStorageWriteResult:
		return c.handleDraftStorageWriteResult(ctx, k.DraftStorageWriteResult), true
	case *corev1.Event_DraftOpen:
		return c.handleDraftOpen(ctx, k.DraftOpen), true
	case *corev1.Event_DraftSetText:
		return c.handleDraftSetText(ctx, k.DraftSetText), true
	case *corev1.Event_DraftSetTargetMachine:
		return c.handleDraftSetTargetMachine(ctx, k.DraftSetTargetMachine), true
	case *corev1.Event_DraftReserveSlots:
		return c.handleDraftReserveSlots(ctx, k.DraftReserveSlots), true
	case *corev1.Event_DraftReleaseSlots:
		return c.handleDraftReleaseSlots(ctx, k.DraftReleaseSlots), true
	case *corev1.Event_DraftAddItems:
		return c.handleDraftAddItems(ctx, k.DraftAddItems), true
	case *corev1.Event_DraftRemoveItem:
		return c.handleDraftRemoveItem(ctx, k.DraftRemoveItem), true
	case *corev1.Event_DraftRetryItem:
		return c.handleDraftRetryItem(ctx, k.DraftRetryItem), true
	case *corev1.Event_DraftSetOriginal:
		return c.handleDraftSetOriginal(ctx, k.DraftSetOriginal), true
	case *corev1.Event_DraftClear:
		return c.handleDraftClear(ctx, k.DraftClear), true
	case *corev1.Event_DraftForgetSession:
		return c.handleDraftForgetSession(ctx, k.DraftForgetSession), true
	case *corev1.Event_DraftClearAll:
		return c.handleDraftClearAll(ctx), true
	case *corev1.Event_DraftDismissNotice:
		return c.handleDraftDismissNotice(ctx, k.DraftDismissNotice), true
	case *corev1.Event_AttachmentPauseUpload:
		return c.handleAttachmentPauseUpload(ctx, k.AttachmentPauseUpload), true
	case *corev1.Event_AttachmentResumeUpload:
		return c.handleAttachmentResumeUpload(ctx, k.AttachmentResumeUpload), true
	case *corev1.Event_AttachmentPrepareImageResult:
		return c.handleAttachmentPrepareImageResult(ctx, k.AttachmentPrepareImageResult), true
	case *corev1.Event_AttachmentHashFileResult:
		return c.handleAttachmentHashFileResult(ctx, k.AttachmentHashFileResult), true
	case *corev1.Event_AttachmentReadChunkResult:
		return c.handleAttachmentReadChunkResult(ctx, k.AttachmentReadChunkResult), true
	case *corev1.Event_DraftSendSession:
		return c.handleDraftSendSession(ctx, k.DraftSendSession), true
	case *corev1.Event_DraftPutBackInput:
		return c.handleDraftPutBackInput(ctx, k.DraftPutBackInput), true
	case *corev1.Event_DraftImageViewerOpen:
		return c.handleDraftImageViewerOpen(ctx, k.DraftImageViewerOpen), true
	case *corev1.Event_DraftImageViewerNavigate:
		return c.handleDraftImageViewerNavigate(ctx, k.DraftImageViewerNavigate), true
	case *corev1.Event_DraftImageViewerClose:
		return c.handleDraftImageViewerClose(ctx, k.DraftImageViewerClose), true
	}
	return nil, false
}

func (c *Core) handleAttachmentConfig(ctx *sync.StepContext, ev *corev1.AttachmentConfigEvent) []*corev1.Effect {
	if ev == nil {
		return nil
	}
	mgr := c.m.ensureAttachments()
	mgr.configured = true
	mgr.persistDrafts = ev.PersistDrafts
	mgr.persistOutbox = ev.PersistOutbox
	mgr.hasPersistedPreparedImgs = ev.HasPersistedPreparedImages
	if ev.MaxChunkBytes > 0 && ev.MaxChunkBytes < attachments.DefaultUploadChunkBytes {
		mgr.maxChunkBytes = ev.MaxChunkBytes
	} else {
		mgr.maxChunkBytes = attachments.DefaultUploadChunkBytes
	}
	var effects []*corev1.Effect
	// Reconcile any drafts touched before AttachmentConfigEvent arrived.
	if len(mgr.drafts) > 0 {
		keys := make([]string, 0, len(mgr.drafts))
		for k := range mgr.drafts {
			keys = append(keys, k)
		}
		sortutil.Strings(keys)
		for _, k := range keys {
			d := mgr.drafts[k]
			if d == nil || d.loadState != draftLoadUnloaded {
				continue
			}
			if !mgr.persistDrafts {
				d.loadState = draftLoadLoaded
				d.hasLocalTextEdit = false
				if d.activated {
					c.startPendingDraftUploads(ctx, d, &effects)
				}
			} else if d.activated && !ev.ScanStorage {
				c.startDraftLoad(d, &effects)
			}
			c.markDraftDirty(ctx, d.scope)
		}
	}
	if ev.ScanStorage {
		opID := mgr.allocOpID()
		mgr.activeListOpID = opID
		effects = append(effects, &corev1.Effect{
			Effect: &corev1.Effect_DraftStorageList{
				DraftStorageList: &corev1.DraftStorageListEffect{OpId: opID},
			},
		})
	}
	c.pumpAttachmentQueues(ctx, &effects)
	return effects
}

func (c *Core) handleDraftStorageListed(ctx *sync.StepContext, ev *corev1.DraftStorageListedEvent) []*corev1.Effect {
	if ev == nil || c.m.attachments == nil {
		return nil
	}
	mgr := c.m.attachments
	if ev.OpId == 0 || mgr.activeListOpID == 0 || ev.OpId != mgr.activeListOpID {
		return nil
	}
	mgr.activeListOpID = 0
	if ev.ErrorKind != corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_UNSPECIFIED {
		return nil
	}

	nowMs := c.m.sync.NowMs()
	var effects []*corev1.Effect
	for _, entry := range ev.Entries {
		if entry == nil || entry.Key == "" {
			continue
		}
		if entry.Corrupt {
			effects = append(effects, makeDraftStorageDeleteEffect(entry.Bucket, entry.Key))
			continue
		}
		switch entry.Bucket {
		case corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_OUTBOX:
			rec, ok := attachments.DecodePersistedOutbox(entry.Data)
			if !ok {
				effects = append(effects, makeDraftStorageDeleteEffect(entry.Bucket, entry.Key))
				continue
			}
			if mgr.forgottenSessions[rec.SessionId] {
				effects = append(effects, makeDraftStorageDeleteEffect(entry.Bucket, entry.Key))
				continue
			}
			c.restoreOutboxRecordFromPersisted(rec, entry.Key)
			c.m.ensureSessionStore().pendingDirty = true
		case corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_DRAFT:
			rec, ok := attachments.DecodePersistedDraft(entry.Data)
			if !ok {
				effects = append(effects, makeDraftStorageDeleteEffect(entry.Bucket, entry.Key))
				continue
			}
			if rec.Scope.Kind == corev1.DraftScopeKind_DRAFT_SCOPE_KIND_SESSION && mgr.forgottenSessions[rec.Scope.Id] {
				effects = append(effects, makeDraftStorageDeleteEffect(entry.Bucket, entry.Key))
				continue
			}
			if nowMs > 0 && rec.UpdatedMs > 0 && nowMs-rec.UpdatedMs >= attachments.DraftIdleExpiryMs {
				// 7-day idle draft expiry (docs/session-images.md).
				effects = append(effects, makeDraftStorageDeleteEffect(entry.Bucket, entry.Key))
				continue
			}
			d := c.ensureDraftState(rec.Scope)
			if d != nil && d.loadState != draftLoadLoaded {
				// Only hydrate on startup scan; do NOT eagerly prepare/upload unopened sessions!
				hadPreConfigEdit := d.hasLocalTextEdit
				shouldWrite := hadPreConfigEdit || d.textRev != d.loadStartTextRev || hasPendingLoadOutboxBarriers(d)
				c.hydrateDraftFromPersisted(ctx, d, rec, d.activated, &effects)
				d.activeLoadOpID = 0
				d.storageError = nil
				d.hasLocalTextEdit = false
				if shouldWrite {
					targetSeq := c.enqueueDraftWrite(d, &effects)
					c.bindPendingLoadOutboxBarriers(d, targetSeq, &effects)
				}
				c.markDraftDirty(ctx, d.scope)
			}
		}
	}
	// Garbage-collect orphaned local files only after a clean storage scan (docs/session-images.md:55).
	if len(ev.LocalRefs) > 0 {
		c.emitUnreferencedLocalReleases(ev.LocalRefs, &effects)
	}
	c.pumpAttachmentQueues(ctx, &effects)
	return effects
}

func (c *Core) handleDraftOpen(ctx *sync.StepContext, act *corev1.DraftOpenAction) []*corev1.Effect {
	if act == nil || attachments.ScopeStorageKey(act.Scope) == "" {
		return nil
	}
	mgr := c.m.ensureAttachments()
	if act.Scope.Kind == corev1.DraftScopeKind_DRAFT_SCOPE_KIND_SESSION && mgr.forgottenSessions[act.Scope.Id] {
		return nil
	}
	d := c.ensureDraftState(act.Scope)
	d.activated = true
	var effects []*corev1.Effect
	if act.MachineId != "" {
		c.retargetDraftLocked(ctx, d, act.MachineId, &effects)
	}
	if mgr.persistDrafts && (d.loadState == draftLoadUnloaded || d.loadState == draftLoadFailed) {
		c.startDraftLoad(d, &effects)
	} else if d.loadState == draftLoadLoaded {
		c.refreshDraftExpiredItems(ctx, d, &effects)
		c.startPendingDraftUploads(ctx, d, &effects)
	}
	c.pumpAttachmentQueues(ctx, &effects)
	c.markDraftDirty(ctx, d.scope)
	return effects
}

func (c *Core) startDraftLoad(d *attDraft, effects *[]*corev1.Effect) {
	if d == nil {
		return
	}
	mgr := c.m.ensureAttachments()
	opID := mgr.allocOpID()
	d.loadState = draftLoadLoading
	d.activeLoadOpID = opID
	d.loadStartTextRev = d.textRev
	d.storageError = nil
	*effects = append(*effects, &corev1.Effect{
		Effect: &corev1.Effect_DraftStorageRead{
			DraftStorageRead: &corev1.DraftStorageReadEffect{
				OpId:   opID,
				Bucket: corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_DRAFT,
				Key:    d.key,
			},
		},
	})
}

func (c *Core) handleDraftStorageReadResult(ctx *sync.StepContext, ev *corev1.DraftStorageReadResultEvent) []*corev1.Effect {
	if ev == nil || c.m.attachments == nil || ev.OpId == 0 {
		return nil
	}
	mgr := c.m.attachments
	d := mgr.drafts[ev.Key]
	if d == nil {
		for _, cand := range mgr.drafts {
			if cand != nil && cand.activeLoadOpID == ev.OpId {
				d = cand
				break
			}
		}
	}
	if d == nil || d.activeLoadOpID == 0 || d.activeLoadOpID != ev.OpId || d.loadState == draftLoadLoaded {
		return nil
	}
	d.activeLoadOpID = 0
	var effects []*corev1.Effect

	if ev.ErrorKind != corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_UNSPECIFIED {
		if ev.ErrorKind == corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_CORRUPT_STORE {
			d.loadState = draftLoadLoaded
			effects = append(effects, makeDraftStorageDeleteEffect(corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_DRAFT, d.key))
			if d.textRev != d.loadStartTextRev || hasPendingLoadOutboxBarriers(d) {
				targetSeq := c.enqueueDraftWrite(d, &effects)
				c.bindPendingLoadOutboxBarriers(d, targetSeq, &effects)
			}
			c.markDraftDirty(ctx, d.scope)
			return effects
		}
		d.loadState = draftLoadFailed
		d.storageError = attachments.ClassifyStorageErrorText(ev.ErrorKind, true)
		c.markDraftDirty(ctx, d.scope)
		return effects
	}

	d.storageError = nil
	nowMs := c.m.sync.NowMs()
	if ev.Found && len(ev.Data) > 0 {
		rec, ok := attachments.DecodePersistedDraft(ev.Data)
		if !ok {
			// Legacy or corrupt payload: discard without migration.
			d.loadState = draftLoadLoaded
			effects = append(effects, makeDraftStorageDeleteEffect(corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_DRAFT, d.key))
		} else if nowMs > 0 && rec.UpdatedMs > 0 && nowMs-rec.UpdatedMs >= attachments.DraftIdleExpiryMs {
			// 7-day idle draft expiry.
			d.loadState = draftLoadLoaded
			effects = append(effects, makeDraftStorageDeleteEffect(corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_DRAFT, d.key))
			var staleRefs []string
			for _, pi := range rec.Items {
				if pi != nil {
					staleRefs = append(staleRefs, pi.SourceRef, pi.PreparedRef, pi.ThumbRef)
				}
			}
			c.emitUnreferencedLocalReleases(staleRefs, &effects)
		} else {
			c.hydrateDraftFromPersisted(ctx, d, rec, d.activated, &effects)
		}
	} else {
		d.loadState = draftLoadLoaded
	}

	if d.loadState == draftLoadLoaded && (d.textRev != d.loadStartTextRev || d.hasLocalTextEdit || hasPendingLoadOutboxBarriers(d)) {
		d.hasLocalTextEdit = false
		targetSeq := c.enqueueDraftWrite(d, &effects)
		c.bindPendingLoadOutboxBarriers(d, targetSeq, &effects)
	}
	c.pumpAttachmentQueues(ctx, &effects)
	c.markDraftDirty(ctx, d.scope)
	return effects
}

func hasPendingLoadOutboxBarriers(d *attDraft) bool {
	if d == nil {
		return false
	}
	for _, b := range d.pendingOutboxDeletes {
		if b.pendingLoad {
			return true
		}
	}
	return false
}

func (c *Core) bindPendingLoadOutboxBarriers(d *attDraft, targetSeq uint64, effects *[]*corev1.Effect) {
	if d == nil || !hasPendingLoadOutboxBarriers(d) {
		return
	}
	for i := range d.pendingOutboxDeletes {
		if d.pendingOutboxDeletes[i].pendingLoad {
			d.pendingOutboxDeletes[i].pendingLoad = false
			d.pendingOutboxDeletes[i].writeSeq = targetSeq
		}
	}
	if d.activeWriteOpID == 0 {
		// Draft write resolved synchronously (e.g. empty draft delete); flush bound barriers now.
		c.flushDraftWriteBarriers(d, targetSeq, effects)
	}
}

func (c *Core) hydrateDraftFromPersisted(ctx *sync.StepContext, d *attDraft, rec *corev1.PersistedDraftRecord, startUploads bool, effects *[]*corev1.Effect) {
	if d == nil || rec == nil {
		return
	}
	userTypedDuringLoad := d.textRev != d.loadStartTextRev || d.hasLocalTextEdit
	if !userTypedDuringLoad {
		if d.text == "" {
			d.text = rec.Text
		} else if rec.Text != "" {
			d.text = attachments.MergePutBackText(rec.Text, d.text)
		}
		c.syncNewSessionPromptFromDraft(d)
	}
	if len(d.items) == 0 {
		d.original = rec.Original
		for _, pi := range rec.Items {
			if pi == nil || pi.ItemId == "" {
				continue
			}
			it := attItemFromPersisted(pi)
			// Per docs/session-files.md §历史事件与网页: restored items NEVER trust old remote IDs
			// as immediately ready; clear remote ID/expiry and re-validate/re-upload before sending.
			it.remoteID = ""
			it.uploadedMachineID = ""
			it.expiresMs = 0
			it.state = corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_PREPARING
			it.phase = attPhaseIdle
			it.generation = 1
			if !it.hasLocalCopy() {
				it.remoteOnly = true
				c.markItemMissingLocal(it)
			}
			d.items = append(d.items, it)
		}
	}
	d.loadState = draftLoadLoaded
	c.syncNewSessionAttachmentCount(d)
	if startUploads {
		c.startPendingDraftUploads(ctx, d, effects)
	}
}

func (c *Core) handleDraftSetText(ctx *sync.StepContext, act *corev1.DraftSetTextAction) []*corev1.Effect {
	if act == nil || attachments.ScopeStorageKey(act.Scope) == "" {
		return nil
	}
	mgr := c.m.ensureAttachments()
	if act.Scope.Kind == corev1.DraftScopeKind_DRAFT_SCOPE_KIND_SESSION && mgr.forgottenSessions[act.Scope.Id] {
		return nil
	}
	d := c.ensureDraftState(act.Scope)
	d.activated = true
	var effects []*corev1.Effect
	if d.text == act.Text {
		if mgr.persistDrafts && d.loadState == draftLoadFailed {
			c.startDraftLoad(d, &effects)
		}
		return effects
	}
	d.text = act.Text
	d.textRev++
	c.syncNewSessionPromptFromDraft(d)
	if !mgr.configured {
		// Hold edit in memory until AttachmentConfigEvent arrives; never assume loaded or overwrite disk!
		d.hasLocalTextEdit = true
	} else if mgr.persistDrafts {
		switch d.loadState {
		case draftLoadUnloaded, draftLoadFailed:
			// Never overwrite an unloaded disk draft; start load and keep user edit via hasLocalTextEdit/textRev.
			d.hasLocalTextEdit = true
			c.startDraftLoad(d, &effects)
		case draftLoadLoading:
			d.hasLocalTextEdit = true
			// In-flight load will see textRev != loadStartTextRev and flush merged draft.
		case draftLoadLoaded:
			c.enqueueDraftWrite(d, &effects)
		}
	}
	c.markDraftDirty(ctx, d.scope)
	return effects
}

func (c *Core) syncNewSessionPromptFromDraft(d *attDraft) {
	if d == nil || d.scope == nil || d.scope.Kind != corev1.DraftScopeKind_DRAFT_SCOPE_KIND_NEW_SESSION {
		return
	}
	if nd := c.m.ensureDraft(d.scope.Id); nd != nil {
		nd.Prompt = d.text
	}
}

func (c *Core) syncNewSessionAttachmentCount(d *attDraft) {
	if d == nil || d.scope == nil || d.scope.Kind != corev1.DraftScopeKind_DRAFT_SCOPE_KIND_NEW_SESSION {
		return
	}
	if nd := c.m.ensureDraft(d.scope.Id); nd != nil {
		nd.AttachmentCount = int32(len(d.items))
		if len(d.items) == 0 {
			nd.AttachmentsMoved = false
		}
	}
}

func (c *Core) handleDraftSetTargetMachine(ctx *sync.StepContext, act *corev1.DraftSetTargetMachineAction) []*corev1.Effect {
	if act == nil || attachments.ScopeStorageKey(act.Scope) == "" {
		return nil
	}
	d := c.ensureDraftState(act.Scope)
	var effects []*corev1.Effect
	c.retargetDraftLocked(ctx, d, act.MachineId, &effects)
	c.pumpAttachmentQueues(ctx, &effects)
	c.markDraftDirty(ctx, d.scope)
	return effects
}

func (c *Core) retargetDraftLocked(ctx *sync.StepContext, d *attDraft, machineID string, effects *[]*corev1.Effect) bool {
	if d == nil || machineID == "" {
		return false
	}
	changed := d.targetMachineID != "" && d.targetMachineID != machineID
	d.targetMachineID = machineID
	if len(d.items) == 0 {
		return false
	}
	anyInvalidated := false
	for _, it := range d.items {
		if it == nil {
			continue
		}
		itemChanged := (it.uploadedMachineID != "" && it.uploadedMachineID != machineID) ||
			(it.targetMachineID != "" && it.targetMachineID != machineID) ||
			(changed && (it.state == corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_READY ||
				it.state == corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_UPLOADING ||
				it.state == corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_PAUSED ||
				it.state == corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_WAITING_RECONNECT ||
				it.state == corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_PREPARING))
		if itemChanged {
			anyInvalidated = true
			c.invalidateItem(ctx, attOwnerDraft, d.key, it, true, effects)
		}
	}
	if anyInvalidated {
		if d.scope != nil && d.scope.Kind == corev1.DraftScopeKind_DRAFT_SCOPE_KIND_NEW_SESSION {
			if nd := c.m.ensureDraft(d.scope.Id); nd != nil && len(d.items) > 0 {
				nd.AttachmentsMoved = true
			}
		}
		c.scheduleDraftExpiryTimer(ctx, d)
		if d.loadState == draftLoadLoaded {
			c.enqueueDraftWrite(d, effects)
		}
	}
	c.startPendingDraftUploads(ctx, d, effects)
	return anyInvalidated
}

func (c *Core) handleDraftReserveSlots(ctx *sync.StepContext, act *corev1.DraftReserveSlotsAction) []*corev1.Effect {
	if act == nil || act.ReservationId == "" || attachments.ScopeStorageKey(act.Scope) == "" {
		return nil
	}
	d := c.ensureDraftState(act.Scope)
	if d.loadState == draftLoadLoading {
		return nil
	}
	imgs := act.ImageSlots
	if imgs < 0 {
		imgs = 0
	}
	files := act.FileSlots
	if files < 0 {
		files = 0
	}
	if imgs == 0 && files == 0 {
		delete(d.reservations, act.ReservationId)
	} else {
		d.reservations[act.ReservationId] = attSlotReservation{images: imgs, files: files}
	}
	c.markDraftDirty(ctx, d.scope)
	return nil
}

func (c *Core) handleDraftReleaseSlots(ctx *sync.StepContext, act *corev1.DraftReleaseSlotsAction) []*corev1.Effect {
	if act == nil || act.ReservationId == "" || attachments.ScopeStorageKey(act.Scope) == "" {
		return nil
	}
	if c.m.attachments == nil {
		return nil
	}
	d := c.m.attachments.drafts[attachments.ScopeStorageKey(act.Scope)]
	if d == nil {
		return nil
	}
	if _, ok := d.reservations[act.ReservationId]; ok {
		delete(d.reservations, act.ReservationId)
		c.markDraftDirty(ctx, d.scope)
	}
	return nil
}

func (d *attDraft) reservedCounts() (imgs, files int32) {
	if d == nil {
		return 0, 0
	}
	for _, r := range d.reservations {
		imgs += r.images
		files += r.files
	}
	return imgs, files
}

func (d *attDraft) itemCounts() (imgs, files int32) {
	if d == nil {
		return 0, 0
	}
	for _, it := range d.items {
		if it == nil {
			continue
		}
		if it.kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE {
			imgs++
		} else {
			files++
		}
	}
	return imgs, files
}

func (c *Core) handleDraftAddItems(ctx *sync.StepContext, act *corev1.DraftAddItemsAction) []*corev1.Effect {
	if act == nil || attachments.ScopeStorageKey(act.Scope) == "" {
		return nil
	}
	mgr := c.m.ensureAttachments()
	if act.Scope.Kind == corev1.DraftScopeKind_DRAFT_SCOPE_KIND_SESSION && mgr.forgottenSessions[act.Scope.Id] {
		return nil
	}
	d := c.ensureDraftState(act.Scope)
	d.activated = true
	if act.ReservationId != "" {
		delete(d.reservations, act.ReservationId)
	}
	if !mgr.configured || d.loadState == draftLoadLoading {
		var rejectedRefs []string
		for _, picked := range act.Items {
			if picked == nil {
				continue
			}
			if picked.SourceRef != "" {
				rejectedRefs = append(rejectedRefs, picked.SourceRef)
			}
			if picked.ThumbRef != "" {
				rejectedRefs = append(rejectedRefs, picked.ThumbRef)
			}
		}
		var effects []*corev1.Effect
		c.emitUnreferencedLocalReleases(rejectedRefs, &effects)
		d.notice = attachments.RestoreWaitReason()
		c.markDraftDirty(ctx, d.scope)
		return effects
	}
	if len(act.Items) == 0 {
		c.markDraftDirty(ctx, d.scope)
		return nil
	}

	machineID := c.resolveDraftMachineID(d)
	caps, _ := c.resolveAttachmentCaps(machineID, false)
	curImgs, curFiles := d.itemCounts()
	resImgs, resFiles := d.reservedCounts()

	cands := make([]*corev1.AttachmentCandidateInput, len(act.Items))
	for i, picked := range act.Items {
		if picked != nil && picked.Candidate != nil {
			cands[i] = picked.Candidate
		} else {
			cands[i] = &corev1.AttachmentCandidateInput{}
		}
	}
	decisions, notice := attachments.EvaluateCandidates(caps, curImgs+resImgs, curFiles+resFiles, cands)
	d.notice = notice

	var effects []*corev1.Effect
	var rejectedRefs []string
	added := false
	for i, dec := range decisions {
		picked := act.Items[i]
		if dec == nil || !dec.Accepted {
			if picked != nil {
				if picked.SourceRef != "" {
					rejectedRefs = append(rejectedRefs, picked.SourceRef)
				}
				if picked.ThumbRef != "" {
					rejectedRefs = append(rejectedRefs, picked.ThumbRef)
				}
			}
			continue
		}
		itemID := mgr.allocItemID()
		var kind corev1.AttachmentItemKind
		if dec.Route == corev1.AttachmentCandidateRoute_ATTACHMENT_CANDIDATE_ROUTE_IMAGE {
			kind = corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE
		} else {
			kind = corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_FILE
		}
		cand := cands[i]
		dispName := dec.DisplayName
		if dispName == "" && cand.Name != "" {
			dispName = sharedfiles.SanitizeFileName(cand.Name)
		}
		it := &attItem{
			id:                itemID,
			kind:              kind,
			isVideo:           dec.IsVideo,
			fileName:          strings.TrimSpace(cand.Name),
			displayName:       dispName,
			mimeType:          cand.MimeType,
			sizeBytes:         cand.SizeBytes,
			hasOriginalSource: picked.HasOriginalSource,
			hasDuration:       picked.HasDuration,
			durationMs:        picked.DurationMs,
			sourceRef:         picked.SourceRef,
			thumbRef:          picked.ThumbRef,
			state:             corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_PREPARING,
			phase:             attPhaseIdle,
			generation:        1,
			targetMachineID:   machineID,
		}
		if it.kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_FILE && it.fileName == "" {
			it.fileName = it.displayName
		}
		if !it.hasLocalCopy() {
			it.remoteOnly = true
			c.markItemMissingLocal(it)
		}
		d.items = append(d.items, it)
		added = true
	}

	if added {
		c.syncNewSessionAttachmentCount(d)
		if d.loadState == draftLoadLoaded {
			c.enqueueDraftWrite(d, &effects)
		}
		c.startPendingDraftUploads(ctx, d, &effects)
		c.pumpAttachmentQueues(ctx, &effects)
	}
	c.emitUnreferencedLocalReleases(rejectedRefs, &effects)
	c.markDraftDirty(ctx, d.scope)
	return effects
}

func (c *Core) handleDraftRemoveItem(ctx *sync.StepContext, act *corev1.DraftRemoveItemAction) []*corev1.Effect {
	if act == nil || act.ItemId == "" || c.m.attachments == nil {
		return nil
	}
	d := c.m.attachments.drafts[attachments.ScopeStorageKey(act.Scope)]
	if d == nil {
		return nil
	}
	idx := -1
	for i, it := range d.items {
		if it != nil && it.id == act.ItemId {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	var effects []*corev1.Effect
	removed := d.items[idx]
	d.items = append(d.items[:idx], d.items[idx+1:]...)
	// Synchronously invalidate & abort unprotected transfer before releasing local refs.
	c.invalidateItem(ctx, attOwnerDraft, d.key, removed, true, &effects)
	d.notice = nil
	if imgs, _ := d.itemCounts(); imgs == 0 {
		d.original = false
		d.viewerOpen = false
		d.viewerIndex = 0
	} else if d.viewerIndex >= imgs {
		d.viewerIndex = imgs - 1
	}
	c.syncNewSessionAttachmentCount(d)
	c.scheduleDraftExpiryTimer(ctx, d)
	var targetSeq uint64
	if d.loadState == draftLoadLoaded {
		targetSeq = c.enqueueDraftWrite(d, &effects)
	}
	c.releaseDraftLocalRefsAfterWrite(d, targetSeq, removed.localRefs(), &effects)
	c.pumpAttachmentQueues(ctx, &effects)
	c.markDraftDirty(ctx, d.scope)
	return effects
}

func (c *Core) handleDraftRetryItem(ctx *sync.StepContext, act *corev1.DraftRetryItemAction) []*corev1.Effect {
	if act == nil || act.ItemId == "" || c.m.attachments == nil {
		return nil
	}
	d := c.m.attachments.drafts[attachments.ScopeStorageKey(act.Scope)]
	if d == nil {
		return nil
	}
	it := d.findItem(act.ItemId)
	if it == nil {
		return nil
	}
	var effects []*corev1.Effect
	if it.remoteOnly || !it.hasLocalCopy() {
		c.markItemMissingLocal(it)
		c.markDraftDirty(ctx, d.scope)
		return nil
	}
	d.activated = true
	c.invalidateItem(ctx, attOwnerDraft, d.key, it, true, &effects)
	if it.kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_FILE {
		// Recompute file hash & size on manual retry in case the underlying file changed on disk.
		it.sha256 = nil
		it.sizeBytes = 0
	}
	it.restartedOnce = false
	it.retriedReconnect = false
	c.startItemPipeline(ctx, attOwnerDraft, d.key, d.scope, c.resolveDraftMachineID(d), d.original, it, &effects)
	c.pumpAttachmentQueues(ctx, &effects)
	c.markDraftDirty(ctx, d.scope)
	return effects
}

func (c *Core) handleDraftSetOriginal(ctx *sync.StepContext, act *corev1.DraftSetOriginalAction) []*corev1.Effect {
	if act == nil || c.m.attachments == nil {
		return nil
	}
	d := c.m.attachments.drafts[attachments.ScopeStorageKey(act.Scope)]
	if d == nil || d.loadState == draftLoadLoading || d.sending {
		return nil
	}
	if d.original == act.Original || !d.canToggleOriginal() {
		return nil
	}
	d.original = act.Original
	var effects []*corev1.Effect
	var oldPrepared []string
	for _, it := range d.items {
		if it == nil || it.kind != corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE {
			continue
		}
		if it.preparedRef != "" {
			oldPrepared = append(oldPrepared, it.preparedRef)
		}
		// Synchronously invalidate image (generation++, clear remote ID/expiry/prepared validity, abort unprotected).
		it.hasPrepared = false
		it.preparedRef = ""
		it.sha256 = nil
		c.invalidateItem(ctx, attOwnerDraft, d.key, it, true, &effects)
		c.startItemPipeline(ctx, attOwnerDraft, d.key, d.scope, c.resolveDraftMachineID(d), d.original, it, &effects)
	}
	c.scheduleDraftExpiryTimer(ctx, d)
	var targetSeq uint64
	if d.loadState == draftLoadLoaded {
		targetSeq = c.enqueueDraftWrite(d, &effects)
	}
	c.releaseDraftLocalRefsAfterWrite(d, targetSeq, oldPrepared, &effects)
	c.pumpAttachmentQueues(ctx, &effects)
	c.markDraftDirty(ctx, d.scope)
	return effects
}

func (c *Core) handleDraftClear(ctx *sync.StepContext, act *corev1.DraftClearAction) []*corev1.Effect {
	if act == nil || c.m.attachments == nil {
		return nil
	}
	d := c.m.attachments.drafts[attachments.ScopeStorageKey(act.Scope)]
	if d == nil {
		return nil
	}
	var effects []*corev1.Effect
	var refs []string
	for _, it := range d.items {
		if it == nil {
			continue
		}
		c.invalidateItem(ctx, attOwnerDraft, d.key, it, true, &effects)
		refs = append(refs, it.localRefs()...)
	}
	d.items = nil
	d.text = ""
	d.textRev++
	d.original = false
	d.notice = nil
	d.storageError = nil
	d.viewerOpen = false
	d.viewerIndex = 0
	clear(d.reservations)
	d.expiryTimerTargetMs = 0
	c.m.sync.CancelAppTimer(ctx, corev1.TimerKind_TIMER_KIND_DRAFT_EXPIRY, d.key)
	c.syncNewSessionPromptFromDraft(d)
	c.syncNewSessionAttachmentCount(d)
	var targetSeq uint64
	if c.m.attachments.persistDrafts && d.loadState == draftLoadLoaded {
		targetSeq = c.enqueueDraftWrite(d, &effects)
	}
	c.releaseDraftLocalRefsAfterWrite(d, targetSeq, refs, &effects)
	c.pumpAttachmentQueues(ctx, &effects)
	c.markDraftDirty(ctx, d.scope)
	return effects
}

func (c *Core) handleDraftDismissNotice(ctx *sync.StepContext, act *corev1.DraftDismissNoticeAction) []*corev1.Effect {
	if act == nil || c.m.attachments == nil {
		return nil
	}
	d := c.m.attachments.drafts[attachments.ScopeStorageKey(act.Scope)]
	if d == nil || d.notice == nil {
		return nil
	}
	d.notice = nil
	c.markDraftDirty(ctx, d.scope)
	return nil
}

func (c *Core) handleDraftImageViewerOpen(ctx *sync.StepContext, act *corev1.DraftImageViewerOpenAction) []*corev1.Effect {
	if act == nil || attachments.ScopeStorageKey(act.Scope) == "" {
		return nil
	}
	d := c.ensureDraftState(act.Scope)
	imgs, _ := d.itemCounts()
	d.viewerOpen = true
	if imgs <= 0 {
		d.viewerIndex = 0
	} else {
		idx := act.Index
		if idx < 0 {
			idx = 0
		} else if idx >= imgs {
			idx = imgs - 1
		}
		d.viewerIndex = idx
	}
	c.markDraftDirty(ctx, d.scope)
	return nil
}

func (c *Core) handleDraftImageViewerNavigate(ctx *sync.StepContext, act *corev1.DraftImageViewerNavigateAction) []*corev1.Effect {
	if act == nil || c.m.attachments == nil {
		return nil
	}
	d := c.m.attachments.drafts[attachments.ScopeStorageKey(act.Scope)]
	if d == nil {
		return nil
	}
	imgs, _ := d.itemCounts()
	if imgs <= 0 {
		return nil
	}
	var next int32
	if act.SetIndex {
		next = act.Index
	} else {
		next = d.viewerIndex + act.Delta
	}
	if next < 0 {
		next = 0
	} else if next >= imgs {
		next = imgs - 1
	}
	d.viewerOpen = true
	d.viewerIndex = next
	c.markDraftDirty(ctx, d.scope)
	return nil
}

func (c *Core) handleDraftImageViewerClose(ctx *sync.StepContext, act *corev1.DraftImageViewerCloseAction) []*corev1.Effect {
	if act == nil || c.m.attachments == nil {
		return nil
	}
	d := c.m.attachments.drafts[attachments.ScopeStorageKey(act.Scope)]
	if d == nil || !d.viewerOpen {
		return nil
	}
	d.viewerOpen = false
	d.viewerIndex = 0
	c.markDraftDirty(ctx, d.scope)
	return nil
}

func (c *Core) enqueueDraftWrite(d *attDraft, effects *[]*corev1.Effect) uint64 {
	if d == nil || c.m.attachments == nil || !c.m.attachments.persistDrafts {
		return 0
	}
	d.writeSeq++
	if d.activeWriteOpID != 0 {
		d.dirtyWrite = true
		return d.writeSeq
	}
	c.emitDraftWriteNow(d, effects)
	return d.writeSeq
}

func (c *Core) emitDraftWriteNow(d *attDraft, effects *[]*corev1.Effect) {
	mgr := c.m.ensureAttachments()
	d.dirtyWrite = false
	if d.text == "" && len(d.items) == 0 {
		// Empty draft: delete persisted entry and satisfy any waiting outbox barriers immediately.
		*effects = append(*effects, makeDraftStorageDeleteEffect(corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_DRAFT, d.key))
		c.flushDraftWriteBarriers(d, d.writeSeq, effects)
		return
	}
	rec := &corev1.PersistedDraftRecord{
		SchemaVersion: attachments.PersistedSchemaVersion,
		Scope:         cloneDraftScope(d.scope),
		Text:          d.text,
		Original:      d.original,
		UpdatedMs:     c.m.sync.NowMs(),
	}
	var refs []string
	for _, it := range d.items {
		if it == nil || it.remoteOnly {
			continue
		}
		rec.Items = append(rec.Items, it.toPersisted())
		refs = append(refs, it.localRefs()...)
	}
	raw, err := attachments.EncodePersistedDraft(rec)
	if err != nil {
		return
	}
	opID := mgr.allocOpID()
	d.activeWriteOpID = opID
	mgr.draftWriteOps[opID] = attWriteOpMeta{
		kind:     attWriteKindDraft,
		draftKey: d.key,
		writeSeq: d.writeSeq,
	}
	*effects = append(*effects, &corev1.Effect{
		Effect: &corev1.Effect_DraftStorageWrite{
			DraftStorageWrite: &corev1.DraftStorageWriteEffect{
				OpId:           opID,
				Bucket:         corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_DRAFT,
				Key:            d.key,
				Data:           raw,
				ReferencedRefs: refs,
			},
		},
	})
}

func (c *Core) releaseDraftLocalRefsAfterWrite(d *attDraft, targetSeq uint64, refs []string, effects *[]*corev1.Effect) {
	if len(refs) == 0 {
		return
	}
	if d != nil && targetSeq > 0 && d.activeWriteOpID != 0 {
		d.pendingLocalReleases = append(d.pendingLocalReleases, pendingLocalReleaseBarrier{
			writeSeq: targetSeq,
			refs:     slices.Clone(refs),
		})
		return
	}
	c.emitUnreferencedLocalReleases(refs, effects)
}

func (c *Core) flushDraftWriteBarriers(d *attDraft, settledSeq uint64, effects *[]*corev1.Effect) {
	if d == nil {
		return
	}
	if len(d.pendingOutboxDeletes) > 0 {
		kept := d.pendingOutboxDeletes[:0]
		for _, b := range d.pendingOutboxDeletes {
			if !b.pendingLoad && b.writeSeq <= settledSeq {
				*effects = append(*effects, makeDraftStorageDeleteEffect(corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_OUTBOX, b.outboxKey))
			} else {
				kept = append(kept, b)
			}
		}
		d.pendingOutboxDeletes = kept
	}
	if len(d.pendingLocalReleases) > 0 {
		var releaseCandidates []string
		keptRel := d.pendingLocalReleases[:0]
		for _, r := range d.pendingLocalReleases {
			if r.writeSeq <= settledSeq {
				releaseCandidates = append(releaseCandidates, r.refs...)
			} else {
				keptRel = append(keptRel, r)
			}
		}
		d.pendingLocalReleases = keptRel
		c.emitUnreferencedLocalReleases(releaseCandidates, effects)
	}
}

func (c *Core) handleDraftStorageWriteResult(ctx *sync.StepContext, ev *corev1.DraftStorageWriteResultEvent) []*corev1.Effect {
	if ev == nil || c.m.attachments == nil || ev.OpId == 0 {
		return nil
	}
	mgr := c.m.attachments
	meta, ok := mgr.draftWriteOps[ev.OpId]
	if !ok {
		return nil
	}
	delete(mgr.draftWriteOps, ev.OpId)

	var effects []*corev1.Effect
	switch meta.kind {
	case attWriteKindDraft:
		d := mgr.drafts[meta.draftKey]
		if d == nil || d.activeWriteOpID != ev.OpId {
			return nil
		}
		d.activeWriteOpID = 0
		if !ev.Ok {
			d.storageError = attachments.ClassifyStorageErrorText(ev.ErrorKind, false)
		} else {
			d.storageError = nil
			c.flushDraftWriteBarriers(d, meta.writeSeq, &effects)
		}
		if d.dirtyWrite {
			c.emitDraftWriteNow(d, &effects)
		}
		c.markDraftDirty(ctx, d.scope)

	case attWriteKindPreSendOutbox:
		c.onPreSendOutboxWriteResult(ctx, meta, ev, &effects)

	case attWriteKindResendOutbox:
		c.onResendOutboxWriteResult(ctx, meta, ev, &effects)

	case attWriteKindFailedResendOutbox:
		c.onFailedResendOutboxWriteResult(meta, ev, &effects)
	}
	return effects
}

func (c *Core) scheduleDraftExpiryTimer(ctx *sync.StepContext, d *attDraft) {
	if d == nil || ctx == nil {
		return
	}
	nowMs := c.m.sync.NowMs()
	machineID := c.resolveDraftMachineID(d)
	if !c.uploadNetworkReady(machineID) || nowMs <= 0 {
		if d.expiryTimerTargetMs != 0 {
			d.expiryTimerTargetMs = 0
			c.m.sync.CancelAppTimer(ctx, corev1.TimerKind_TIMER_KIND_DRAFT_EXPIRY, d.key)
		}
		return
	}
	var earliest int64
	for _, it := range d.items {
		if it != nil && it.isReadyFor(machineID, d.original, nowMs) {
			if earliest == 0 || it.expiresMs < earliest {
				earliest = it.expiresMs
			}
		}
	}
	if earliest == 0 {
		if d.expiryTimerTargetMs != 0 {
			d.expiryTimerTargetMs = 0
			c.m.sync.CancelAppTimer(ctx, corev1.TimerKind_TIMER_KIND_DRAFT_EXPIRY, d.key)
		}
		return
	}
	if d.expiryTimerTargetMs == earliest {
		return
	}
	d.expiryTimerTargetMs = earliest
	delay := earliest - nowMs
	if delay < 1 {
		delay = 1
	}
	c.m.sync.ScheduleAppTimer(ctx, corev1.TimerKind_TIMER_KIND_DRAFT_EXPIRY, d.key, delay)
}

func (c *Core) handleDraftExpiryTimer(ctx *sync.StepContext, draftKey string) []*corev1.Effect {
	if c.m.attachments == nil || draftKey == "" {
		return nil
	}
	d := c.m.attachments.drafts[draftKey]
	if d == nil {
		return nil
	}
	d.expiryTimerTargetMs = 0
	var effects []*corev1.Effect
	if c.refreshDraftExpiredItems(ctx, d, &effects) {
		c.pumpAttachmentQueues(ctx, &effects)
		c.markDraftDirty(ctx, d.scope)
	}
	c.scheduleDraftExpiryTimer(ctx, d)
	return effects
}

func (c *Core) refreshDraftExpiredItems(ctx *sync.StepContext, d *attDraft, effects *[]*corev1.Effect) bool {
	if d == nil {
		return false
	}
	nowMs := c.m.sync.NowMs()
	if nowMs <= 0 {
		return false
	}
	machineID := c.resolveDraftMachineID(d)
	any := false
	for _, it := range d.items {
		if it == nil || it.state != corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_READY {
			continue
		}
		if it.expiresMs > 0 && it.expiresMs <= nowMs {
			any = true
			c.invalidateItem(ctx, attOwnerDraft, d.key, it, true, effects)
			c.startItemPipeline(ctx, attOwnerDraft, d.key, d.scope, machineID, d.original, it, effects)
		}
	}
	if any {
		c.scheduleDraftExpiryTimer(ctx, d)
	}
	return any
}

func (c *Core) viewAttachmentDraft(req *corev1.AttachmentDraftScreenRequest) (*corev1.ViewModel, error) {
	var scope *corev1.DraftScope
	if req != nil {
		scope = req.Scope
	}
	return &corev1.ViewModel{
		View: &corev1.ViewModel_AttachmentDraft{
			AttachmentDraft: &corev1.AttachmentDraftViewModel{
				Draft: c.buildAttachmentDraftView(scope, true),
			},
		},
	}, nil
}

func (c *Core) buildAttachmentDraftView(scope *corev1.DraftScope, alwaysBuild bool) *corev1.AttachmentDraftView {
	key := attachments.ScopeStorageKey(scope)
	if key == "" {
		return nil
	}
	var d *attDraft
	if c.m.attachments != nil {
		d = c.m.attachments.drafts[key]
	}
	if d == nil && !alwaysBuild {
		return nil
	}
	if d == nil {
		d = &attDraft{
			scope:        cloneDraftScope(scope),
			key:          key,
			reservations: make(map[string]attSlotReservation),
			loadState:    draftLoadLoaded,
		}
	}

	machineID := c.resolveDraftMachineID(d)
	caps, onlineReady := c.resolveAttachmentCaps(machineID, false)
	nowMs := c.m.sync.NowMs()
	curImgs, curFiles := d.itemCounts()
	resImgs, resFiles := d.reservedCounts()
	effImgs := curImgs + resImgs
	effFiles := curFiles + resFiles

	imagesAllowed := caps.SupportsImages && caps.ImageUploadBlockedReason == nil
	filesAllowed := caps.SupportsFiles && caps.FileUploadBlockedReason == nil
	room := attachments.LibraryRoom(effImgs, caps.MaxImages, effFiles, caps.MaxFiles, imagesAllowed, filesAllowed)

	var galleryBlocked *corev1.Text
	if !imagesAllowed && !filesAllowed {
		if caps.ImageUploadBlockedReason != nil {
			galleryBlocked = caps.ImageUploadBlockedReason
		} else {
			galleryBlocked = caps.FileUploadBlockedReason
		}
	} else if room == 0 {
		if imagesAllowed {
			galleryBlocked = text.Msg(text.KeyAttachmentMaxImages, text.Int("count", int64(caps.MaxImages)))
		} else {
			galleryBlocked = text.Msg(text.KeyAttachmentMaxFiles, text.Int("count", int64(caps.MaxFiles)))
		}
	}

	restoring := d.loadState == draftLoadLoading
	hasPendingPicks := resImgs > 0 || resFiles > 0
	canPickImages := !restoring && imagesAllowed && effImgs < caps.MaxImages
	canPickFiles := !restoring && filesAllowed && effFiles < caps.MaxFiles
	canPickGallery := !restoring && (imagesAllowed || filesAllowed) && room > 0

	var imagesPickBlocked, filesPickBlocked *corev1.Text
	if !canPickImages && !restoring {
		if caps.ImageUploadBlockedReason != nil {
			imagesPickBlocked = caps.ImageUploadBlockedReason
		} else if caps.SupportsImages {
			imagesPickBlocked = text.Msg(text.KeyAttachmentMaxImages, text.Int("count", int64(caps.MaxImages)))
		}
	}
	if !canPickFiles && !restoring {
		if caps.FileUploadBlockedReason != nil {
			filesPickBlocked = caps.FileUploadBlockedReason
		} else if caps.SupportsFiles {
			filesPickBlocked = text.Msg(text.KeyAttachmentMaxFiles, text.Int("count", int64(caps.MaxFiles)))
		}
	}

	var (
		allViews        []*corev1.AttachmentDraftItemView
		imgViews        []*corev1.AttachmentDraftItemView
		fileViews       []*corev1.AttachmentDraftItemView
		imgProblems     []*corev1.ImageProblemView
		totalImageBytes int64
		anyBusy         bool
		allReady        = !restoring && !d.sending && !hasPendingPicks
		firstFileErr    *corev1.Text
		hasFiles        bool
		imgIdx          int32
	)
	for _, it := range d.items {
		if it == nil {
			continue
		}
		isImg := it.kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE
		if !isImg {
			hasFiles = true
		}
		effState := it.state
		if effState == corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_READY && !it.isReadyFor(machineID, d.original, nowMs) {
			allReady = false
		}
		switch effState {
		case corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_PREPARING,
			corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_UPLOADING,
			corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_WAITING_RECONNECT:
			anyBusy = true
			allReady = false
		case corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_PAUSED,
			corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_FAILED:
			allReady = false
		}

		if isImg && it.sizeBytes > 0 {
			totalImageBytes += it.sizeBytes
		}
		if isImg && effState == corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_FAILED && it.problem != nil {
			imgProblems = append(imgProblems, it.problem)
		}
		if !isImg && effState == corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_FAILED && firstFileErr == nil {
			if it.fileError != nil {
				firstFileErr = it.fileError
			} else {
				firstFileErr = text.Msg(text.KeyAttachmentUploadFailed)
			}
		}

		dispName := it.displayName
		if dispName == "" && it.fileName != "" {
			dispName = sharedfiles.SanitizeFileName(it.fileName)
		}
		head, tail := sharedfiles.SplitMiddleTruncateName(dispName)
		var sizeText *corev1.Text
		if it.sizeBytes >= 0 && (it.sizeBytes > 0 || !isImg) {
			sizeText = text.Plain(sharedfiles.FormatFileSize(it.sizeBytes))
		}
		var durText string
		if it.isVideo && it.hasDuration && it.durationMs > 0 {
			durText = sharedfiles.FormatMediaDuration(it.durationMs)
		}

		itemImgIdx := int32(-1)
		var posText, posA11y, remA11y *corev1.Text
		if isImg {
			itemImgIdx = imgIdx
			posText = attachments.DraftViewerPositionText(imgIdx, curImgs)
			posA11y = text.Msg(text.KeyImagePosition, text.Int("index", int64(imgIdx+1)), text.Int("count", int64(curImgs)))
			remA11y = text.Msg(text.KeyImageRemoveIndex, text.Int("index", int64(imgIdx+1)))
			imgIdx++
		} else if it.isVideo {
			remA11y = text.Msg(text.KeyAttachmentRemoveVideo, text.Str("name", dispName))
		} else {
			remA11y = text.Msg(text.KeyAttachmentRemoveFile, text.Str("name", dispName))
		}

		canRetry := effState == corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_FAILED &&
			!it.remoteOnly && it.hasLocalCopy() &&
			(!isImg || it.problem == nil || it.problem.Retryable || !it.hasPrepared)
		canPause := effState == corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_UPLOADING ||
			effState == corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_WAITING_RECONNECT
		canResume := effState == corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_PAUSED && onlineReady

		iv := &corev1.AttachmentDraftItemView{
			ItemId:          it.id,
			Kind:            it.kind,
			IsVideo:         it.isVideo,
			State:           effState,
			Progress:        it.progress,
			ProgressPercent: it.progressPct,
			DisplayName:     dispName,
			NameHead:        head,
			NameTail:        tail,
			SizeBytes:       it.sizeBytes,
			SizeText:        sizeText,
			DurationText:    durText,
			Width:           it.width,
			Height:          it.height,
			SourceRef:       it.sourceRef,
			PreparedRef:     it.preparedRef,
			ThumbRef:        it.thumbRef,
			StatusText:      attachments.DraftItemStatusText(it.kind, effState, it.progressPct, it.problem, it.fileError),
			Problem:         it.problem,
			FileErrorText:   it.fileError,
			CanRetry:        canRetry,
			CanPause:        canPause,
			CanResume:       canResume,
			CanRemove:       !d.sending,
			ImageIndex:      itemImgIdx,
			PositionText:    posText,
			PositionA11Y:    posA11y,
			RemoveA11Y:      remA11y,
			RemoteOnly:      it.remoteOnly,
		}
		allViews = append(allViews, iv)
		if isImg {
			imgViews = append(imgViews, iv)
		} else {
			fileViews = append(fileViews, iv)
		}
	}

	canToggleOrig := !restoring && !d.sending && d.canToggleOriginal()
	var origDisabledReason *corev1.Text
	if curImgs > 0 && !d.canToggleOriginal() {
		origDisabledReason = text.Msg(text.KeyImageOriginalMissing)
	}
	var origTotalSizeText *corev1.Text
	if d.original && totalImageBytes > 0 {
		origTotalSizeText = attachments.FormatOriginalTotalSize(totalImageBytes)
	}

	failureSummary := attachments.SummarizeImageFailures(imgProblems)
	if failureSummary == nil && firstFileErr != nil {
		failureSummary = firstFileErr
	}

	busy := restoring || d.sending || hasPendingPicks || anyBusy
	hasContent := strings.TrimSpace(d.text) != "" || len(d.items) > 0
	var sendBlocked *corev1.Text
	if hasFiles && !caps.SupportsFiles {
		allReady = false
		sendBlocked = caps.FileUploadBlockedReason
		if sendBlocked == nil {
			sendBlocked = text.Msg(text.KeyAttachmentUpdateRequired)
		}
	} else if curImgs > 0 && !caps.SupportsImages {
		allReady = false
		sendBlocked = caps.ImageUploadBlockedReason
	} else if !allReady && firstFileErr != nil {
		sendBlocked = firstFileErr
	} else if !allReady && failureSummary != nil {
		sendBlocked = failureSummary
	}
	canSend := onlineReady && allReady && hasContent && sendBlocked == nil

	viewer := buildDraftImageViewerView(d.viewerOpen, d.viewerIndex, imgViews)

	return &corev1.AttachmentDraftView{
		Scope:                      cloneDraftScope(d.scope),
		Text:                       d.text,
		TextRevision:               d.textRev,
		Restoring:                  restoring,
		Sending:                    d.sending,
		HasPendingPicks:            hasPendingPicks,
		ReservedImages:             resImgs,
		ReservedFiles:              resFiles,
		Original:                   d.original,
		CanToggleOriginal:          canToggleOrig,
		OriginalToggleDisabledReason: origDisabledReason,
		TotalImageBytes:            totalImageBytes,
		OriginalTotalSizeText:      origTotalSizeText,
		OriginalHint:               text.Msg(text.KeyImageOriginalHint),
		FileMetadataNotice:         text.Msg(text.KeyAttachmentMetadata),
		ImageCount:                 curImgs,
		FileCount:                  curFiles,
		MaxImages:                  caps.MaxImages,
		MaxFiles:                   caps.MaxFiles,
		ImageCountText:             text.Plain(strconv.Itoa(int(curImgs)) + " / " + strconv.Itoa(int(caps.MaxImages))),
		FileCountText:              text.Plain(strconv.Itoa(int(curFiles)) + " / " + strconv.Itoa(int(caps.MaxFiles))),
		Items:                      allViews,
		Images:                     imgViews,
		Files:                      fileViews,
		Busy:                       busy,
		Ready:                      allReady,
		CanSend:                    canSend,
		SendBlockedReason:          sendBlocked,
		HasUnsavedContent:          d.text != "" || len(d.items) > 0 || hasPendingPicks,
		Notice:                     d.notice,
		FailureSummary:             failureSummary,
		StorageError:               d.storageError,
		CanPickImages:              canPickImages,
		CanPickFiles:               canPickFiles,
		CanPickGallery:             canPickGallery,
		LibraryRoom:                room,
		GalleryAllowsVideo:         caps.SupportsFiles,
		GalleryAcceptMime:          attachments.GalleryAcceptMime(caps.SupportsFiles),
		ImagesPickBlockedReason:    imagesPickBlocked,
		FilesPickBlockedReason:     filesPickBlocked,
		GalleryBlockedReason:       galleryBlocked,
		ImageViewer:                viewer,
	}
}

func buildDraftImageViewerView(open bool, curIdx int32, imgViews []*corev1.AttachmentDraftItemView) *corev1.DraftImageViewerView {
	total := int32(len(imgViews))
	out := &corev1.DraftImageViewerView{
		Open:       open,
		TotalCount: total,
	}
	if total == 0 {
		out.EmptyText = text.Msg(text.KeyImageViewerEmpty)
		return out
	}
	if curIdx < 0 {
		curIdx = 0
	} else if curIdx >= total {
		curIdx = total - 1
	}
	out.CurrentIndex = curIdx
	out.HasPrev = curIdx > 0
	out.HasNext = curIdx+1 < total
	out.PositionText = attachments.DraftViewerPositionText(curIdx, total)
	out.PositionA11Y = text.Msg(text.KeyImagePosition, text.Int("index", int64(curIdx+1)), text.Int("count", int64(total)))
	out.CurrentImage = imgViews[curIdx]
	return out
}

func (d *attDraft) findItem(itemID string) *attItem {
	for _, it := range d.items {
		if it != nil && it.id == itemID {
			return it
		}
	}
	return nil
}

func (d *attDraft) canToggleOriginal() bool {
	hasImg := false
	for _, it := range d.items {
		if it != nil && it.kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE {
			hasImg = true
			if it.remoteOnly || !it.hasOriginalSource || it.sourceRef == "" {
				return false
			}
		}
	}
	return hasImg
}

func (it *attItem) hasLocalCopy() bool {
	if it == nil {
		return false
	}
	if it.kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE {
		return it.sourceRef != "" || (it.hasPrepared && it.preparedRef != "")
	}
	return it.sourceRef != ""
}

func (it *attItem) preparedValid(original bool) bool {
	if it == nil || it.kind != corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE {
		return false
	}
	return it.hasPrepared && it.preparedRef != "" && it.preparedOriginal == original && len(it.sha256) == 32
}

func (it *attItem) isReadyFor(machineID string, draftOriginal bool, nowMs int64) bool {
	if it == nil || it.state != corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_READY {
		return false
	}
	if machineID == "" || it.uploadedMachineID != machineID || it.remoteID == "" {
		return false
	}
	if nowMs > 0 && it.expiresMs > 0 && it.expiresMs <= nowMs {
		return false
	}
	if it.kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE && it.preparedOriginal != draftOriginal {
		return false
	}
	return true
}

func (it *attItem) localRefs() []string {
	if it == nil {
		return nil
	}
	var out []string
	if it.sourceRef != "" {
		out = append(out, it.sourceRef)
	}
	if it.preparedRef != "" {
		out = append(out, it.preparedRef)
	}
	if it.thumbRef != "" {
		out = append(out, it.thumbRef)
	}
	return out
}

func (it *attItem) toPersisted() *corev1.PersistedAttachmentItem {
	return &corev1.PersistedAttachmentItem{
		ItemId:            it.id,
		Kind:              it.kind,
		IsVideo:           it.isVideo,
		FileName:          it.fileName,
		MimeType:          it.mimeType,
		SizeBytes:         it.sizeBytes,
		Sha256:            slices.Clone(it.sha256),
		ImageFormat:       it.imageFormat,
		Width:             it.width,
		Height:            it.height,
		PreparedOriginal:  it.preparedOriginal,
		HasOriginalSource: it.hasOriginalSource,
		HasPrepared:       it.hasPrepared,
		SourceRef:         it.sourceRef,
		PreparedRef:       it.preparedRef,
		ThumbRef:          it.thumbRef,
		RemoteId:          it.remoteID,
		TransferId:        it.transferID,
		MachineId:         it.uploadedMachineID,
		ExpiresMs:         it.expiresMs,
		HasDuration:       it.hasDuration,
		DurationMs:        it.durationMs,
	}
}

func attItemFromPersisted(p *corev1.PersistedAttachmentItem) *attItem {
	disp := sharedfiles.SanitizeFileName(p.FileName)
	if strings.TrimSpace(p.FileName) == "" {
		disp = ""
	}
	return &attItem{
		id:                p.ItemId,
		kind:              p.Kind,
		isVideo:           p.IsVideo,
		fileName:          p.FileName,
		displayName:       disp,
		mimeType:          p.MimeType,
		sizeBytes:         p.SizeBytes,
		sha256:            slices.Clone(p.Sha256),
		imageFormat:       p.ImageFormat,
		width:             p.Width,
		height:            p.Height,
		preparedOriginal:  p.PreparedOriginal,
		hasOriginalSource: p.HasOriginalSource,
		hasPrepared:       p.HasPrepared,
		sourceRef:         p.SourceRef,
		preparedRef:       p.PreparedRef,
		thumbRef:          p.ThumbRef,
		remoteID:          p.RemoteId,
		transferID:        p.TransferId,
		uploadedMachineID: p.MachineId,
		targetMachineID:   p.MachineId,
		expiresMs:         p.ExpiresMs,
		hasDuration:       p.HasDuration,
		durationMs:        p.DurationMs,
	}
}

func (it *attItem) cloneForSnapshot() *attItem {
	if it == nil {
		return nil
	}
	cp := *it
	cp.sha256 = slices.Clone(it.sha256)
	cp.activeOpID = 0
	cp.activeReqID = ""
	cp.phase = attPhaseIdle
	return &cp
}

func (it *attItem) toOutboxSummary() *corev1.OutboxAttachmentSummary {
	disp := it.displayName
	if disp == "" && it.fileName != "" {
		disp = sharedfiles.SanitizeFileName(it.fileName)
	}
	var sizeText *corev1.Text
	if it.sizeBytes >= 0 && (it.sizeBytes > 0 || it.kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_FILE) {
		sizeText = text.Plain(sharedfiles.FormatFileSize(it.sizeBytes))
	}
	return &corev1.OutboxAttachmentSummary{
		ItemId:       it.id,
		Kind:         it.kind,
		IsVideo:      it.isVideo,
		DisplayName:  disp,
		SizeBytes:    it.sizeBytes,
		SizeText:     sizeText,
		Width:        it.width,
		Height:       it.height,
		ThumbRef:     it.thumbRef,
		RemoteId:     it.remoteID,
		HasLocalCopy: !it.remoteOnly && it.hasLocalCopy(),
	}
}

func makeDraftStorageDeleteEffect(bucket corev1.DraftStorageBucket, key string) *corev1.Effect {
	return &corev1.Effect{
		Effect: &corev1.Effect_DraftStorageDelete{
			DraftStorageDelete: &corev1.DraftStorageDeleteEffect{
				Bucket: bucket,
				Key:    key,
			},
		},
	}
}

func (c *Core) emitUnreferencedLocalReleases(candidates []string, effects *[]*corev1.Effect) {
	if len(candidates) == 0 || c.m.attachments == nil {
		return
	}
	inUse := c.collectAllReferencedLocalRefs()
	seen := make(map[string]bool, len(candidates))
	var toRelease []string
	for _, ref := range candidates {
		if ref == "" || seen[ref] || inUse[ref] {
			continue
		}
		seen[ref] = true
		toRelease = append(toRelease, ref)
	}
	if len(toRelease) == 0 {
		return
	}
	sortutil.Strings(toRelease)
	*effects = append(*effects, &corev1.Effect{
		Effect: &corev1.Effect_AttachmentReleaseLocal{
			AttachmentReleaseLocal: &corev1.AttachmentReleaseLocalEffect{
				Refs: toRelease,
			},
		},
	})
}

func (c *Core) collectAllReferencedLocalRefs() map[string]bool {
	inUse := make(map[string]bool)
	mgr := c.m.attachments
	if mgr == nil {
		return inUse
	}
	addItems := func(items []*attItem) {
		for _, it := range items {
			for _, r := range it.localRefs() {
				inUse[r] = true
			}
		}
	}
	for _, d := range mgr.drafts {
		if d == nil {
			continue
		}
		addItems(d.items)
		if d.pendingSend != nil {
			addItems(d.pendingSend.items)
		}
	}
	for _, rec := range mgr.outbox {
		if rec != nil {
			addItems(rec.items)
		}
	}
	for _, job := range mgr.resendJobs {
		if job != nil && !job.aborted {
			addItems(job.items)
		}
	}
	return inUse
}

func (c *Core) onAttachmentsSyncEvent(ev *corev1.Event, effects *[]*corev1.Effect) {
	if c.m.attachments == nil || ev == nil {
		return
	}
	switch k := ev.Event.(type) {
	case *corev1.Event_SyncLifecycle:
		if k.SyncLifecycle == nil {
			return
		}
		switch k.SyncLifecycle.Kind {
		case corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_STOP:
			c.pauseAllActiveUploadsOnStop(effects)
		case corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START:
			c.resumeStoppedUploadsOnStart(effects)
		case corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_FOREGROUND:
			c.refreshAllExpiredDraftsOnForeground(effects)
		}
	}
}
