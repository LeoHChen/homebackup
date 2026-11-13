import AVKit
import HomeBackupKit
import SwiftUI

struct PhotoDetailView: View {
    let file: FileItem
    @State private var image: UIImage?
    @State private var player: AVPlayer?
    @State private var shareURL: URL?
    @State private var loading = false
    @State private var error: String?

    var body: some View {
        ZStack {
            Color.black.ignoresSafeArea()
            if let player {
                VideoPlayer(player: player).onAppear { player.play() }
            } else if let image {
                Image(uiImage: image).resizable().scaledToFit()
            } else {
                ProgressView().tint(.white)
            }
        }
        .navigationTitle(file.modified?.formatted(date: .abbreviated, time: .shortened) ?? file.name)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            if file.isVideo && player == nil {
                Button { Task { await playVideo() } } label: { Image(systemName: "play.circle") }
                    .disabled(loading)
            }
            if let shareURL {
                ShareLink(item: shareURL)
            } else {
                Button { Task { await downloadOriginal() } } label: { Image(systemName: "square.and.arrow.down") }
                    .disabled(loading)
            }
        }
        .overlay(alignment: .bottom) {
            if let error { Text(error).font(.footnote).padding().background(.thinMaterial, in: Capsule()).padding() }
        }
        .task {
            guard let client = AppModel.shared.client else { return }
            image = await ThumbnailCache.shared.image(for: file, size: .large, client: client)
        }
    }

    private func original() async throws -> URL {
        guard let client = AppModel.shared.client else { throw URLError(.userAuthenticationRequired) }
        let tmp = try await client.download(id: file.id)
        let dest = FileManager.default.temporaryDirectory.appendingPathComponent(file.name)
        try? FileManager.default.removeItem(at: dest)
        try FileManager.default.moveItem(at: tmp, to: dest)
        return dest
    }

    private func playVideo() async {
        loading = true
        defer { loading = false }
        do {
            let url = try await original()
            shareURL = url
            player = AVPlayer(url: url)
        } catch { self.error = error.localizedDescription }
    }

    private func downloadOriginal() async {
        loading = true
        defer { loading = false }
        do { shareURL = try await original() } catch { self.error = error.localizedDescription }
    }
}
