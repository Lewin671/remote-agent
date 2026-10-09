// A QR code drawn on the device with Core Image (Android draws its own
// with ZXing): error correction M, as every client's pairing code.
import CoreImage
import CoreImage.CIFilterBuiltins
import SwiftUI
import UIKit

enum QRCode {
    /// The code for `text`, one pixel a module, or nil when it does not fit a code.
    static func image(_ text: String) -> CGImage? {
        let filter = CIFilter.qrCodeGenerator()
        filter.message = Data(text.utf8)
        filter.correctionLevel = "M"
        guard let output = filter.outputImage else { return nil }
        return CIContext().createCGImage(output, from: output.extent)
    }
}

/// A QR code on white, whatever the theme: a scanner needs dark on light.
/// The modules are scaled without smoothing, so their edges stay sharp.
struct QRCodeView: View {
    let text: String
    let label: String

    var body: some View {
        Group {
            if let image = QRCode.image(text) {
                Image(decorative: image, scale: 1).interpolation(.none).resizable().scaledToFit()
            } else {
                Color.white
            }
        }
        .padding(12)
        .frame(width: 170, height: 170)
        .background(.white, in: RoundedRectangle(cornerRadius: Theme.Radius.lg, style: .continuous))
        .shadow(color: .black.opacity(0.08), radius: 9, y: 4)
        .accessibilityElement()
        .accessibilityLabel(label)
        .accessibilityAddTraits(.isImage)
    }
}

/// The web client's address for the QR code: app.<domain> next to ra.<domain>, unless one is set.
func webOrigin(server: String, preferred: String) -> String {
    if !preferred.isEmpty { return preferred }
    guard let host = URLComponents(string: server)?.host, host.hasPrefix("ra.") else { return server }
    return "https://app." + host.dropFirst(3)
}
