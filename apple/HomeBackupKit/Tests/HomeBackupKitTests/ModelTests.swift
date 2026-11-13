import XCTest
@testable import HomeBackupKit

final class ModelTests: XCTestCase {
    func testDecodeGoJSON() throws {
        let json = """
        {"changes":[
          {"seq":7,"file":{"id":"1","source":"iphone","path":"2026/11/a.jpg","name":"a.jpg","isDir":false,
            "size":58030,"modified":"2026-11-13T05:25:40.904465316Z","sha256":"8ed9","media":"photo"}},
          {"seq":8,"removed":true,"file":{"id":"2","source":"iphone","path":"old","name":"old","isDir":true}},
          {"seq":9,"file":{"source":"files","path":"docs","name":"docs","isDir":true,
            "modified":"2026-11-13T05:25:40Z","deletedAt":"2026-11-14T01:02:03.5+08:00"}}
        ],"cursor":"9","more":false}
        """
        let page = try JSON.decoder.decode(ChangesPage.self, from: Data(json.utf8))
        XCTAssertEqual(page.cursor, "9")
        XCTAssertEqual(page.changes.count, 3)
        let a = page.changes[0].file
        XCTAssertTrue(a.isPhoto)
        XCTAssertFalse(page.changes[0].removed)
        XCTAssertEqual(a.modified!.timeIntervalSince1970, 1_794_547_540.904465, accuracy: 0.001)
        XCTAssertTrue(page.changes[1].removed)
        XCTAssertEqual(page.changes[2].file.id, "")
        XCTAssertEqual(page.changes[2].file.deletedAt!.timeIntervalSince1970, 1_794_589_323.5, accuracy: 0.001)
    }

    func testParseDate() {
        XCTAssertNotNil(JSON.parseDate("2026-11-13T05:25:40Z"))
        XCTAssertNotNil(JSON.parseDate("2026-11-13T05:25:40.123-07:00"))
        XCTAssertNil(JSON.parseDate("yesterday"))
    }
}
