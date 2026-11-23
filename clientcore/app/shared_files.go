package app

import (
	"slices"
	"strings"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/outcome"
	"github.com/Lewin671/remote-agent/clientcore/domain/sharedfiles"
	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/sync"
)

type sfKey struct {
	sessionID string
	fileID    string
}

func (k sfKey) timerKey() string {
	return k.sessionID + "\x00" + k.fileID
}

func parseSFTimerKey(raw string) (sfKey, bool) {
	sid, fid, ok := strings.Cut(raw, "\x00")
	if !ok || sid == "" || fid == "" {
		return sfKey{}, false
	}
	return sfKey{sessionID: sid, fileID: fid}, true
}

type sfPhase uint8

const (
	sfPhaseIdle sfPhase = iota
	sfPhaseInspectingDisk
	sfPhaseFetchingChunk
	sfPhaseWritingChunk
	sfPhaseWaitingReconnect
	sfPhaseWaitingPriorVerify
	sfPhaseVerifying
)

type sfEntry struct {
	key    sfKey
	status sharedfiles.TransferStatus
	phase  sfPhase

	// Authoritative file metadata snapshot captured at download start (survives history buffer eviction).
	machineID        string
	displayName      string
	kind             corev1.SharedFileKind
	hasMediaDuration bool
	sizeBytes        int64
	sha256           []byte
	chunkBytes       int32

	// Persisted transfer progress & partial metadata.
	committedOffset int64
	downloadedBytes int64
	hasPartial      bool
	partial         sharedfiles.PartialMeta
	bytesOnDisk     int64

	// Current attempt tracking.
	startedMonoMs           int64
	sessionBytes            int64
	hasEta                  bool
	etaSeconds              int64
	retriedOnce             bool
	waitingReconnect        bool
	reconnectDeadlineMonoMs int64
	reconnectNotBeforeMs    int64
	autoOpen                bool
	deliverOnly             bool

	// In-flight correlation IDs.
	activeReqID            string
	activeChunkOffset      int64
	activeOpID             uint64
	pendingCommittedOffset int64
	pendingValidLen        int64
	pausedPendingWriteOpID uint64
	pausedPendingCommitOff int64
	pausedPendingPartial   sharedfiles.PartialMeta

	// Media duration & failure state.
	hasDuration   bool
	durationMs    int64
	failureKind   corev1.SharedFileFailureKind
	failureDetail string
	failureText   *corev1.Text
}

type sfStaleVerify struct {
	key         sfKey
	deliverOnly bool
}

type sfManager struct {
	localCacheMaxBytes    int64
	storageFullKind       corev1.SharedFileFailureKind
	nextOpID              uint64
	activeScanOpID        uint64
	scanBaselineKeys      map[sfKey]bool
	scanMutatedKeys       map[sfKey]bool
	scanForgottenSessions map[string]bool
	visibleSessionID      string
	entries               map[sfKey]*sfEntry
	reqToKey              map[string]sfKey
	staleVerifyOps        map[uint64]sfStaleVerify
	inFlightVerifyKey     map[sfKey]uint64
}

func newSFManager() *sfManager {
	return &sfManager{
		storageFullKind:   corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_PHONE_STORAGE_FULL,
		entries:           make(map[sfKey]*sfEntry),
		reqToKey:          make(map[string]sfKey),
		staleVerifyOps:    make(map[uint64]sfStaleVerify),
		inFlightVerifyKey: make(map[sfKey]uint64),
	}
}

func (mgr *sfManager) markScanMutated(key sfKey) {
	if mgr == nil || mgr.activeScanOpID == 0 {
		return
	}
	if mgr.scanMutatedKeys == nil {
		mgr.scanMutatedKeys = make(map[sfKey]bool)
	}
	mgr.scanMutatedKeys[key] = true
}

func (m *Model) ensureSharedFiles() *sfManager {
	if m.sharedFiles == nil {
		m.sharedFiles = newSFManager()
	}
	return m.sharedFiles
}

func (mgr *sfManager) allocOpID() uint64 {
	mgr.nextOpID++
	return mgr.nextOpID
}

func (mgr *sfManager) hasTrackedEntries() bool {
	return mgr != nil && len(mgr.entries) > 0
}

func (c *Core) syncSharedFileUI(entry *sfEntry) {
	if entry == nil {
		return
	}
	store := c.m.ensureSessionStore()
	ui := store.ensureUI(entry.key.sessionID)
	if ui.SharedFiles == nil {
		ui.SharedFiles = make(map[string]*sharedfiles.LocalState)
	}
	if entry.status == sharedfiles.TransferStatusUnspecified && !entry.hasDuration {
		if _, existed := ui.SharedFiles[entry.key.fileID]; existed {
			delete(ui.SharedFiles, entry.key.fileID)
			ui.SharedFilesRev++
			store.pendingDirty = true
		}
		return
	}
	next := &sharedfiles.LocalState{
		Status:           entry.status,
		DownloadedBytes:  entry.downloadedBytes,
		HasEta:           entry.hasEta,
		EtaSeconds:       entry.etaSeconds,
		WaitingReconnect: entry.waitingReconnect,
		HasDuration:      entry.hasDuration,
		DurationMs:       entry.durationMs,
		FailureKind:      entry.failureKind,
		FailureDetail:    entry.failureDetail,
		FailureText:      entry.failureText,
	}
	prev := ui.SharedFiles[entry.key.fileID]
	if prev == nil || !localStatesEqual(prev, next) {
		ui.SharedFiles[entry.key.fileID] = next
		ui.SharedFilesRev++
		store.pendingDirty = true
	}
}

func (c *Core) clearSharedFileUI(key sfKey) {
	store := c.m.ensureSessionStore()
	ui := store.uiBySession[key.sessionID]
	if ui == nil || ui.SharedFiles == nil {
		return
	}
	if _, existed := ui.SharedFiles[key.fileID]; existed {
		delete(ui.SharedFiles, key.fileID)
		ui.SharedFilesRev++
		store.pendingDirty = true
	}
}

func localStatesEqual(a, b *sharedfiles.LocalState) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Status == b.Status &&
		a.DownloadedBytes == b.DownloadedBytes &&
		a.HasEta == b.HasEta &&
		a.EtaSeconds == b.EtaSeconds &&
		a.WaitingReconnect == b.WaitingReconnect &&
		a.HasDuration == b.HasDuration &&
		a.DurationMs == b.DurationMs &&
		a.FailureKind == b.FailureKind &&
		a.FailureDetail == b.FailureDetail &&
		a.FailureText == b.FailureText
}

// isSharedFileMachineOnline mirrors screens/session.go's machineOnlineReady check.
func (c *Core) isSharedFileMachineOnline(mac *lists.Machine) bool {
	if mac == nil {
		return false
	}
	st := c.m.state
	computerOnline := lists.MachineReachable(mac) || (st.DisplayGrace && mac.Online)
	unlinked := !st.DisplayGrace && (lists.WorldUnlinked(&st.World) || lists.MachineUnlinked(mac, &st.World))
	protoMismatch := lists.ProtocolMismatchDirection(mac, st.World.ClientVersion) != 0
	return (st.World.Connection == lists.ConnConnected || st.DisplayGrace) &&
		computerOnline && !unlinked && !protoMismatch && !st.World.AccountControlLost && !st.Removed
}

func (c *Core) isSessionMachineOnline(sid, fallbackMachineID string) bool {
	mid := fallbackMachineID
	if sess := c.m.sync.SessionEntry(sid); sess != nil && sess.MachineID != "" {
		mid = sess.MachineID
	}
	if mid == "" {
		return false
	}
	return c.isSharedFileMachineOnline(c.m.findMachine(mid))
}

func (c *Core) isSessionExitedByID(sid string) bool {
	if sess := c.m.sync.SessionEntry(sid); sess != nil {
		return lists.Exited(&sess.Session)
	}
	return false
}

func (c *Core) findSharedFileInSession(sid, fid string) *v1.SharedFile {
	if sid == "" || fid == "" {
		return nil
	}
	snap := c.m.sync.SessionSnapshot(sid)
	var found *v1.SharedFile
	for _, ev := range snap.Events {
		if ev == nil {
			continue
		}
		tc := ev.GetToolCall()
		if tc == nil || len(tc.SharedFiles) == 0 {
			continue
		}
		for _, sf := range tc.SharedFiles {
			if sf != nil && sf.FileId == fid {
				found = sf
			}
		}
	}
	return found
}

func (c *Core) dispatchSharedFileEvent(ctx *sync.StepContext, ev *corev1.Event) []*corev1.Effect {
	switch k := ev.Event.(type) {
	case *corev1.Event_SharedFileConfig:
		return c.handleSharedFileConfig(k.SharedFileConfig)
	case *corev1.Event_SharedFileLocalScanned:
		return c.handleSharedFileLocalScanned(k.SharedFileLocalScanned)
	case *corev1.Event_SharedFileStartDownload:
		return c.handleSharedFileStartDownload(ctx, k.SharedFileStartDownload)
	case *corev1.Event_SharedFilePauseDownload:
		return c.handleSharedFilePauseDownload(ctx, k.SharedFilePauseDownload)
	case *corev1.Event_SharedFileCancelDownload:
		return c.handleSharedFileCancelDownload(ctx, k.SharedFileCancelDownload)
	case *corev1.Event_SharedFileDeleteLocal:
		return c.handleSharedFileDeleteLocal(ctx, k.SharedFileDeleteLocal)
	case *corev1.Event_SharedFileSetVisibleSession:
		c.handleSharedFileSetVisibleSession(k.SharedFileSetVisibleSession)
		return nil
	case *corev1.Event_SharedFileForgetSession:
		return c.handleSharedFileForgetSession(ctx, k.SharedFileForgetSession)
	case *corev1.Event_SharedFileClearAll:
		return c.handleSharedFileClearAll(ctx)
	case *corev1.Event_SharedFileDurationProbed:
		c.handleSharedFileDurationProbed(k.SharedFileDurationProbed)
		return nil
	case *corev1.Event_SharedFileLocalMissing:
		c.handleSharedFileLocalMissing(ctx, k.SharedFileLocalMissing)
		return nil
	case *corev1.Event_SharedFileDiskInspected:
		return c.handleSharedFileDiskInspected(ctx, k.SharedFileDiskInspected)
	case *corev1.Event_SharedFileWriteChunkResult:
		return c.handleSharedFileWriteChunkResult(ctx, k.SharedFileWriteChunkResult)
	case *corev1.Event_SharedFileVerifyAndCommitResult:
		return c.handleSharedFileVerifyAndCommitResult(k.SharedFileVerifyAndCommitResult)
	}
	return nil
}

func (c *Core) handleSharedFileConfig(ev *corev1.SharedFileConfigEvent) []*corev1.Effect {
	if ev == nil {
		return nil
	}
	mgr := c.m.ensureSharedFiles()
	if ev.LocalCacheMaxBytes > 0 {
		mgr.localCacheMaxBytes = ev.LocalCacheMaxBytes
	} else {
		mgr.localCacheMaxBytes = 0
	}
	mgr.storageFullKind = sharedfiles.EffectiveStorageFullKind(ev.StorageFullKind)
	if !ev.ScanLocal {
		return nil
	}
	opID := mgr.allocOpID()
	mgr.activeScanOpID = opID
	mgr.scanBaselineKeys = make(map[sfKey]bool, len(mgr.entries))
	for k, e := range mgr.entries {
		if e != nil && e.status != sharedfiles.TransferStatusUnspecified {
			mgr.scanBaselineKeys[k] = true
		}
	}
	mgr.scanMutatedKeys = make(map[sfKey]bool)
	mgr.scanForgottenSessions = make(map[string]bool)
	return []*corev1.Effect{
		{
			Effect: &corev1.Effect_SharedFileScanLocal{
				SharedFileScanLocal: &corev1.SharedFileScanLocalEffect{OpId: opID},
			},
		},
	}
}

func (c *Core) handleSharedFileLocalScanned(ev *corev1.SharedFileLocalScannedEvent) []*corev1.Effect {
	if ev == nil {
		return nil
	}
	mgr := c.m.ensureSharedFiles()
	if ev.OpId == 0 || mgr.activeScanOpID == 0 || ev.OpId != mgr.activeScanOpID {
		return nil
	}
	mgr.activeScanOpID = 0
	baseline := mgr.scanBaselineKeys
	mutated := mgr.scanMutatedKeys
	forgotten := mgr.scanForgottenSessions
	mgr.scanBaselineKeys = nil
	mgr.scanMutatedKeys = nil
	mgr.scanForgottenSessions = nil
	if ev.Error != "" {
		return nil
	}

	// Preserve already-probed media durations, active downloads, and any entry created or
	// mutated while the scan was in flight. Only clear pre-scan entries missing from disk.
	savedDurations := make(map[sfKey]int64)
	for k, e := range mgr.entries {
		if e == nil {
			continue
		}
		if e.hasDuration {
			savedDurations[k] = e.durationMs
		}
		if baseline[k] && !mutated[k] &&
			e.status != sharedfiles.TransferStatusDownloading {
			delete(mgr.entries, k)
			c.clearSharedFileUI(k)
		}
	}

	var effects []*corev1.Effect
	for _, le := range ev.Entries {
		if le == nil || le.SessionId == "" || le.FileId == "" {
			continue
		}
		key := sfKey{sessionID: le.SessionId, fileID: le.FileId}
		if forgotten[key.sessionID] || mutated[key] {
			continue
		}
		if existing := mgr.entries[key]; existing != nil && existing.status == sharedfiles.TransferStatusDownloading {
			continue
		}
		if le.Completed {
			if le.BytesOnDisk < 0 || (le.ExpectedSizeBytes > 0 && le.BytesOnDisk != le.ExpectedSizeBytes) {
				effects = append(effects, makeDeleteLocalEffect(key))
				continue
			}
			if sf := c.findSharedFileInSession(le.SessionId, le.FileId); sf != nil && sf.SizeBytes != le.BytesOnDisk {
				effects = append(effects, makeDeleteLocalEffect(key))
				continue
			}
			entry := &sfEntry{
				key:             key,
				status:          sharedfiles.TransferStatusDownloaded,
				phase:           sfPhaseIdle,
				sizeBytes:       le.BytesOnDisk,
				downloadedBytes: le.BytesOnDisk,
				committedOffset: le.BytesOnDisk,
				bytesOnDisk:     le.BytesOnDisk,
				hasDuration:     le.HasDuration,
				durationMs:      le.DurationMs,
			}
			if !entry.hasDuration {
				if dur, ok := savedDurations[key]; ok {
					entry.hasDuration = true
					entry.durationMs = dur
				}
			}
			mgr.entries[key] = entry
			c.syncSharedFileUI(entry)
			continue
		}

		pm := sharedfiles.PartialMeta{
			ExpectedSizeBytes: le.ExpectedSizeBytes,
			ChunkBytes:        le.ChunkBytes,
			CommittedOffset:   le.CommittedOffset,
			BytesOnDisk:       le.BytesOnDisk,
			Sha256:            slices.Clone(le.Sha256),
		}
		if !sharedfiles.IsPartialWellFormed(pm) {
			effects = append(effects, makeDeletePartialEffect(key))
			continue
		}
		effBytes := sharedfiles.EffectivePartialBytes(pm)
		entry := &sfEntry{
			key:             key,
			status:          sharedfiles.TransferStatusPaused,
			phase:           sfPhaseIdle,
			sizeBytes:       pm.ExpectedSizeBytes,
			chunkBytes:      pm.ChunkBytes,
			sha256:          slices.Clone(pm.Sha256),
			committedOffset: effBytes,
			downloadedBytes: effBytes,
			hasPartial:      true,
			partial:         pm,
		}
		mgr.entries[key] = entry
		c.syncSharedFileUI(entry)
	}
	return effects
}

func (c *Core) handleSharedFileStartDownload(ctx *sync.StepContext, act *corev1.SharedFileStartDownloadAction) []*corev1.Effect {
	if act == nil || act.SessionId == "" || act.FileId == "" {
		return nil
	}
	sid := act.SessionId
	fid := act.FileId
	file := c.findSharedFileInSession(sid, fid)
	if file == nil {
		return nil
	}
	mgr := c.m.ensureSharedFiles()
	key := sfKey{sessionID: sid, fileID: fid}
	entry := mgr.entries[key]
	if entry != nil {
		switch entry.status {
		case sharedfiles.TransferStatusDownloading,
			sharedfiles.TransferStatusCleanedUp:
			return nil
		case sharedfiles.TransferStatusDownloaded:
			if entry.bytesOnDisk == file.SizeBytes {
				return nil
			}
			// Size drifted from authoritative file metadata; discard stale state and re-download.
			entry.status = sharedfiles.TransferStatusUnspecified
			entry.bytesOnDisk = 0
		}
	}

	if c.isSessionExitedByID(sid) {
		var effects []*corev1.Effect
		if entry != nil && entry.hasPartial {
			effects = append(effects, makeDeletePartialEffect(key))
		}
		if entry == nil {
			entry = &sfEntry{key: key}
			mgr.entries[key] = entry
		}
		c.markEntryCleanedUp(ctx, mgr, entry)
		return effects
	}

	sess := c.m.sync.SessionEntry(sid)
	if sess == nil || sess.MachineID == "" {
		return nil
	}
	mac := c.m.findMachine(sess.MachineID)
	if mac == nil || mac.Meta == nil || mac.Meta.Capabilities == nil || !mac.Meta.Capabilities.SharedFiles {
		return nil
	}
	if !c.isSharedFileMachineOnline(mac) {
		return nil
	}

	if entry == nil {
		entry = &sfEntry{key: key}
		mgr.entries[key] = entry
	}

	// Fail closed when SHA-256 is missing or malformed (not 32 bytes) or size is negative.
	if file.SizeBytes < 0 || !sharedfiles.HasValidSHA256(file.Sha256) {
		var effects []*corev1.Effect
		if entry.hasPartial {
			effects = append(effects, makeDeletePartialEffect(key))
		}
		c.failEntryChecksum(ctx, mgr, entry)
		return effects
	}

	chunkBytes := sharedfiles.EffectiveChunkBytes(mac.Meta.Capabilities.SharedFileChunkBytes)
	displayName := sharedfiles.SanitizeFileName(file.Name)
	cls := sharedfiles.Classify(displayName)

	var effects []*corev1.Effect
	var resumeOff int64
	var readyToVerify bool
	if entry.hasPartial {
		if off, ready, ok := sharedfiles.CanResumePartial(entry.partial, file, chunkBytes); ok {
			resumeOff = off
			readyToVerify = ready
		} else {
			effects = append(effects, makeDeletePartialEffect(key))
			entry.hasPartial = false
			entry.partial = sharedfiles.PartialMeta{}
		}
	}

	mgr.markScanMutated(key)
	entry.machineID = sess.MachineID
	entry.displayName = displayName
	entry.kind = cls.Kind
	entry.hasMediaDuration = cls.HasMediaDuration
	entry.sizeBytes = file.SizeBytes
	entry.sha256 = slices.Clone(file.Sha256)
	entry.chunkBytes = chunkBytes
	entry.status = sharedfiles.TransferStatusDownloading
	entry.committedOffset = resumeOff
	entry.downloadedBytes = resumeOff
	entry.startedMonoMs = c.m.sync.MonoMs()
	entry.sessionBytes = 0
	entry.hasEta = false
	entry.etaSeconds = 0
	entry.retriedOnce = false
	entry.waitingReconnect = false
	entry.reconnectDeadlineMonoMs = 0
	entry.reconnectNotBeforeMs = 0
	entry.failureKind = corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_UNSPECIFIED
	entry.failureDetail = ""
	entry.failureText = nil
	entry.autoOpen = act.AutoOpenIfStaying && mgr.visibleSessionID == sid
	entry.deliverOnly = mgr.localCacheMaxBytes > 0 && file.SizeBytes > mgr.localCacheMaxBytes
	entry.pausedPendingWriteOpID = 0

	if readyToVerify {
		if eff := c.emitVerifyAndCommit(mgr, entry); eff != nil {
			effects = append(effects, eff)
		}
	} else if remaining := file.SizeBytes - resumeOff; remaining > 0 {
		opID := mgr.allocOpID()
		entry.phase = sfPhaseInspectingDisk
		entry.activeOpID = opID
		effects = append(effects, &corev1.Effect{
			Effect: &corev1.Effect_SharedFileInspectDisk{
				SharedFileInspectDisk: &corev1.SharedFileInspectDiskEffect{
					OpId:          opID,
					SessionId:     sid,
					FileId:        fid,
					RequiredBytes: remaining,
				},
			},
		})
	} else {
		// 0-byte file requires no disk-space preflight; fetch the single padded block at offset 0.
		c.submitNextSharedFileChunk(ctx, mgr, entry)
	}

	c.syncSharedFileUI(entry)
	return effects
}

func (c *Core) handleSharedFileDiskInspected(ctx *sync.StepContext, ev *corev1.SharedFileDiskInspectedEvent) []*corev1.Effect {
	if ev == nil || ev.SessionId == "" || ev.FileId == "" {
		return nil
	}
	mgr := c.m.sharedFiles
	if mgr == nil {
		return nil
	}
	key := sfKey{sessionID: ev.SessionId, fileID: ev.FileId}
	entry := mgr.entries[key]
	if entry == nil || entry.status != sharedfiles.TransferStatusDownloading ||
		entry.phase != sfPhaseInspectingDisk || entry.activeOpID != ev.OpId {
		return nil
	}
	entry.activeOpID = 0

	if ev.ErrorKind != corev1.SharedFileIOErrorKind_SHARED_FILE_IO_ERROR_KIND_UNSPECIFIED || strings.TrimSpace(ev.ErrorDetail) != "" {
		fKind, fDetail := sharedfiles.ClassifyIOFailure(ev.ErrorKind, ev.ErrorDetail, mgr.storageFullKind)
		c.failEntryPreservingPartial(ctx, mgr, entry, fKind, fDetail, nil)
		return nil
	}
	remaining := entry.sizeBytes - entry.committedOffset
	if ev.Known && remaining > 0 && ev.AvailableBytes < remaining {
		c.failEntryPreservingPartial(ctx, mgr, entry, sharedfiles.EffectiveStorageFullKind(mgr.storageFullKind), "", nil)
		return nil
	}
	if c.isSessionExitedByID(key.sessionID) {
		var effects []*corev1.Effect
		if entry.hasPartial {
			effects = append(effects, makeDeletePartialEffect(key))
		}
		c.markEntryCleanedUp(ctx, mgr, entry)
		return effects
	}
	c.submitNextSharedFileChunk(ctx, mgr, entry)
	return nil
}

func (c *Core) submitNextSharedFileChunk(ctx *sync.StepContext, mgr *sfManager, entry *sfEntry) {
	offset := entry.committedOffset
	entry.phase = sfPhaseFetchingChunk
	entry.activeChunkOffset = offset
	entry.waitingReconnect = false

	reqID, _ := c.m.sync.SubmitCommand(ctx, sync.SubmitCommandParams{
		SlotKey:     "",
		MachineID:   entry.machineID,
		SessionID:   entry.key.sessionID,
		BulkFileKey: entry.key.sessionID + "/" + entry.key.fileID,
		BulkBytes:   int64(entry.chunkBytes),
		BulkUpload:  false,
		Command: &v1.Command{
			Kind: &v1.Command_GetSharedFileChunk{
				GetSharedFileChunk: &v1.GetSharedFileChunkRequest{
					SessionId: entry.key.sessionID,
					FileId:    entry.key.fileID,
					Offset:    offset,
				},
			},
		},
	})
	entry.activeReqID = reqID
	if reqID != "" {
		mgr.reqToKey[reqID] = entry.key
	}
}

func (c *Core) settleSharedFileChunk(
	sc sync.SettledCommand,
	effects *[]*corev1.Effect,
	dirty *sync.DirtyFlags,
) bool {
	mgr := c.m.sharedFiles
	if mgr == nil || sc.RequestID == "" {
		return false
	}
	key, ok := mgr.reqToKey[sc.RequestID]
	if !ok {
		return false
	}
	delete(mgr.reqToKey, sc.RequestID)

	entry := mgr.entries[key]
	if entry == nil ||
		entry.status != sharedfiles.TransferStatusDownloading ||
		entry.phase != sfPhaseFetchingChunk ||
		entry.activeReqID != sc.RequestID {
		return true
	}
	entry.activeReqID = ""

	stepPrev, ctx := c.m.sync.BeginStep()
	var stepEffects []*corev1.Effect

	if sc.Category == outcome.Executed && sc.ErrorCode == "" && sc.Result != nil && sc.Result.GetGetSharedFileChunk() != nil {
		resp := sc.Result.GetGetSharedFileChunk()
		validData, nextCommit, valid := sharedfiles.ValidateChunk(resp, entry.sizeBytes, entry.chunkBytes, entry.activeChunkOffset)
		if !valid {
			stepEffects = append(stepEffects, makeDeletePartialEffect(key))
			c.failEntryChecksum(ctx, mgr, entry)
		} else {
			opID := mgr.allocOpID()
			entry.phase = sfPhaseWritingChunk
			entry.activeOpID = opID
			entry.pendingCommittedOffset = nextCommit
			entry.pendingValidLen = int64(len(validData))
			stepEffects = append(stepEffects, &corev1.Effect{
				Effect: &corev1.Effect_SharedFileWriteChunk{
					SharedFileWriteChunk: &corev1.SharedFileWriteChunkEffect{
						OpId:            opID,
						SessionId:       key.sessionID,
						FileId:          key.fileID,
						DisplayName:     entry.displayName,
						Offset:          entry.activeChunkOffset,
						Data:            validData,
						TotalSizeBytes:  entry.sizeBytes,
						ChunkBytes:      entry.chunkBytes,
						CommittedOffset: nextCommit,
						ExpectedSha256:  slices.Clone(entry.sha256),
						ResetPartial:    entry.activeChunkOffset == 0,
					},
				},
			})
		}
	} else if sc.FromDaemon && sc.ErrorCode == "not_found" {
		stepEffects = append(stepEffects, makeDeletePartialEffect(key))
		c.markEntryCleanedUp(ctx, mgr, entry)
	} else if !sc.FromDaemon && sharedfiles.IsTransientDisconnectCode(sc.ErrorCode) {
		if !entry.retriedOnce {
			entry.retriedOnce = true
			entry.phase = sfPhaseWaitingReconnect
			entry.waitingReconnect = true
			entry.hasEta = false
			entry.etaSeconds = 0
			nowMono := c.m.sync.MonoMs()
			entry.reconnectDeadlineMonoMs = nowMono + sharedfiles.ReconnectWaitMs
			delay := sharedfiles.ReconnectWaitMs
			if c.isSessionMachineOnline(key.sessionID, entry.machineID) {
				entry.reconnectNotBeforeMs = nowMono + sharedfiles.ReconnectOnlineRetryDelayMs
				delay = sharedfiles.ReconnectOnlineRetryDelayMs
			} else {
				entry.reconnectNotBeforeMs = nowMono
			}
			c.m.sync.ScheduleAppTimer(ctx, corev1.TimerKind_TIMER_KIND_SHARED_FILE_RECONNECT, key.timerKey(), delay)
			c.syncSharedFileUI(entry)
		} else {
			c.failEntryPreservingPartial(ctx, mgr, entry, corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_DISCONNECTED, "", nil)
		}
	} else {
		var detail string
		var errText *corev1.Text
		if sc.FromDaemon {
			if sc.Result != nil {
				detail = strings.TrimSpace(sc.Result.ErrorMessage)
				if detail == "" {
					detail = strings.TrimSpace(sc.Result.ErrorCode)
				}
			}
		} else {
			if sc.ErrorText != nil {
				errText = sc.ErrorText
			} else {
				detail = strings.TrimSpace(sc.ErrorCode)
			}
		}
		c.failEntryPreservingPartial(ctx, mgr, entry, corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_OTHER, detail, errText)
	}

	stepRes := c.m.sync.EndStep(stepPrev, ctx)
	*effects = append(*effects, stepRes.Effects...)
	*effects = append(*effects, stepEffects...)
	dirty.Add(stepRes.Dirty.Home, stepRes.Dirty.Project, stepRes.Dirty.NewSession)
	if stepRes.Dirty.SessionData {
		dirty.MarkSessionData()
	}
	for _, nested := range stepRes.Settled {
		c.settleSharedFileChunk(nested, effects, dirty)
	}
	return true
}

func (c *Core) handleSharedFileWriteChunkResult(ctx *sync.StepContext, ev *corev1.SharedFileWriteChunkResultEvent) []*corev1.Effect {
	if ev == nil || ev.SessionId == "" || ev.FileId == "" {
		return nil
	}
	mgr := c.m.sharedFiles
	if mgr == nil {
		return nil
	}
	key := sfKey{sessionID: ev.SessionId, fileID: ev.FileId}
	entry := mgr.entries[key]
	if entry == nil {
		return nil
	}

	// If user paused while this exact chunk write was already executing on disk,
	// absorb a successful write so Core's committedOffset matches disk, while staying PAUSED.
	if entry.pausedPendingWriteOpID != 0 && entry.pausedPendingWriteOpID == ev.OpId {
		entry.pausedPendingWriteOpID = 0
		if ev.Ok && entry.status == sharedfiles.TransferStatusPaused {
			mgr.markScanMutated(key)
			entry.committedOffset = entry.pausedPendingCommitOff
			entry.downloadedBytes = min(entry.committedOffset, entry.sizeBytes)
			if entry.sizeBytes > 0 && entry.committedOffset > 0 {
				entry.hasPartial = true
				entry.partial = entry.pausedPendingPartial
			}
			c.syncSharedFileUI(entry)
		}
		return nil
	}

	if entry.status != sharedfiles.TransferStatusDownloading ||
		entry.phase != sfPhaseWritingChunk || entry.activeOpID != ev.OpId {
		return nil
	}
	entry.activeOpID = 0

	if !ev.Ok {
		fKind, fDetail := sharedfiles.ClassifyIOFailure(ev.ErrorKind, ev.ErrorDetail, mgr.storageFullKind)
		if fKind == corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_CHECKSUM_MISMATCH {
			c.failEntryChecksum(ctx, mgr, entry)
			return []*corev1.Effect{makeDeletePartialEffect(key)}
		}
		c.failEntryPreservingPartial(ctx, mgr, entry, fKind, fDetail, nil)
		return nil
	}

	entry.committedOffset = entry.pendingCommittedOffset
	entry.downloadedBytes = min(entry.committedOffset, entry.sizeBytes)
	entry.sessionBytes += entry.pendingValidLen
	entry.waitingReconnect = false
	if entry.sizeBytes > 0 && entry.committedOffset > 0 {
		entry.hasPartial = true
		entry.partial = sharedfiles.PartialMeta{
			ExpectedSizeBytes: entry.sizeBytes,
			ChunkBytes:        entry.chunkBytes,
			CommittedOffset:   entry.committedOffset,
			BytesOnDisk:       entry.downloadedBytes,
			Sha256:            slices.Clone(entry.sha256),
		}
	}

	if entry.sizeBytes > 0 && entry.committedOffset < entry.sizeBytes {
		if c.isSessionExitedByID(key.sessionID) {
			c.markEntryCleanedUp(ctx, mgr, entry)
			return []*corev1.Effect{makeDeletePartialEffect(key)}
		}
		elapsedMs := c.m.sync.MonoMs() - entry.startedMonoMs
		if eta, ok := sharedfiles.ComputeEtaSeconds(entry.sessionBytes, entry.sizeBytes-entry.downloadedBytes, elapsedMs); ok {
			entry.hasEta = true
			entry.etaSeconds = eta
		} else {
			entry.hasEta = false
			entry.etaSeconds = 0
		}
		c.submitNextSharedFileChunk(ctx, mgr, entry)
		c.syncSharedFileUI(entry)
		return nil
	}

	// All blocks received and written: transition to SHA-256 verification and commit/delivery.
	entry.hasEta = false
	entry.etaSeconds = 0
	eff := c.emitVerifyAndCommit(mgr, entry)
	c.syncSharedFileUI(entry)
	if eff == nil {
		return nil
	}
	return []*corev1.Effect{eff}
}

func (c *Core) emitVerifyAndCommit(mgr *sfManager, entry *sfEntry) *corev1.Effect {
	if mgr.inFlightVerifyKey[entry.key] != 0 {
		// Serialize verification per key: wait for the in-flight verify op to settle first.
		entry.phase = sfPhaseWaitingPriorVerify
		entry.activeOpID = 0
		return nil
	}
	opID := mgr.allocOpID()
	entry.phase = sfPhaseVerifying
	entry.activeOpID = opID
	mgr.inFlightVerifyKey[entry.key] = opID
	return &corev1.Effect{
		Effect: &corev1.Effect_SharedFileVerifyAndCommit{
			SharedFileVerifyAndCommit: &corev1.SharedFileVerifyAndCommitEffect{
				OpId:           opID,
				SessionId:      entry.key.sessionID,
				FileId:         entry.key.fileID,
				DisplayName:    entry.displayName,
				TotalSizeBytes: entry.sizeBytes,
				ExpectedSha256: slices.Clone(entry.sha256),
				DeliverOnly:    entry.deliverOnly,
				ProbeDuration:  entry.hasMediaDuration && !entry.deliverOnly,
			},
		},
	}
}

func shouldCleanupStaleVerify(entry *sfEntry) bool {
	if entry == nil {
		return true
	}
	switch entry.status {
	case sharedfiles.TransferStatusUnspecified,
		sharedfiles.TransferStatusCleanedUp:
		return true
	case sharedfiles.TransferStatusFailed:
		return !entry.hasPartial
	default:
		return false
	}
}

func (c *Core) handleSharedFileVerifyAndCommitResult(ev *corev1.SharedFileVerifyAndCommitResultEvent) []*corev1.Effect {
	if ev == nil || ev.SessionId == "" || ev.FileId == "" {
		return nil
	}
	mgr := c.m.sharedFiles
	if mgr == nil {
		return nil
	}
	key := sfKey{sessionID: ev.SessionId, fileID: ev.FileId}

	// If a cancel/delete occurred while VerifyAndCommit was in flight, settle the per-key
	// serialization slot first, then either start a waiting verification round or clean up
	// an orphaned committed copy only when no active/partial/completed entry owns this key.
	if stale, isStale := mgr.staleVerifyOps[ev.OpId]; isStale {
		delete(mgr.staleVerifyOps, ev.OpId)
		if mgr.inFlightVerifyKey[stale.key] == ev.OpId {
			delete(mgr.inFlightVerifyKey, stale.key)
		}
		cur := mgr.entries[stale.key]
		if cur != nil &&
			cur.status == sharedfiles.TransferStatusDownloading &&
			cur.phase == sfPhaseWaitingPriorVerify {
			if eff := c.emitVerifyAndCommit(mgr, cur); eff != nil {
				return []*corev1.Effect{eff}
			}
			return nil
		}
		if ev.Ok && !stale.deliverOnly && shouldCleanupStaleVerify(cur) {
			return []*corev1.Effect{makeDeleteLocalEffect(stale.key)}
		}
		return nil
	}

	entry := mgr.entries[key]
	if entry == nil || entry.status != sharedfiles.TransferStatusDownloading ||
		entry.phase != sfPhaseVerifying || entry.activeOpID != ev.OpId {
		return nil
	}
	entry.activeOpID = 0
	if mgr.inFlightVerifyKey[key] == ev.OpId {
		delete(mgr.inFlightVerifyKey, key)
	}

	if !ev.Ok {
		fKind, fDetail := sharedfiles.ClassifyIOFailure(ev.ErrorKind, ev.ErrorDetail, mgr.storageFullKind)
		if fKind == corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_CHECKSUM_MISMATCH {
			c.failEntryChecksum(nil, mgr, entry)
			return []*corev1.Effect{makeDeletePartialEffect(key)}
		}
		c.failEntryPreservingPartial(nil, mgr, entry, fKind, fDetail, nil)
		return nil
	}

	autoOpen := entry.autoOpen && mgr.visibleSessionID == key.sessionID
	entry.autoOpen = false
	deliverOnly := entry.deliverOnly
	displayName := entry.displayName
	kind := entry.kind
	sizeBytes := entry.sizeBytes

	if deliverOnly {
		// > local_cache_max_bytes (e.g. Web > 50 MiB): delivered directly without local cache; return to AVAILABLE.
		mgr.markScanMutated(key)
		delete(mgr.entries, key)
		c.clearSharedFileUI(key)
		return []*corev1.Effect{
			{
				Effect: &corev1.Effect_SharedFileCompleted{
					SharedFileCompleted: &corev1.SharedFileCompletedEffect{
						SessionId:   key.sessionID,
						FileId:      key.fileID,
						DisplayName: displayName,
						Kind:        kind,
						SizeBytes:   sizeBytes,
						AutoOpen:    false,
						DeliverOnly: true,
					},
				},
			},
		}
	}

	mgr.markScanMutated(key)
	entry.status = sharedfiles.TransferStatusDownloaded
	entry.phase = sfPhaseIdle
	entry.downloadedBytes = sizeBytes
	entry.committedOffset = sizeBytes
	entry.bytesOnDisk = sizeBytes
	entry.hasPartial = false
	entry.partial = sharedfiles.PartialMeta{}
	entry.hasEta = false
	entry.etaSeconds = 0
	entry.waitingReconnect = false
	entry.failureKind = corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_UNSPECIFIED
	entry.failureDetail = ""
	entry.failureText = nil
	if ev.HasDuration && ev.DurationMs > 0 {
		entry.hasDuration = true
		entry.durationMs = ev.DurationMs
	}
	c.syncSharedFileUI(entry)

	return []*corev1.Effect{
		{
			Effect: &corev1.Effect_SharedFileCompleted{
				SharedFileCompleted: &corev1.SharedFileCompletedEffect{
					SessionId:   key.sessionID,
					FileId:      key.fileID,
					DisplayName: displayName,
					Kind:        kind,
					SizeBytes:   sizeBytes,
					AutoOpen:    autoOpen,
					DeliverOnly: false,
				},
			},
		},
	}
}

func (c *Core) handleSharedFilePauseDownload(ctx *sync.StepContext, act *corev1.SharedFilePauseDownloadAction) []*corev1.Effect {
	if act == nil || act.SessionId == "" || act.FileId == "" || c.m.sharedFiles == nil {
		return nil
	}
	mgr := c.m.sharedFiles
	key := sfKey{sessionID: act.SessionId, fileID: act.FileId}
	entry := mgr.entries[key]
	if entry == nil ||
		entry.status != sharedfiles.TransferStatusDownloading ||
		entry.phase == sfPhaseVerifying {
		return nil
	}
	c.pauseEntry(ctx, mgr, entry)
	return nil
}

func (c *Core) pauseEntry(ctx *sync.StepContext, mgr *sfManager, entry *sfEntry) {
	c.cancelActiveTransferWork(ctx, mgr, entry, true)
	mgr.markScanMutated(entry.key)
	entry.status = sharedfiles.TransferStatusPaused
	entry.phase = sfPhaseIdle
	entry.downloadedBytes = min(entry.committedOffset, entry.sizeBytes)
	entry.hasEta = false
	entry.etaSeconds = 0
	entry.waitingReconnect = false
	entry.reconnectDeadlineMonoMs = 0
	entry.reconnectNotBeforeMs = 0
	entry.autoOpen = false
	c.syncSharedFileUI(entry)
}

func (c *Core) handleSharedFileCancelDownload(ctx *sync.StepContext, act *corev1.SharedFileCancelDownloadAction) []*corev1.Effect {
	if act == nil || act.SessionId == "" || act.FileId == "" {
		return nil
	}
	mgr := c.m.ensureSharedFiles()
	key := sfKey{sessionID: act.SessionId, fileID: act.FileId}
	entry := mgr.entries[key]
	if entry != nil && entry.status == sharedfiles.TransferStatusDownloaded {
		return nil
	}
	if entry != nil {
		c.cancelActiveTransferWork(ctx, mgr, entry, false)
		delete(mgr.entries, key)
		c.clearSharedFileUI(key)
	}
	mgr.markScanMutated(key)
	return []*corev1.Effect{makeDeletePartialEffect(key)}
}

func (c *Core) handleSharedFileDeleteLocal(ctx *sync.StepContext, act *corev1.SharedFileDeleteLocalAction) []*corev1.Effect {
	if act == nil || act.SessionId == "" || act.FileId == "" {
		return nil
	}
	mgr := c.m.ensureSharedFiles()
	key := sfKey{sessionID: act.SessionId, fileID: act.FileId}
	if entry := mgr.entries[key]; entry != nil {
		c.cancelActiveTransferWork(ctx, mgr, entry, false)
		delete(mgr.entries, key)
	}
	mgr.markScanMutated(key)
	c.clearSharedFileUI(key)
	return []*corev1.Effect{makeDeleteLocalEffect(key)}
}

func (c *Core) handleSharedFileSetVisibleSession(act *corev1.SharedFileSetVisibleSessionAction) {
	if act == nil {
		return
	}
	mgr := c.m.ensureSharedFiles()
	mgr.visibleSessionID = act.SessionId
	for _, entry := range mgr.entries {
		if entry != nil && entry.autoOpen && (act.SessionId == "" || entry.key.sessionID != act.SessionId) {
			entry.autoOpen = false
		}
	}
}

func (c *Core) handleSharedFileForgetSession(ctx *sync.StepContext, act *corev1.SharedFileForgetSessionAction) []*corev1.Effect {
	if act == nil || act.SessionId == "" {
		return nil
	}
	sid := act.SessionId
	if mgr := c.m.sharedFiles; mgr != nil {
		if mgr.activeScanOpID != 0 {
			if mgr.scanForgottenSessions == nil {
				mgr.scanForgottenSessions = make(map[string]bool)
			}
			mgr.scanForgottenSessions[sid] = true
		}
		for k, entry := range mgr.entries {
			if k.sessionID != sid {
				continue
			}
			c.cancelActiveTransferWork(ctx, mgr, entry, false)
			delete(mgr.entries, k)
		}
	}
	store := c.m.ensureSessionStore()
	if ui := store.uiBySession[sid]; ui != nil && len(ui.SharedFiles) > 0 {
		ui.SharedFiles = make(map[string]*sharedfiles.LocalState)
		ui.SharedFilesRev++
		store.pendingDirty = true
	}
	return []*corev1.Effect{
		{
			Effect: &corev1.Effect_SharedFileDeleteSession{
				SharedFileDeleteSession: &corev1.SharedFileDeleteSessionEffect{SessionId: sid},
			},
		},
	}
}

func (c *Core) handleSharedFileClearAll(ctx *sync.StepContext) []*corev1.Effect {
	if mgr := c.m.sharedFiles; mgr != nil {
		mgr.activeScanOpID = 0
		mgr.scanBaselineKeys = nil
		mgr.scanMutatedKeys = nil
		mgr.scanForgottenSessions = nil
		for k, entry := range mgr.entries {
			c.cancelActiveTransferWork(ctx, mgr, entry, false)
			delete(mgr.entries, k)
		}
		clear(mgr.reqToKey)
	}
	store := c.m.ensureSessionStore()
	for _, ui := range store.uiBySession {
		if ui != nil && len(ui.SharedFiles) > 0 {
			ui.SharedFiles = make(map[string]*sharedfiles.LocalState)
			ui.SharedFilesRev++
			store.pendingDirty = true
		}
	}
	return []*corev1.Effect{
		{
			Effect: &corev1.Effect_SharedFileClearAll{
				SharedFileClearAll: &corev1.SharedFileClearAllEffect{},
			},
		},
	}
}

func (c *Core) handleSharedFileDurationProbed(ev *corev1.SharedFileDurationProbedEvent) {
	if ev == nil || ev.SessionId == "" || ev.FileId == "" || ev.DurationMs <= 0 {
		return
	}
	mgr := c.m.ensureSharedFiles()
	key := sfKey{sessionID: ev.SessionId, fileID: ev.FileId}
	entry := mgr.entries[key]
	if entry == nil {
		entry = &sfEntry{key: key}
		mgr.entries[key] = entry
	}
	if entry.hasDuration && entry.durationMs == ev.DurationMs {
		return
	}
	entry.hasDuration = true
	entry.durationMs = ev.DurationMs
	c.syncSharedFileUI(entry)
}

func (c *Core) handleSharedFileLocalMissing(ctx *sync.StepContext, ev *corev1.SharedFileLocalMissingEvent) {
	if ev == nil || ev.SessionId == "" || ev.FileId == "" || c.m.sharedFiles == nil {
		return
	}
	mgr := c.m.sharedFiles
	key := sfKey{sessionID: ev.SessionId, fileID: ev.FileId}
	entry := mgr.entries[key]
	if entry == nil || entry.status != sharedfiles.TransferStatusDownloaded {
		return
	}
	_ = ctx
	mgr.markScanMutated(key)
	delete(mgr.entries, key)
	c.clearSharedFileUI(key)
}

func (c *Core) handleSharedFileReconnectTimer(ctx *sync.StepContext, timerKey string) []*corev1.Effect {
	mgr := c.m.sharedFiles
	if mgr == nil {
		return nil
	}
	key, ok := parseSFTimerKey(timerKey)
	if !ok {
		return nil
	}
	entry := mgr.entries[key]
	if entry == nil ||
		entry.status != sharedfiles.TransferStatusDownloading ||
		entry.phase != sfPhaseWaitingReconnect {
		return nil
	}
	if c.isSessionExitedByID(key.sessionID) {
		var effects []*corev1.Effect
		if entry.hasPartial {
			effects = append(effects, makeDeletePartialEffect(key))
		}
		c.markEntryCleanedUp(ctx, mgr, entry)
		return effects
	}
	nowMono := c.m.sync.MonoMs()
	if c.isSessionMachineOnline(key.sessionID, entry.machineID) {
		entry.startedMonoMs = nowMono
		entry.sessionBytes = 0
		entry.hasEta = false
		entry.etaSeconds = 0
		entry.waitingReconnect = false
		c.submitNextSharedFileChunk(ctx, mgr, entry)
		c.syncSharedFileUI(entry)
		return nil
	}
	if nowMono < entry.reconnectDeadlineMonoMs {
		c.m.sync.ScheduleAppTimer(ctx, corev1.TimerKind_TIMER_KIND_SHARED_FILE_RECONNECT, key.timerKey(), entry.reconnectDeadlineMonoMs-nowMono)
		return nil
	}
	c.failEntryPreservingPartial(ctx, mgr, entry, corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_DISCONNECTED, "", nil)
	return nil
}

// onSharedFilesSyncEvent reacts to lifecycle and watch events handled inside sync.Engine.
func (c *Core) onSharedFilesSyncEvent(ev *corev1.Event, effects *[]*corev1.Effect) {
	mgr := c.m.sharedFiles
	if mgr == nil {
		return
	}
	switch k := ev.Event.(type) {
	case *corev1.Event_SyncLifecycle:
		if k.SyncLifecycle == nil {
			return
		}
		switch k.SyncLifecycle.Kind {
		case corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_BACKGROUND:
			// Leaving the foreground revokes all pending auto-open tickets while letting transfers finish if OS allows.
			for _, entry := range mgr.entries {
				if entry != nil {
					entry.autoOpen = false
				}
			}
		case corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_STOP:
			// Stopping sync pauses active network/disk downloads (keeping partial blocks on disk for resume)
			// and revokes auto-open tickets, while letting entries already in local VERIFYING finish settling.
			var activeKeys []string
			for key, entry := range mgr.entries {
				if entry == nil {
					continue
				}
				if entry.status == sharedfiles.TransferStatusDownloading &&
					entry.phase != sfPhaseVerifying {
					activeKeys = append(activeKeys, key.timerKey())
				} else {
					entry.autoOpen = false
				}
			}
			if len(activeKeys) == 0 {
				return
			}
			sortutil.Strings(activeKeys)
			stepPrev, ctx := c.m.sync.BeginStep()
			for _, tk := range activeKeys {
				if key, ok := parseSFTimerKey(tk); ok {
					if entry := mgr.entries[key]; entry != nil {
						c.pauseEntry(ctx, mgr, entry)
					}
				}
			}
			stepRes := c.m.sync.EndStep(stepPrev, ctx)
			*effects = append(*effects, stepRes.Effects...)
		}

	case *corev1.Event_SessionWatch:
		if k.SessionWatch == nil || k.SessionWatch.Watched {
			return
		}
		sid := k.SessionWatch.SessionId
		if mgr.visibleSessionID == sid {
			mgr.visibleSessionID = ""
		}
		for key, entry := range mgr.entries {
			if key.sessionID == sid && entry != nil {
				entry.autoOpen = false
			}
		}
	}
}

// reconcileSharedFileDownloads reacts to world state updates (machine reconnection, session exit).
func (c *Core) reconcileSharedFileDownloads(effects *[]*corev1.Effect, dirty *sync.DirtyFlags) {
	mgr := c.m.sharedFiles
	if !mgr.hasTrackedEntries() {
		return
	}

	var ordered []string
	for key, entry := range mgr.entries {
		if entry == nil {
			continue
		}
		if entry.status == sharedfiles.TransferStatusUnspecified ||
			entry.status == sharedfiles.TransferStatusDownloaded ||
			entry.status == sharedfiles.TransferStatusCleanedUp {
			continue
		}
		ordered = append(ordered, key.timerKey())
	}
	if len(ordered) == 0 {
		return
	}
	sortutil.Strings(ordered)

	stepPrev, ctx := c.m.sync.BeginStep()
	var extraEffects []*corev1.Effect
	nowMono := c.m.sync.MonoMs()

	for _, tk := range ordered {
		key, ok := parseSFTimerKey(tk)
		if !ok {
			continue
		}
		entry := mgr.entries[key]
		if entry == nil {
			continue
		}

		// Session exited: let an already-verifying local commit finish; clean up everything else.
		if c.isSessionExitedByID(key.sessionID) {
			if entry.phase == sfPhaseVerifying || entry.phase == sfPhaseWaitingPriorVerify {
				continue
			}
			hadPartial := entry.hasPartial || entry.phase == sfPhaseWritingChunk || entry.pausedPendingWriteOpID != 0
			c.cancelActiveTransferWork(ctx, mgr, entry, false)
			if hadPartial {
				extraEffects = append(extraEffects, makeDeletePartialEffect(key))
			}
			c.markEntryCleanedUp(ctx, mgr, entry)
			continue
		}

		// Wake waiting-reconnect downloads immediately when the computer returns online.
		if entry.status == sharedfiles.TransferStatusDownloading &&
			entry.phase == sfPhaseWaitingReconnect &&
			nowMono >= entry.reconnectNotBeforeMs &&
			nowMono <= entry.reconnectDeadlineMonoMs &&
			c.isSessionMachineOnline(key.sessionID, entry.machineID) {
			c.m.sync.CancelAppTimer(ctx, corev1.TimerKind_TIMER_KIND_SHARED_FILE_RECONNECT, key.timerKey())
			entry.startedMonoMs = nowMono
			entry.sessionBytes = 0
			entry.hasEta = false
			entry.etaSeconds = 0
			entry.waitingReconnect = false
			c.submitNextSharedFileChunk(ctx, mgr, entry)
			c.syncSharedFileUI(entry)
		}
	}

	stepRes := c.m.sync.EndStep(stepPrev, ctx)
	*effects = append(*effects, stepRes.Effects...)
	*effects = append(*effects, extraEffects...)
	dirty.Add(stepRes.Dirty.Home, stepRes.Dirty.Project, stepRes.Dirty.NewSession)
	if stepRes.Dirty.SessionData {
		dirty.MarkSessionData()
	}
	for _, nested := range stepRes.Settled {
		c.settleSharedFileChunk(nested, effects, dirty)
	}
}

func (c *Core) cancelActiveTransferWork(ctx *sync.StepContext, mgr *sfManager, entry *sfEntry, isPause bool) {
	if entry == nil {
		return
	}
	if entry.activeReqID != "" {
		reqID := entry.activeReqID
		entry.activeReqID = ""
		delete(mgr.reqToKey, reqID)
		if ctx != nil {
			c.m.sync.CancelCommand(ctx, reqID)
		}
	}
	if entry.phase == sfPhaseWaitingReconnect && ctx != nil {
		c.m.sync.CancelAppTimer(ctx, corev1.TimerKind_TIMER_KIND_SHARED_FILE_RECONNECT, entry.key.timerKey())
	}
	if entry.phase == sfPhaseWritingChunk && entry.activeOpID != 0 {
		if isPause {
			entry.pausedPendingWriteOpID = entry.activeOpID
			entry.pausedPendingCommitOff = entry.pendingCommittedOffset
			entry.pausedPendingPartial = sharedfiles.PartialMeta{
				ExpectedSizeBytes: entry.sizeBytes,
				ChunkBytes:        entry.chunkBytes,
				CommittedOffset:   entry.pendingCommittedOffset,
				BytesOnDisk:       min(entry.pendingCommittedOffset, entry.sizeBytes),
				Sha256:            slices.Clone(entry.sha256),
			}
		}
	}
	if !isPause {
		entry.pausedPendingWriteOpID = 0
	}
	if entry.phase == sfPhaseVerifying && entry.activeOpID != 0 {
		mgr.staleVerifyOps[entry.activeOpID] = sfStaleVerify{
			key:         entry.key,
			deliverOnly: entry.deliverOnly,
		}
	}
	entry.activeOpID = 0
}

func (c *Core) markEntryCleanedUp(ctx *sync.StepContext, mgr *sfManager, entry *sfEntry) {
	c.cancelActiveTransferWork(ctx, mgr, entry, false)
	mgr.markScanMutated(entry.key)
	entry.status = sharedfiles.TransferStatusCleanedUp
	entry.phase = sfPhaseIdle
	entry.downloadedBytes = 0
	entry.committedOffset = 0
	entry.hasPartial = false
	entry.partial = sharedfiles.PartialMeta{}
	entry.hasEta = false
	entry.etaSeconds = 0
	entry.waitingReconnect = false
	entry.autoOpen = false
	entry.failureKind = corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_UNSPECIFIED
	entry.failureDetail = ""
	entry.failureText = nil
	c.syncSharedFileUI(entry)
}

func (c *Core) failEntryChecksum(ctx *sync.StepContext, mgr *sfManager, entry *sfEntry) {
	c.cancelActiveTransferWork(ctx, mgr, entry, false)
	mgr.markScanMutated(entry.key)
	entry.status = sharedfiles.TransferStatusFailed
	entry.phase = sfPhaseIdle
	entry.downloadedBytes = 0
	entry.committedOffset = 0
	entry.hasPartial = false
	entry.partial = sharedfiles.PartialMeta{}
	entry.hasEta = false
	entry.etaSeconds = 0
	entry.waitingReconnect = false
	entry.autoOpen = false
	entry.failureKind = corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_CHECKSUM_MISMATCH
	entry.failureDetail = ""
	entry.failureText = nil
	c.syncSharedFileUI(entry)
}

func (c *Core) failEntryPreservingPartial(
	ctx *sync.StepContext,
	mgr *sfManager,
	entry *sfEntry,
	kind corev1.SharedFileFailureKind,
	detail string,
	errText *corev1.Text,
) {
	c.cancelActiveTransferWork(ctx, mgr, entry, false)
	mgr.markScanMutated(entry.key)
	entry.status = sharedfiles.TransferStatusFailed
	entry.phase = sfPhaseIdle
	entry.downloadedBytes = min(entry.committedOffset, entry.sizeBytes)
	entry.hasEta = false
	entry.etaSeconds = 0
	entry.waitingReconnect = false
	entry.autoOpen = false
	entry.failureKind = kind
	entry.failureDetail = detail
	entry.failureText = errText
	c.syncSharedFileUI(entry)
}

func makeDeletePartialEffect(key sfKey) *corev1.Effect {
	return &corev1.Effect{
		Effect: &corev1.Effect_SharedFileDeletePartial{
			SharedFileDeletePartial: &corev1.SharedFileDeletePartialEffect{
				SessionId: key.sessionID,
				FileId:    key.fileID,
			},
		},
	}
}

func makeDeleteLocalEffect(key sfKey) *corev1.Effect {
	return &corev1.Effect{
		Effect: &corev1.Effect_SharedFileDeleteLocal{
			SharedFileDeleteLocal: &corev1.SharedFileDeleteLocalEffect{
				SessionId: key.sessionID,
				FileId:    key.fileID,
			},
		},
	}
}
