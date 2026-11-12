import Foundation
import RAProto
import Testing
@testable import RACore

@Suite struct ImageProblemTests {
    @Test func permanentRefusalsAndRecoverableUploadsHaveSafeExplanations() {
        #expect(imageProblem("原图超过20 MB").message == "图片超过 20 MB，关掉原图再发")
        #expect(!imageProblem("the image does not decode").retryable)
        #expect(imageProblem("unavailable: machine offline").retryable)
        #expect(imageProblem("opaque internal exception").message == "这张图片处理失败")
        #expect(imageFailureLine(["图片尺寸太大", "decode failed"]) == "2 张图没加上，点图片看原因")
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
