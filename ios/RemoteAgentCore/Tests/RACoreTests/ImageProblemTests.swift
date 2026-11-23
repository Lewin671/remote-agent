import Foundation
import RAProto
import Testing
@testable import RACore

@Suite(.timeLimit(.minutes(2))) struct ImageProblemTests {
    @Test func coreClassifiesStructuredProblemsAndSummarizesMixedKinds() throws {
        let client = try CoreClient()
        defer { client.close() }
        _ = try client.dispatch(.with { $0.localeChanged.uiLocale = "zh-CN" })

        let req = PBAttachmentEvalScreenRequest.with {
            $0.problems = [
                imageProblemInput(for: ImageTooLarge(L10nMessage.imageIosOriginalExceedsMb(count: 20), kind: .originalTooLarge)),
                imageProblemInput(for: CommandError("generation_conflict", "stale", fromDaemon: true)),
                imageProblemInput(for: CommandError("internal", "upload png: network timeout", fromDaemon: true)),
                imageProblemInput(for: UserError(L10nMessage.imageIosCannotDecodeThisImage(), problemKind: .decode)),
            ]
        }
        #expect(req.problems.allSatisfy { $0.localKey.isEmpty })

        let vm = try client.view(.with { $0.attachmentEval = req }).attachmentEval
        #expect(vm.problems.count == 4)
        #expect(vm.problems[0].kind == .originalTooLarge && !vm.problems[0].retryable)
        #expect(vm.problems[1].kind == .uploadFailed && vm.problems[1].retryable)
        #expect(vm.problems[2].kind == .uploadFailed && vm.problems[2].retryable)
        #expect(vm.problems[3].kind == .decode && !vm.problems[3].retryable)
        #expect(vm.failureSummary.rendered() == L10n.imageAttachFailed(count: 4, locale: "zh-CN"))
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
