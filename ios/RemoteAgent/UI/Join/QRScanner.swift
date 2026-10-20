// Scans one QR code with the camera (Android's ui/Scan.kt), decoding on the
// device with AVFoundation; nothing of the picture leaves it or is kept.
//
// The camera is asked for when this view appears, which is when the user
// tapped 扫描二维码, and never before. Refused, the page says so and offers
// the way to the system's settings: iOS asks only once.
@preconcurrency import AVFoundation
import SwiftUI
import UIKit

/// How the camera stands for this app.
enum CameraAccess: Equatable {
    /// Not asked yet; asking now.
    case asking
    case granted
    /// Refused by the user, or by a restriction on the device.
    case denied
    /// Allowed, but this device has no camera to use (a simulator).
    case missing
}

struct QRScanner: View {
    let onResult: (String) -> Void
    let onCancel: () -> Void
    @State private var access = CameraAccess.asking
    @Environment(\.scenePhase) private var phase
    @Environment(\.openURL) private var openURL

    var body: some View {
        ZStack {
            Color.black.ignoresSafeArea()
            switch access {
            case .asking:
                EmptyView()
            case .granted:
                CameraPreview(onResult: onResult) { access = .missing }
                    .ignoresSafeArea()
                    .accessibilityLabel("相机画面，正在找配对二维码")
                Finder()
            case .denied:
                ScannerNote(text: "需要相机权限才能扫码；也可以返回手动输入配对码。", action: "允许使用相机") {
                    // iOS asks once; after that the switch is in the system's settings.
                    if let url = URL(string: UIApplication.openSettingsURLString) { openURL(url) }
                }
            case .missing:
                ScannerNote(text: "这台设备没有可用的相机；可以返回手动输入配对码。", action: nil) {}
            }
        }
        .overlay(alignment: .topLeading) {
            Button(action: onCancel) {
                Text("取消").type(T.body, .white).padding(.horizontal, 16).hitTarget()
            }
            .buttonStyle(PressStyle())
        }
        .preferredColorScheme(.dark)
        .task { await check(ask: true) }
        // Back from the system's settings, where the camera may have been allowed.
        .onChange(of: phase) { _, new in
            if new == .active { Task { await check(ask: false) } }
        }
    }

    private func check(ask: Bool) async {
        #if DEBUG
        if let forced = LaunchDriver.cameraAccess {
            access = forced
            return
        }
        #endif
        switch AVCaptureDevice.authorizationStatus(for: .video) {
        case .authorized:
            if access != .missing { access = .granted }
        case .notDetermined:
            guard ask else { return }
            access = await AVCaptureDevice.requestAccess(for: .video) ? .granted : .denied
        default:
            access = .denied
        }
    }
}

/// What the scanner says when it has no picture to show, with at most one thing to do about it.
private struct ScannerNote: View {
    let text: String
    let action: String?
    let onAction: () -> Void

    var body: some View {
        VStack(spacing: 20) {
            Text(text).type(T.body, .white).multilineTextAlignment(.center)
            if let action { Btn(action, kind: .primary, fill: false, action: onAction) }
        }
        .padding(24)
        .frame(maxWidth: 420)
    }
}

/// The square the code is held in: corners only, so the picture stays visible.
private struct Finder: View {
    var body: some View {
        VStack(spacing: 20) {
            RoundedRectangle(cornerRadius: Theme.Radius.lg, style: .continuous)
                .strokeBorder(.white.opacity(0.9), lineWidth: 3)
                .frame(width: 240, height: 240)
            Text("把手机 App 上的配对二维码放进框里")
                .type(T.sm, .white).multilineTextAlignment(.center)
                .padding(.horizontal, 12).padding(.vertical, 6)
                .background(.black.opacity(0.45), in: Capsule())
        }
        .padding(24)
        .accessibilityElement(children: .combine)
    }
}

private struct CameraPreview: UIViewRepresentable {
    let onResult: (String) -> Void
    let onMissing: () -> Void

    func makeCoordinator() -> ScanSession { ScanSession() }

    func makeUIView(context: Context) -> PreviewView {
        let view = PreviewView()
        view.videoLayer.videoGravity = .resizeAspectFill
        context.coordinator.onResult = onResult
        context.coordinator.onMissing = onMissing
        context.coordinator.start(showing: view.videoLayer)
        return view
    }

    func updateUIView(_ view: PreviewView, context: Context) {
        context.coordinator.onResult = onResult
        context.coordinator.onMissing = onMissing
    }

    /// Leaving the scanner releases the camera.
    static func dismantleUIView(_ view: PreviewView, coordinator: ScanSession) {
        coordinator.stop()
    }
}

private final class PreviewView: UIView {
    override class var layerClass: AnyClass { AVCaptureVideoPreviewLayer.self }
    var videoLayer: AVCaptureVideoPreviewLayer { layer as! AVCaptureVideoPreviewLayer }
}

/// The capture session of one visit to the scanner. It is set up, started
/// and stopped on a queue of its own, as AVFoundation asks; what it reads
/// comes back on the main actor, once.
@MainActor
private final class ScanSession: NSObject, AVCaptureMetadataOutputObjectsDelegate {
    var onResult: (String) -> Void = { _ in }
    var onMissing: () -> Void = {}
    private let session = AVCaptureSession()
    private let queue = DispatchQueue(label: "ra.scanner")
    private var done = false
    private var rotation: AVCaptureDevice.RotationCoordinator?
    private var rotationWatch: NSKeyValueObservation?
    private weak var layer: AVCaptureVideoPreviewLayer?

    func start(showing layer: AVCaptureVideoPreviewLayer) {
        guard let camera = AVCaptureDevice.default(.builtInWideAngleCamera, for: .video, position: .back),
              let input = try? AVCaptureDeviceInput(device: camera), session.canAddInput(input)
        else {
            // After this pass: the view is being made.
            Task { @MainActor in self.onMissing() }
            return
        }
        let output = AVCaptureMetadataOutput()
        session.beginConfiguration()
        session.addInput(input)
        guard session.canAddOutput(output) else {
            session.commitConfiguration()
            Task { @MainActor in self.onMissing() }
            return
        }
        session.addOutput(output)
        output.setMetadataObjectsDelegate(self, queue: .main)
        if output.availableMetadataObjectTypes.contains(.qr) { output.metadataObjectTypes = [.qr] }
        session.commitConfiguration()
        layer.session = session
        self.layer = layer
        // The picture stays upright whichever way the device is held.
        let rotation = AVCaptureDevice.RotationCoordinator(device: camera, previewLayer: layer)
        self.rotation = rotation
        turnPicture()
        rotationWatch = rotation.observe(\.videoRotationAngleForHorizonLevelPreview) { [weak self] _, _ in
            Task { @MainActor in self?.turnPicture() }
        }
        let session = session
        queue.async { session.startRunning() }
    }

    private func turnPicture() {
        guard let rotation else { return }
        layer?.connection?.videoRotationAngle = rotation.videoRotationAngleForHorizonLevelPreview
    }

    func stop() {
        // A code the camera read before it stopped may still be on its way here.
        done = true
        rotationWatch = nil
        rotation = nil
        let session = session
        queue.async { if session.isRunning { session.stopRunning() } }
    }

    nonisolated func metadataOutput(
        _ output: AVCaptureMetadataOutput, didOutput objects: [AVMetadataObject], from connection: AVCaptureConnection
    ) {
        // Untrusted text: it is only handed on, to be parsed as a pairing link or refused.
        guard let text = objects.lazy.compactMap({ ($0 as? AVMetadataMachineReadableCodeObject)?.stringValue }).first
        else { return }
        MainActor.assumeIsolated {
            guard !done else { return }
            done = true
            Haptics.confirm()
            stop()
            onResult(text)
        }
    }
}
