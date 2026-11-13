import HomeBackupKit
import QuickLook
import SwiftUI

/// Picks a source (My Files, Google Drive backup, ...) and browses it.
struct FilesView: View {
    @State private var model = AppModel.shared

    var body: some View {
        NavigationStack {
            List(model.browsableSources) { src in
                NavigationLink(value: FolderRef(source: src.id, path: "", writable: src.isWritable)) {
                    Label(Self.title(src), systemImage: src.isWritable ? "externaldrive.fill" : "icloud.and.arrow.down")
                }
            }
            .navigationTitle("Files")
            .navigationDestination(for: FolderRef.self) { FolderView(ref: $0) }
            .refreshable { await model.refreshSources() }
        }
    }

    nonisolated static func title(_ s: SourceStatus) -> String { s.displayName }
}

struct FolderRef: Hashable {
    var source: String
    var path: String
    var writable: Bool
}

struct FolderView: View {
    let ref: FolderRef
    @State private var items: [FileItem] = []
    @State private var loading = false
    @State private var error: String?
    @State private var preview: URL?
    @State private var importing = false
    @State private var newFolder = false
    @State private var renaming: FileItem?
    @State private var nameField = ""
    @State private var uploadStatus: String?

    var body: some View {
        List {
            if let uploadStatus { Text(uploadStatus).font(.footnote).foregroundStyle(.secondary) }
            ForEach(items, id: \.path) { item in
                row(item)
                    .swipeActions {
                        if ref.writable && !item.id.isEmpty {
                            Button("Delete", role: .destructive) { Task { await delete(item) } }
                            Button("Rename") { nameField = item.name; renaming = item }.tint(.blue)
                        }
                    }
            }
            if let error { Text(error).foregroundStyle(.red) }
        }
        .overlay { if loading && items.isEmpty { ProgressView() } }
        .navigationTitle(ref.path.isEmpty ? "Files" : (ref.path as NSString).lastPathComponent)
        .refreshable { await load() }
        .task { await load() }
        .quickLookPreview($preview)
        .toolbar {
            if ref.writable {
                Menu {
                    Button { nameField = ""; newFolder = true } label: { Label("New Folder", systemImage: "folder.badge.plus") }
                    Button { importing = true } label: { Label("Upload Files", systemImage: "square.and.arrow.up") }
                } label: { Image(systemName: "plus") }
            }
        }
        .fileImporter(isPresented: $importing, allowedContentTypes: [.item], allowsMultipleSelection: true) { result in
            if case .success(let urls) = result { Task { await upload(urls) } }
        }
        .alert("New Folder", isPresented: $newFolder) {
            TextField("Name", text: $nameField)
            Button("Create") { Task { await mkdir(nameField) } }
            Button("Cancel", role: .cancel) {}
        }
        .alert("Rename", isPresented: Binding(get: { renaming != nil }, set: { if !$0 { renaming = nil } })) {
            TextField("Name", text: $nameField)
            Button("Rename") { if let r = renaming { Task { await rename(r, to: nameField) } } }
            Button("Cancel", role: .cancel) { renaming = nil }
        }
    }

    @ViewBuilder private func row(_ item: FileItem) -> some View {
        if item.isDir {
            NavigationLink(value: FolderRef(source: ref.source, path: item.path, writable: ref.writable)) {
                Label(item.name, systemImage: "folder")
            }
        } else {
            Button { Task { await open(item) } } label: {
                HStack {
                    if item.media != nil {
                        Thumbnail(file: item).frame(width: 36, height: 36).clipShape(RoundedRectangle(cornerRadius: 4))
                    } else {
                        Image(systemName: "doc").frame(width: 36)
                    }
                    VStack(alignment: .leading) {
                        Text(item.name).foregroundStyle(.primary)
                        Text(ByteCountFormatter.string(fromByteCount: item.size, countStyle: .file) +
                             (item.modified.map { " · " + $0.formatted(date: .abbreviated, time: .omitted) } ?? ""))
                            .font(.caption).foregroundStyle(.secondary)
                    }
                }
            }
        }
    }

    private var client: HomeBackupClient? { AppModel.shared.client }

    private func child(_ name: String) -> String {
        ref.path.isEmpty ? name : ref.path + "/" + name
    }

    private func load() async {
        guard let client else { return }
        loading = true
        defer { loading = false }
        do {
            items = try await client.list(source: ref.source, path: ref.path)
            error = nil
        } catch { self.error = error.localizedDescription }
    }

    private func open(_ item: FileItem) async {
        guard let client else { return }
        do {
            let tmp = try await client.download(id: item.id)
            let dest = FileManager.default.temporaryDirectory.appendingPathComponent(item.name)
            try? FileManager.default.removeItem(at: dest)
            try FileManager.default.moveItem(at: tmp, to: dest)
            preview = dest
        } catch { self.error = error.localizedDescription }
    }

    private func mkdir(_ name: String) async {
        let name = name.trimmingCharacters(in: .whitespaces)
        guard let client, !name.isEmpty else { return }
        do {
            _ = try await client.createFolder(source: ref.source, path: child(name))
            await load()
        } catch { self.error = error.localizedDescription }
    }

    private func rename(_ item: FileItem, to name: String) async {
        renaming = nil
        let name = name.trimmingCharacters(in: .whitespaces)
        guard let client, !name.isEmpty, name != item.name else { return }
        do {
            _ = try await client.move(id: item.id, to: child(name))
            await load()
        } catch { self.error = error.localizedDescription }
    }

    private func delete(_ item: FileItem) async {
        guard let client else { return }
        do {
            try await client.delete(id: item.id)
            items.removeAll { $0.path == item.path }
        } catch { self.error = error.localizedDescription }
    }

    private func upload(_ urls: [URL]) async {
        guard let client else { return }
        for (i, url) in urls.enumerated() {
            let scoped = url.startAccessingSecurityScopedResource()
            defer { if scoped { url.stopAccessingSecurityScopedResource() } }
            uploadStatus = "Uploading \(i + 1) of \(urls.count): \(url.lastPathComponent)"
            do {
                let existing = items.first { $0.name == url.lastPathComponent && !$0.isDir }
                _ = try await client.upload(UploadRequest(fileURL: url, source: ref.source,
                                                          path: child(url.lastPathComponent),
                                                          fileId: existing?.id, baseSha256: existing?.sha256))
            } catch { self.error = error.localizedDescription }
        }
        uploadStatus = nil
        await load()
    }
}
