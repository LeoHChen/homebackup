import XCTest
@testable import HomeBackupKit

final class MessagesTests: XCTestCase {
    func testUnpairedHidesRawServerText() {
        let m = UserMessage.describe(APIError(status: 401, code: "unauthorized", message: "missing or invalid device token"))
        XCTAssertTrue(m.lowercased().contains("pair"), m)
        XCTAssertFalse(m.contains("invalid device token"), m)
    }

    func testUnreachableNamesTheServer() {
        let m = UserMessage.describe(URLError(.cannotConnectToHost), server: "macmini.local")
        XCTAssertTrue(m.contains("macmini.local"), m)
        XCTAssertTrue(m.lowercased().contains("reach"), m)
    }

    func testKeyMismatch() {
        let m = UserMessage.describe(URLError(.serverCertificateUntrusted))
        XCTAssertTrue(m.contains("key"), m)
    }

    func testServerMessagePassesThroughForClientErrors() {
        XCTAssertEqual(UserMessage.describe(APIError(status: 400, code: "bad_request", message: "invalid path")), "invalid path")
    }

    func testServerFailureMentionsLog() {
        let m = UserMessage.describe(APIError(status: 500, code: "internal", message: ""))
        XCTAssertTrue(m.contains("error 500"), m)
        XCTAssertTrue(m.contains("log"), m)
    }

    func testFallsBackToDescription() {
        struct Odd: LocalizedError { var errorDescription: String? { "odd thing" } }
        XCTAssertEqual(UserMessage.describe(Odd()), "odd thing")
    }
}
