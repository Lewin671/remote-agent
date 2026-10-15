/** The axis of one continuous wheel or trackpad scroll: decided by its first
 * moving event and kept until the scroll pauses, so a sideways pan that
 * drifts up never counts as an upward scroll. */
export function wheelAxis(pause = 200) {
  let axis: "" | "x" | "y" = "";
  let last = -Infinity;
  return (e: { deltaX: number; deltaY: number; timeStamp: number }): "" | "x" | "y" => {
    if (e.timeStamp - last > pause) axis = "";
    last = e.timeStamp;
    if (!axis && (e.deltaX || e.deltaY)) axis = Math.abs(e.deltaX) > Math.abs(e.deltaY) ? "x" : "y";
    return axis;
  };
}
