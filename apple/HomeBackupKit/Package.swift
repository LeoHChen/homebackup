// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "HomeBackupKit",
    platforms: [.iOS(.v17), .macOS(.v14)],
    products: [
        .library(name: "HomeBackupKit", targets: ["HomeBackupKit"]),
    ],
    targets: [
        .target(name: "HomeBackupKit"),
        .testTarget(name: "HomeBackupKitTests", dependencies: ["HomeBackupKit"]),
    ]
)
