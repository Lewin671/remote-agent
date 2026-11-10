import Testing

@testable import RACore

@Suite struct ScheduleHeadlineTests {
    @Test func theSentenceSaysWhatStoppedFirstThenWhatIsNew() {
        #expect(scheduleHeadline(stopped: 3, fresh: 4, visible: 9, loading: false) == ScheduleHeadline(title: "3 个任务停下了", count: 3, sub: "另有 4 个新结果"))
        #expect(scheduleHeadline(stopped: 1, fresh: 0, visible: 2, loading: false).sub.isEmpty)
        #expect(scheduleHeadline(stopped: 0, fresh: 2, visible: 5, loading: false) == ScheduleHeadline(title: "2 个新结果", count: nil, sub: ""))
        #expect(scheduleHeadline(stopped: 0, fresh: 0, visible: 5, loading: false).title == "没有新结果")
        #expect(scheduleHeadline(stopped: 0, fresh: 0, visible: 0, loading: true).title == "正在读取任务…")
        #expect(scheduleHeadline(stopped: 0, fresh: 0, visible: 0, loading: false).title == "还没有定时任务")
    }
}
