import HomeBackupKit
import SwiftUI

/// All photos and videos on the server, newest first.
struct PhotosView: View {
    @State private var index = PhotoIndex.shared
    @State private var backup = BackupManager.shared
    private let columns = [GridItem(.adaptive(minimum: 96), spacing: 2)]

    var body: some View {
        NavigationStack {
            ScrollView {
                if backup.running {
                    BackupBanner(backup: backup).padding(.horizontal)
                }
                LazyVGrid(columns: columns, spacing: 2) {
                    ForEach(index.items) { item in
                        NavigationLink(value: item) {
                            Thumbnail(file: item)
                                .aspectRatio(1, contentMode: .fill)
                                .clipped()
                        }
                    }
                }
                if index.items.isEmpty && !index.isRefreshing {
                    ContentUnavailableView("No photos yet", systemImage: "photo",
                                           description: Text("Turn on backup in Settings, or import Google Photos with Takeout on the server."))
                        .padding(.top, 80)
                }
            }
            .navigationTitle("Photos")
            .navigationDestination(for: FileItem.self) { PhotoDetailView(file: $0) }
            .refreshable { await index.refresh() }
            .task { await index.refresh() }
            .overlay(alignment: .bottom) {
                if let err = index.lastError {
                    Text(err).font(.footnote).padding(8).background(.thinMaterial, in: Capsule()).padding()
                }
            }
        }
    }
}

struct BackupBanner: View {
    let backup: BackupManager

    var body: some View {
        HStack {
            ProgressView(value: Double(backup.done), total: Double(max(backup.total, 1)))
            Text("Backing up · \(backup.remaining) left").font(.footnote).foregroundStyle(.secondary)
        }
        .padding(.vertical, 6)
    }
}

/// A square thumbnail loaded from the cache or the server.
struct Thumbnail: View {
    let file: FileItem
    var size: HomeBackupClient.ThumbnailSize = .small
    @State private var image: UIImage?

    var body: some View {
        Color(.secondarySystemBackground)
            .overlay {
                if let image {
                    Image(uiImage: image).resizable().scaledToFill()
                }
            }
            .overlay(alignment: .bottomTrailing) {
                if file.isVideo {
                    Image(systemName: "play.fill").font(.caption).foregroundStyle(.white).padding(4).shadow(radius: 2)
                }
            }
            .task(id: file.sha256) {
                guard let client = AppModel.shared.client else { return }
                image = await ThumbnailCache.shared.image(for: file, size: size, client: client)
            }
    }
}
