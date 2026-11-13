import XCTest
@testable import HomeBackupKit

final class PinningTests: XCTestCase {
    // A certificate made by homebackupd (internal/tlsutil) and its
    // fingerprint as the server computes it.
    static let certDER = Data(base64Encoded: "MIIBpTCCAUugAwIBAgIQMbf/opVgG2cMzutSn0ROmTAKBggqhkjOPQQDAjAYMRYwFAYDVQQDEw1ob21lYmFja3VwIHZtMB4XDTI2MTExMzA0MjgwNVoXDTM2MTExMzA1MjgwNVowGDEWMBQGA1UEAxMNaG9tZWJhY2t1cCB2bTBZMBMGByqGSM49AgEGCCqGSM49AwEHA0IABFCXmPa8eXCgxVBOboOLv588LDpoXGNfwYgK03JwR1W7P2FegqawuC9v2tWwNtOaZgHXnZDxlrlQ/y3XDdsuPMqjdzB1MA4GA1UdDwEB/wQEAwIHgDATBgNVHSUEDDAKBggrBgEFBQcDATAMBgNVHRMBAf8EAjAAMEAGA1UdEQQ5MDeCCWxvY2FsaG9zdIICdm2CCHZtLmxvY2FshwR/AAABhxAAAAAAAAAAAAAAAAAAAAABhwTAAAICMAoGCCqGSM49BAMCA0gAMEUCIFEnP2wWi3z2sNJoLN3Tpg+F788bPJuH6K3iOlZPN7bKAiEA5OdsSuWoz3GttmuS03ytr0loi+ONygx7aKasbL6ZuTA=")!
    static let fingerprint = "6db497cb6f0de2221de57531c7a1732d9b17391080e444cc1d71aafb9a7d1fee"

    func testFingerprintMatchesServer() {
        XCTAssertEqual(Pinning.fingerprint(certificateDER: Self.certDER), Self.fingerprint)
    }

    func testShortFormat() {
        XCTAssertEqual(Pinning.short("28c5ec7d33790ad9ffff"), "28C5 EC7D 3379 0AD9")
    }

    func testGarbage() {
        XCTAssertNil(Pinning.fingerprint(certificateDER: Data([0x30, 0x82, 0xFF])))
        XCTAssertNil(Pinning.fingerprint(certificateDER: Data()))
    }
}
