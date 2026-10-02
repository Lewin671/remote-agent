package com.termux.terminal;

import org.junit.Test;
import java.nio.charset.StandardCharsets;
import static org.junit.Assert.assertEquals;

public class ClipboardSecurityTest {
    @Test public void remoteOutputCannotOverwriteClipboard() {
        final int[] copies = {0};
        TerminalOutput output = new TerminalOutput() {
            public void write(byte[] data, int offset, int count) {}
            public void titleChanged(String oldTitle, String newTitle) {}
            public void onCopyTextToClipboard(String text) { copies[0]++; }
            public void onPasteTextFromClipboard() {}
            public void onBell() {}
            public void onColorsChanged() {}
        };
        TerminalEmulator emulator = new TerminalEmulator(output, 80, 24, 8, 16, 100, null);
        for (String ending : new String[]{"\u0007", "\u001b\\"}) {
            // Also split the control sequence across stream chunks.
            for (byte b : ("\u001b]52;c;cm0gLXJmIH4=" + ending).getBytes(StandardCharsets.UTF_8))
                emulator.append(new byte[]{b}, 1);
        }
        assertEquals(0, copies[0]);
    }
}
