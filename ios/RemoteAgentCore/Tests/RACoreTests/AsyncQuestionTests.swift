import RAProto
import Testing

@testable import RACore

@Suite struct AsyncQuestionTests {
    @Test func asyncQuestionsStayInChatWithAllContentAndNoAnswerCard() {
        let questions: [PBQuestion] = [
            .with {
                $0.header = "测试方式"
                $0.text = "请选择 **测试方式**。"
                $0.options = [
                    .with { $0.label = "A [推荐]"; $0.description_p = "完整测试 *保留原文*" },
                    .with { $0.label = "B" },
                ]
            },
            .with { $0.text = "还有什么要求？" },
        ]
        let events = [
            Mk.ev(1, Mk.agent("开始检查")),
            Mk.ev(2, .questionAsked(.with { $0.questionID = "async"; $0.async = true; $0.questions = questions })),
            // A typed reply and subsequent messages keep the question in ordinary history.
            Mk.ev(3, Mk.user("选择 A")),
            Mk.ev(4, Mk.agent("收到")),
            // Synchronous questions still use the answer dock.
            Mk.ev(5, .questionAsked(.with { $0.questionID = "sync"; $0.questions = questions })),
        ]
        var projection = ChatProjection()
        for n in 1...events.count {
            let prefix = Array(events.prefix(n))
            let d = derive(prefix)
            #expect(d.questions["async"] == nil)
            let rows = buildChat(prefix, d: d, machine: "M")
            let shown = rows.compactMap { item -> ChatItem.AsyncQuestion? in
                if case .asyncQuestion(let q) = item { return q } else { return nil }
            }
            #expect(shown == (n < 2 ? [] : [.init(key: "e2", questions: questions)]))
            #expect(projection.update(prefix, d: d, machine: "M") == rows)
            #expect(rows.map(\.key) == prefix.filter { $0.seq != 5 }.map { "e\($0.seq)" })
        }
        #expect(derive(events).questions.keys == ["sync"])
    }
}
