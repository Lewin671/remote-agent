// Takes device control strings and application program commands out of
// terminal output before the emulator reads it. SwiftTerm 1.11.2 decodes
// Sixel (a DCS) and Kitty graphics (an APC) into images it puts in the
// buffer itself, with nothing the app can refuse them by; output could
// then draw over the screen and hold as much memory as it likes
// (docs/ios-client.md 「终端」). Nothing else the app shows comes in these
// strings: their other uses are queries, whose answers are not sent back.
import Foundation

/// The filter for one terminal's output. It keeps its place between
/// frames: a string may begin in one and end in another.
public struct TerminalStringFilter: Sendable {
    private enum Kind { case dcs, apc }

    private enum State {
        case ground
        /// An ESC that may begin a string.
        case escape
        /// The first byte of a two-byte C1 control, which may be DCS or APC.
        case lead
        case string(Kind)
        /// An ESC inside a string: with `\` it ends it.
        case stringEscape(Kind)
        /// 0xC2 inside a string: with 0x9C it ends it.
        case stringLead(Kind)
    }

    private var state = State.ground

    public init() {}

    /// The output without those strings. What may begin one waits for the next frame.
    public mutating func take(_ data: Data) -> Data {
        var out = Data(capacity: data.count)
        for byte in data { step(byte, &out) }
        return out
    }

    private mutating func step(_ byte: UInt8, _ out: inout Data) {
        switch state {
        case .ground:
            switch byte {
            case 0x1B: state = .escape
            case 0xC2: state = .lead
            default: out.append(byte)
            }
        case .escape:
            switch byte {
            case 0x50: state = .string(.dcs)  // P
            case 0x5F: state = .string(.apc)  // _
            default:
                out.append(0x1B)
                state = .ground
                step(byte, &out)
            }
        case .lead:
            switch byte {
            case 0x90: state = .string(.dcs)
            case 0x9F: state = .string(.apc)
            default:
                out.append(0xC2)
                state = .ground
                step(byte, &out)
            }
        case .string(let kind):
            switch byte {
            case 0x1B: state = .stringEscape(kind)
            case 0xC2: state = .stringLead(kind)
            // CAN and SUB cancel any sequence; BEL ends an APC as the emulator reads it.
            case 0x18, 0x1A: state = .ground
            case 0x07 where kind == .apc: state = .ground
            default: break
            }
        case .stringEscape:
            if byte == 0x5C {  // \
                state = .ground
            } else {
                // Any other escape sequence ends the string and is read as itself.
                state = .escape
                step(byte, &out)
            }
        case .stringLead(let kind):
            if byte == 0x9C {
                state = .ground
            } else {
                state = .string(kind)
                step(byte, &out)
            }
        }
    }
}
