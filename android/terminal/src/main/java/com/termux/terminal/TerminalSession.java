package com.termux.terminal;

import java.util.UUID;

/**
 * A terminal session whose process runs elsewhere.
 * <p>
 * MODIFIED for remote-agent from termux-app v0.118.3 (Apache License 2.0): the upstream class
 * starts a local subprocess on a PTY through JNI. This version owns no process. Bytes typed in the
 * view go to a {@link Sink}; bytes from the remote terminal are fed with {@link #append(byte[], int)}
 * and {@link #snapshot(int, int, byte[])}. The emulator keeps the remote terminal's size: the size
 * the view asks for through {@link #updateSize(int, int, int, int)} is only reported to the sink.
 * <p>
 * All methods must be called on the main thread.
 */
public final class TerminalSession extends TerminalOutput {

    /** Receives what the view produces. */
    public interface Sink {
        /** Bytes typed, to send to the remote terminal. */
        void onInput(byte[] data);

        /** How many columns and rows the view has room for at its current font size. */
        void onViewSize(int columns, int rows);
    }

    public final String mHandle = UUID.randomUUID().toString();

    TerminalEmulator mEmulator;

    /** Callback which gets notified when a session finishes or changes title. */
    TerminalSessionClient mClient;

    /** Set by the application for user identification of session, not by terminal. */
    public String mSessionName;

    private final Sink mSink;
    private final Integer mTranscriptRows;
    private final byte[] mUtf8InputBuffer = new byte[5];
    private int mColumns = 80;
    private int mRows = 24;
    private int mCellWidth = 1;
    private int mCellHeight = 1;
    private boolean mRunning = true;

    public TerminalSession(Integer transcriptRows, TerminalSessionClient client, Sink sink) {
        this.mTranscriptRows = transcriptRows;
        this.mClient = client;
        this.mSink = sink;
    }

    public void updateTerminalSessionClient(TerminalSessionClient client) {
        mClient = client;
        if (mEmulator != null) mEmulator.updateTerminalSessionClient(client);
    }

    /** Called by the view with the size it has room for. The emulator keeps the remote size. */
    public void updateSize(int columns, int rows, int cellWidthPixels, int cellHeightPixels) {
        mCellWidth = cellWidthPixels;
        mCellHeight = cellHeightPixels;
        if (mEmulator == null) {
            mEmulator = new TerminalEmulator(this, mColumns, mRows, mCellWidth, mCellHeight, mTranscriptRows, mClient);
        }
        mSink.onViewSize(columns, rows);
    }

    /** Replaces the screen with a snapshot of the remote terminal. */
    public void snapshot(int columns, int rows, byte[] data) {
        mColumns = Math.max(4, columns);
        mRows = Math.max(4, rows);
        if (mEmulator == null) {
            mEmulator = new TerminalEmulator(this, mColumns, mRows, mCellWidth, mCellHeight, mTranscriptRows, mClient);
        } else {
            mEmulator.resize(mColumns, mRows, mCellWidth, mCellHeight);
        }
        // Reset the parser directly: RIS inside an unfinished OSC/DCS is only string data.
        mEmulator.reset();
        byte[] reset = {27, 'c'};
        mEmulator.append(reset, reset.length);
        mEmulator.append(data, data.length);
        notifyScreenUpdate();
    }

    /** Feeds output of the remote terminal. */
    public void append(byte[] data, int length) {
        if (mEmulator == null) return;
        mEmulator.append(data, length);
        notifyScreenUpdate();
    }

    public int getColumns() {
        return mColumns;
    }

    public int getRows() {
        return mRows;
    }

    /** The terminal title as set through escape sequences or null if none set. */
    public String getTitle() {
        return (mEmulator == null) ? null : mEmulator.getTitle();
    }

    /** Write data typed in the view to the remote terminal. */
    @Override
    public void write(byte[] data, int offset, int count) {
        if (!mRunning || count <= 0) return;
        byte[] copy = new byte[count];
        System.arraycopy(data, offset, copy, 0, count);
        mSink.onInput(copy);
    }

    /** Write the Unicode code point to the terminal encoded in UTF-8. */
    public void writeCodePoint(boolean prependEscape, int codePoint) {
        if (codePoint > 1114111 || (codePoint >= 0xD800 && codePoint <= 0xDFFF)) {
            throw new IllegalArgumentException("Invalid code point: " + codePoint);
        }
        int n = 0;
        if (prependEscape) mUtf8InputBuffer[n++] = 27;
        if (codePoint <= 0b1111111) {
            mUtf8InputBuffer[n++] = (byte) codePoint;
        } else if (codePoint <= 0b11111111111) {
            mUtf8InputBuffer[n++] = (byte) (0b11000000 | (codePoint >> 6));
            mUtf8InputBuffer[n++] = (byte) (0b10000000 | (codePoint & 0b111111));
        } else if (codePoint <= 0b1111111111111111) {
            mUtf8InputBuffer[n++] = (byte) (0b11100000 | (codePoint >> 12));
            mUtf8InputBuffer[n++] = (byte) (0b10000000 | ((codePoint >> 6) & 0b111111));
            mUtf8InputBuffer[n++] = (byte) (0b10000000 | (codePoint & 0b111111));
        } else {
            mUtf8InputBuffer[n++] = (byte) (0b11110000 | (codePoint >> 18));
            mUtf8InputBuffer[n++] = (byte) (0b10000000 | ((codePoint >> 12) & 0b111111));
            mUtf8InputBuffer[n++] = (byte) (0b10000000 | ((codePoint >> 6) & 0b111111));
            mUtf8InputBuffer[n++] = (byte) (0b10000000 | (codePoint & 0b111111));
        }
        write(mUtf8InputBuffer, 0, n);
    }

    public TerminalEmulator getEmulator() {
        return mEmulator;
    }

    protected void notifyScreenUpdate() {
        mClient.onTextChanged(this);
    }

    public void reset() {
        if (mEmulator == null) return;
        mEmulator.reset();
        notifyScreenUpdate();
    }

    /** Stops sending input; the remote stream has ended. */
    public void finish() {
        if (!mRunning) return;
        mRunning = false;
        mClient.onSessionFinished(this);
    }

    public boolean isRunning() {
        return mRunning;
    }

    @Override
    public void titleChanged(String oldTitle, String newTitle) {
        mClient.onTitleChanged(this);
    }

    @Override
    public void onCopyTextToClipboard(String text) {
        mClient.onCopyTextToClipboard(this, text);
    }

    @Override
    public void onPasteTextFromClipboard() {
        mClient.onPasteTextFromClipboard(this);
    }

    @Override
    public void onBell() {
        mClient.onBell(this);
    }

    @Override
    public void onColorsChanged() {
        mClient.onColorsChanged(this);
    }
}
