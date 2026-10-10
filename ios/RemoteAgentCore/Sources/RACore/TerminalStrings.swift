// Takes device control strings and application program commands out of
// terminal output before the emulator reads it. SwiftTerm 1.11.2 decodes
// Sixel (a DCS) and Kitty graphics (an APC) into images it puts in the
// buffer itself, with nothing the app can refuse them by; output could
// then draw over the screen and hold as much memory as it likes
// (docs/ios-client.md 「终端」). Nothing else the app shows comes in these
// strings: their other uses are queries, whose answers are not sent back.
//
// Whether a byte begins such a string depends on where the emulator's
// parser stands: `P` and `_` do right after ESC, the raw bytes 0x90 and
// 0x9F do anywhere but in plain text, where they are part of a character
// (吐 is E5 90 90). So the filter follows the parser, state for state, as
// EscapeSequenceParser.swift of 1.11.2 moves: it reads what it lets
// through exactly as the emulator will. Where a string would begin, the
// emulator gets SUB instead, which ends whatever sequence it was in (CAN
// does not: 1.11.2 runs it and stays where it was), and the string is
// left out. Upgrading SwiftTerm means reading this again.
import Foundation

/// The filter for one terminal's output. It keeps its place between
/// frames: a sequence may begin in one and end in another. Each result
/// must be fed to the emulator whole, in one call: inside an OSC the
/// parser reads on to the end of what it was given.
public struct TerminalStringFilter: Sendable {
    /// Where the emulator's parser stands after what was let through.
    private enum Parser {
        case ground, escape, escapeIntermediate
        case csiEntry, csiParam, csiIntermediate, csiIgnore
        case osc, sosPm
    }

    private enum Dropping {
        case dcs, apc
        /// An ESC inside the string: with `\` it ends it, with anything else a new sequence begins.
        case escape
    }

    private var parser = Parser.ground
    private var dropping: Dropping?

    public init() {}

    /// The output without those strings.
    public mutating func take(_ data: Data) -> Data {
        var out = Data(capacity: data.count)
        // Inside an OSC the parser takes every byte from a printable one up
        // to BEL, CAN or ESC as the string's, within one call.
        var oscRun = false
        for byte in data {
            if let d = dropping {
                switch (d, byte) {
                case (.escape, 0x5C):  // ESC \
                    dropping = nil
                case (.escape, _):
                    dropping = nil
                    out.append(0x1B)
                    parser = .escape
                    step(byte, &out, &oscRun)
                case (_, 0x1B):
                    dropping = .escape
                // What ends the string for the emulator: SUB and ST either, CAN and BEL only an APC.
                case (_, 0x1A), (_, 0x9C), (.apc, 0x18), (.apc, 0x07):
                    dropping = nil
                default:
                    break
                }
                continue
            }
            step(byte, &out, &oscRun)
        }
        return out
    }

    /// Leaves a string out: the emulator is taken back to plain text first.
    private mutating func drop(_ kind: Dropping, _ out: inout Data) {
        out.append(0x1A)
        parser = .ground
        dropping = kind
    }

    private mutating func step(_ byte: UInt8, _ out: inout Data, _ oscRun: inout Bool) {
        if parser == .ground {
            // Everything from 0x20 up is printed, the raw C1 range included.
            if byte == 0x1B { parser = .escape }
            out.append(byte)
            return
        }
        if oscRun {
            if byte == 0x07 || byte == 0x18 || byte == 0x1B {
                oscRun = false
            } else {
                out.append(byte)
                return
            }
        }
        // What holds in every state but plain text.
        switch byte {
        case 0x90: return drop(.dcs, &out)
        case 0x9F: return drop(.apc, &out)
        case 0x1A, 0x9C, 0x80...0x8F, 0x91...0x97, 0x99, 0x9A: parser = .ground
        case 0x1B: parser = .escape
        case 0x9B: parser = .csiEntry
        case 0x9D: parser = .osc
        case 0x98, 0x9E: parser = .sosPm
        default:
            switch parser {
            case .ground:
                break
            case .escape:
                switch byte {
                case 0x50: return drop(.dcs, &out)  // P
                case 0x5F: return drop(.apc, &out)  // _
                case 0x5B: parser = .csiEntry  // [
                case 0x5D: parser = .osc  // ]
                case 0x58, 0x5E: parser = .sosPm  // X ^
                case 0x20...0x2F: parser = .escapeIntermediate
                case 0x30...0x7E: parser = .ground
                case 0x7F, 0x00...0x18, 0x1C...0x1F: break
                default: parser = .ground
                }
            case .escapeIntermediate:
                switch byte {
                case 0x20...0x2F, 0x7F, 0x00...0x18, 0x1C...0x1F: break
                default: parser = .ground
                }
            case .csiEntry:
                switch byte {
                case 0x30...0x39, 0x3B...0x3F: parser = .csiParam
                case 0x20...0x2F: parser = .csiIntermediate
                case 0x7F, 0x00...0x18, 0x1C...0x1F: break
                default: parser = .ground
                }
            case .csiParam:
                switch byte {
                case 0x30...0x3B: break
                case 0x3C...0x3F: parser = .csiIgnore
                case 0x20...0x2F: parser = .csiIntermediate
                case 0x7F, 0x00...0x18, 0x1C...0x1F: break
                default: parser = .ground
                }
            case .csiIntermediate:
                switch byte {
                case 0x20...0x2F: break
                case 0x30...0x3F: parser = .csiIgnore
                case 0x7F, 0x00...0x18, 0x1C...0x1F: break
                default: parser = .ground
                }
            case .csiIgnore:
                switch byte {
                case 0x20...0x3F, 0x7F, 0x00...0x18, 0x1C...0x1F, 0xA0...0xFF: break
                default: parser = .ground
                }
            case .osc:
                switch byte {
                case 0x07, 0x18: parser = .ground
                case 0x20...0x7F, 0xA0...0xFF: oscRun = true
                case 0x00...0x17, 0x1C...0x1F: break
                default: parser = .ground
                }
            case .sosPm:
                switch byte {
                case 0x20...0x7F, 0x00...0x18, 0x1C...0x1F: break
                default: parser = .ground
                }
            }
        }
        out.append(byte)
    }
}
