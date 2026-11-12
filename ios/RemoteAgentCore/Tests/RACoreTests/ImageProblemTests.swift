import Foundation
import RAProto
import Testing
@testable import RACore

@Suite struct ImageProblemTests {
    @Test func permanentRefusalsAndRecoverableUploadsHaveSafeExplanations() {
        #expect(imageProblem("原图超过20 MB").message == L10n.imageImageExceeds20MbTurnOffOriginalAndSend())
        #expect(!imageProblem("the image does not decode").retryable)
        #expect(imageProblem("unavailable: machine offline").retryable)
        #expect(imageProblem("opaque internal exception").message == L10n.imageThisImageCouldNotBeProcessed())
        #expect(imageFailureLine([L10n.imageImageDimensionsAreTooLarge(), "decode failed"]) == "2 张图没加上，点图片看原因")
    }

    @MainActor @Test func uncertainImagesRemainRecoverableWhileTheSourceSurvives() {
        let image = PendingImage(id: "image", uri: nil)
        let message = Outgoing(requestId: "request", text: "text", imageIds: ["uploaded"], images: [image])
        message.state = .unknown
        #expect(!message.canPutBack)
        image.processed = Processed(bytes: Data([1]), format: .png, width: 1, height: 1)
        #expect(message.canPutBack)
        image.release()
        #expect(!message.canPutBack)
    }
}
