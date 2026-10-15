package com.termux.view;

/**
 * The axis of one continuous wheel or trackpad scroll: decided by its first moving event and
 * kept until the scroll pauses, so a sideways pan that drifts up never counts as an upward
 * scroll. The same rule the web client's wheelAxis uses (web/src/wheel.ts), in milliseconds.
 */
public final class WheelAxis {

    /** Which way a scroll goes; the axis holds for the whole scroll. */
    public enum Axis { NONE, HORIZONTAL, VERTICAL }

    /** How long a scroll may pause before the next moving event decides the axis afresh. */
    public static final long PAUSE_MILLIS = 200L;

    private final long mPauseMillis;
    private Axis mAxis = Axis.NONE;
    private long mLastMillis;

    public WheelAxis() {
        this(PAUSE_MILLIS);
    }

    public WheelAxis(long pauseMillis) {
        mPauseMillis = pauseMillis;
    }

    /**
     * The axis the scroll at {@code timeMillis} belongs to, from this event's own movement. The
     * first event that moves decides it by the dominant one; every later event keeps it until a
     * pause longer than the pause, when the next moving event decides afresh. A tie is vertical,
     * so a pull into reading still works.
     */
    public Axis axis(float deltaX, float deltaY, long timeMillis) {
        if (mAxis != Axis.NONE && timeMillis - mLastMillis > mPauseMillis) mAxis = Axis.NONE;
        mLastMillis = timeMillis;
        if (mAxis == Axis.NONE && (deltaX != 0f || deltaY != 0f)) {
            mAxis = Math.abs(deltaX) > Math.abs(deltaY) ? Axis.HORIZONTAL : Axis.VERTICAL;
        }
        return mAxis;
    }
}
