import Foundation
import XCTest
@testable import RACore

final class SecurityTimeTests: XCTestCase {
    func testChineseCalendarLabelsAcrossYearAndDST() {
        let zone = TimeZone(identifier: "America/Los_Angeles")!
        func date(_ value: String) -> Date { ISO8601DateFormatter().date(from: value)! }
        func ms(_ value: String) -> UInt64 { UInt64(date(value).timeIntervalSince1970 * 1000) }
        let now = date("2026-12-31T20:00:00Z")
        XCTAssertEqual(securityTime(ms("2026-12-31T13:02:59Z"), now: now, timeZone: zone), "今天 05:02")
        XCTAssertEqual(securityTime(ms("2027-01-01T13:02:59Z"), now: now, timeZone: zone), "明天 05:02")
        XCTAssertEqual(securityTime(ms("2027-01-02T13:02:59Z"), now: now, timeZone: zone), "1月2日 05:02")
        XCTAssertEqual(securityTime(ms("2027-01-02T13:02:59Z"), now: now, timeOnly: true, timeZone: zone), "05:02")
        XCTAssertEqual(securityTime(ms("2026-03-08T10:02:00Z"), now: date("2026-03-07T20:00:00Z"), timeZone: zone), "明天 03:02")
        XCTAssertEqual(securityTime(0, now: now, timeZone: zone), "尚未读取")
    }
}
