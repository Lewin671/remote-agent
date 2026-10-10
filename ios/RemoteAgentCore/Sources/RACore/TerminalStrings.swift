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

    /// Where the parser would stand inside a string that is being left out.
    private enum Dropping {
        case dcsEntry, dcsParam, dcsIntermediate, dcsIgnore, dcsPassthrough
        case apc
        /// An ESC inside the string: with `\` it ends it, with anything else a new sequence begins.
        case escape
    }

    private var parser = Parser.ground
    private var dropping: Dropping?

    public init() {}

    /// The output without those strings.
    public mutating func take(_ data: Data) -> Data {
        var out = Data(capacity: data.count)
        // Inside an OSC or an APC the parser takes every byte from a
        // printable one up to BEL, CAN or ESC as the string's, within one call.
        var run = false
        for byte in data {
            if let d = dropping { skip(d, byte, &out, &run) } else { step(byte, &out, &run) }
        }
        return out
    }

    /// One byte of a string that is left out. The string ends where it
    /// would for the emulator, also when that is not a terminator: what
    /// follows is shown, as it would have been.
    private mutating func skip(_ d: Dropping, _ byte: UInt8, _ out: inout Data, _ run: inout Bool) {
        if d == .escape {
            dropping = nil
            if byte != 0x5C {  // not ESC \: a sequence of its own
                out.append(0x1B)
                parser = .escape
                step(byte, &out, &run)
            }
            return
        }
        if d == .apc, run {
            if byte == 0x07 || byte == 0x18 || byte == 0x1B { run = false } else { return }
        }
        func leave(as sevenBit: UInt8? = nil, _ next: Parser = .ground) {
            dropping = nil
            run = false
            if let sevenBit {
                out.append(0x1B)
                out.append(sevenBit)
            }
            parser = next
        }
        switch byte {
        case 0x1B: dropping = .escape
        case 0x1A, 0x9C, 0x19: leave()
        case 0x90: dropping = .dcsEntry
        case 0x9F: dropping = .apc
        // The other raw controls end the string and act: as their two-byte forms, the emulator being in plain text.
        case 0x9B: leave(as: 0x5B, .csiEntry)
        case 0x9D: leave(as: 0x5D, .osc)
        case 0x98, 0x9E: leave(as: 0x58, .sosPm)
        case 0x84, 0x85, 0x88: leave(as: byte - 0x40)
        case 0x80...0x9A: leave()
        default:
            let executable = byte <= 0x18 || (0x1C...0x1F).contains(byte)
            switch d {
            case .escape:
                break
            case .apc:
                if byte == 0x07 || byte == 0x18 { leave() } else if !executable { run = true }
            case .dcsEntry:
                switch byte {
                case _ where executable, 0x7F: break
                case 0x20...0x2F: dropping = .dcsIntermediate
                case 0x3A: dropping = .dcsIgnore
                case 0x30...0x3F: dropping = .dcsParam
                case 0x40...0x7E: dropping = .dcsPassthrough
                default: leave()
                }
            case .dcsParam:
                switch byte {
                case _ where executable, 0x7F, 0x30...0x39, 0x3B: break
                case 0x3A, 0x3C...0x3F: dropping = .dcsIgnore
                case 0x20...0x2F: dropping = .dcsIntermediate
                case 0x40...0x7E: dropping = .dcsPassthrough
                default: leave()
                }
            case .dcsIntermediate:
                switch byte {
                case _ where executable, 0x7F, 0x20...0x2F: break
                case 0x30...0x3F: dropping = .dcsIgnore
                case 0x40...0x7E: dropping = .dcsPassthrough
                default: leave()
                }
            case .dcsIgnore, .dcsPassthrough:
                break
            }
        }
    }

    /// Leaves a string out: the emulator is taken back to plain text first.
    private mutating func drop(_ kind: Dropping, _ out: inout Data, _ run: inout Bool) {
        out.append(0x1A)
        parser = .ground
        dropping = kind
        run = false
    }

    private mutating func step(_ byte: UInt8, _ out: inout Data, _ run: inout Bool) {
        if parser == .ground {
            // Everything from 0x20 up is printed, the raw C1 range included.
            if byte == 0x1B { parser = .escape }
            out.append(byte)
            return
        }
        if run {
            if byte == 0x07 || byte == 0x18 || byte == 0x1B {
                run = false
            } else {
                out.append(byte)
                return
            }
        }
        // What holds in every state but plain text.
        switch byte {
        case 0x90: return drop(.dcsEntry, &out, &run)
        case 0x9F: return drop(.apc, &out, &run)
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
                case 0x50: return drop(.dcsEntry, &out, &run)  // P
                case 0x5F: return drop(.apc, &out, &run)  // _
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
                case 0x20...0x7F, 0xA0...0xFF: run = true
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
