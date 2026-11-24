// What the image viewer says while fetching display copies and the names a
// picture leaves the app under. Collection, indexing, source captions, pixel
// dimensions, and display-copy availability live in Go clientcore.
import Foundation
import RAProto

/// Where a display copy stands while the viewer shows an image.
public enum BigImage: Equatable, Sendable {
    case loading
    case ready
    /// The event says there is no display copy.
    case notSynced
    case expired
    /// Never fetched and no connection to fetch it.
    case offline
    case failed(why: String)
    case corrupt(why: String)
    case retiredHistory
}

/// What the viewer writes over the picture for a state; nil while there is nothing to say.
public func viewerNote(_ big: BigImage, sync: PBImageSync) -> String? {
    switch big {
    case .loading, .ready: return nil
    case .notSynced: return sync == .nameOnly ? L10n.imageIosNameOnly() : L10n.imageOriginalOnComputer()
    case .expired: return L10n.imageOriginalExpired()
    case .offline: return L10n.imageLoadConnected()
    case .failed(let why): return L10n.imageIosDownloadFailed(reason: why)
    case .corrupt(let why): return L10n.imageIosVerificationFailed(reason: why)
    case .retiredHistory: return retiredHistoryError
    }
}

/// A tap on the note asks for the display copy again.
public func viewerCanRetry(_ big: BigImage) -> Bool {
    switch big {
    case .failed, .offline: return true
    default: return false
    }
}

/// How fetching a display copy ended, as the viewer's state; the payload when it is there to decode.
public func viewerOutcome(_ result: Result<ImageResult, any Error>, online: Bool) -> (
    state: BigImage, payload: PBImagePayload?
) {
    switch result {
    case .success(.loaded(let payload)): return (.ready, payload)
    case .success(.expired): return (.expired, nil)
    case .success(.missing): return (.notSynced, nil)
    case .success(.retiredHistory): return (.retiredHistory, nil)
    case .success(.corrupt(let why)): return (.corrupt(why: why), nil)
    case .failure(let error): return (online ? .failed(why: errorText(error)) : .offline, nil)
    }
}
