package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.layout.size
import androidx.compose.material3.Icon
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.StrokeJoin
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.graphics.vector.addPathNodes
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp

/*
 * Every icon of the interface. They all come from Lucide (ISC, the text is
 * res/raw/isc_lucide.txt), one per meaning (docs/design-system.md §5);
 * nothing else draws an icon, and no font glyph stands in for one. The path
 * data is lucide-react's, so both clients draw the same shapes: a 24 grid,
 * a line of 2 with round caps and joins, nothing filled but the square that
 * interrupts.
 */

/** A Lucide icon from the paths of its SVG; [filled] also fills them. */
private fun lucide(name: String, vararg paths: String, filled: Boolean = false): ImageVector {
    val b = ImageVector.Builder(name, 24.dp, 24.dp, 24f, 24f)
    for (d in paths) b.addPath(
        addPathNodes(d), fill = if (filled) SolidColor(Color.Black) else null, stroke = SolidColor(Color.Black),
        strokeLineWidth = 2f, strokeLineCap = StrokeCap.Round, strokeLineJoin = StrokeJoin.Round,
    )
    return b.build()
}

/** An SVG circle as a path: two half turns. */
private fun circle(cx: Int, cy: Int, r: Int) = "M${cx - r} ${cy}a$r $r 0 1 0 ${2 * r} 0a$r $r 0 1 0 ${-2 * r} 0z"

object Lucide {
    /** Goes to another page or sheet. */
    val ChevronRight = lucide("chevron-right", "m9 18 6-6-6-6")

    /** Back to the previous page, or up one folder. */
    val ChevronLeft = lucide("chevron-left", "m15 18-6-6 6-6")

    /** Opens in place, or the menu right under it. */
    val ChevronDown = lucide("chevron-down", "m6 9 6 6 6-6")

    /** Closes what opened in place. */
    val ChevronUp = lucide("chevron-up", "m18 15-6-6-6 6")

    /** More actions. */
    val Ellipsis = lucide("ellipsis", circle(12, 12, 1), circle(19, 12, 1), circle(5, 12, 1))

    /** Adds to the message, or to the end of a list. */
    val Plus = lucide("plus", "M5 12h14", "M12 5v14")

    /** A new session. */
    val SquarePen = lucide(
        "square-pen",
        "M12 3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7",
        "M18.375 2.625a1 1 0 0 1 3 3l-9.013 9.014a2 2 0 0 1-.853.505l-2.873.84a.5.5 0 0 1-.62-.62l.84-2.873a2 2 0 0 1 .506-.852z",
    )

    /** Sends; the terminal's up key. */
    val ArrowUp = lucide("arrow-up", "m5 12 7-7 7 7", "M12 19V5")

    /** Interrupts the turn; the one filled icon. Lucide's rect of 18 at 3,3 with corners of 2. */
    val Square = lucide("square", "M5 3h14a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2z", filled = true)

    /** Copies a code block of a reply. Lucide's rect of 14 at 8,8 with corners of 2. */
    val Copy = lucide("copy", "M10 8h10a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H10a2 2 0 0 1-2-2V10a2 2 0 0 1 2-2z", "M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2")

    /** A task of a reply's task list, open and done: Lucide's square, not filled, and square-check. */
    val Box = lucide("square", "M5 3h14a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2z")
    val BoxChecked = lucide("square-check", "M5 3h14a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2z", "m9 12 2 2 4-4")

    /** Down to the newest message; the terminal's down key. */
    val ArrowDown = lucide("arrow-down", "M12 5v14", "m19 12-7 7-7-7")

    /** Done; also the chosen one of several. */
    val Check = lucide("check", "M20 6 9 17l-5-5")

    /** Failed; also closes and clears. */
    val X = lucide("x", "M18 6 6 18", "m6 6 12 12")

    /** In progress; [Mark] turns it. */
    val LoaderCircle = lucide("loader-circle", "M21 12a9 9 0 1 1-6.219-8.56")

    /** Canceled. */
    val Minus = lucide("minus", "M5 12h14")

    /** Filters a list. */
    val Search = lucide("search", "m21 21-4.34-4.34", circle(11, 11, 8))

    /** The sessions tab. */
    val MessageSquare = lucide(
        "message-square",
        "M22 17a2 2 0 0 1-2 2H6.828a2 2 0 0 0-1.414.586l-2.202 2.202A.71.71 0 0 1 2 21.286V5a2 2 0 0 1 2-2h16a2 2 0 0 1 2 2z",
    )

    /** The settings tab. */
    val Settings = lucide(
        "settings",
        "M9.671 4.136a2.34 2.34 0 0 1 4.659 0 2.34 2.34 0 0 0 3.319 1.915 2.34 2.34 0 0 1 2.33 4.033 2.34 2.34 0 0 0 0 3.831 2.34 2.34 0 0 1-2.33 4.033 2.34 2.34 0 0 0-3.319 1.915 2.34 2.34 0 0 1-4.659 0 2.34 2.34 0 0 0-3.32-1.915 2.34 2.34 0 0 1-2.33-4.033 2.34 2.34 0 0 0 0-3.831A2.34 2.34 0 0 1 6.35 6.051a2.34 2.34 0 0 0 3.319-1.915",
        circle(12, 12, 3),
    )

    /** No network on this device, and a server out of reach. */
    val WifiOff = lucide(
        "wifi-off",
        "M12 20h.01", "M8.5 16.429a5 5 0 0 1 7 0", "M5 12.859a10 10 0 0 1 5.17-2.69", "M19 12.859a10 10 0 0 0-2.007-1.523",
        "M2 8.82a15 15 0 0 1 4.177-2.643", "M22 8.82a15 15 0 0 0-11.288-3.764", "m2 2 20 20",
    )
    val CloudOff = lucide(
        "cloud-off",
        "m2 2 20 20", "M5.782 5.782A7 7 0 0 0 9 19h8.5a4.5 4.5 0 0 0 1.307-.193", "M21.532 16.5A4.5 4.5 0 0 0 17.5 10h-1.79A7.008 7.008 0 0 0 10 5.07",
    )

    /** A computer. */
    val Laptop = lucide(
        "laptop",
        "M18 5a2 2 0 0 1 2 2v8.526a2 2 0 0 0 .212.897l1.068 2.127a1 1 0 0 1-.9 1.45H3.62a1 1 0 0 1-.9-1.45l1.068-2.127A2 2 0 0 0 4 15.526V7a2 2 0 0 1 2-2z",
        "M20.054 15.987H3.946",
    )

    /** Client devices: Lucide globe, smartphone and tablet. */
    val Globe = lucide("globe", circle(12, 12, 10), "M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20", "M2 12h20")
    val Smartphone = lucide("smartphone", "M7 2h10a2 2 0 0 1 2 2v16a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2z", "M12 18h.01")
    val Tablet = lucide("tablet", "M6 2h12a2 2 0 0 1 2 2v16a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2z", "M12 18h.01")

    /** The terminal's remaining arrow keys and its return key. */
    val ArrowLeft = lucide("arrow-left", "m12 19-7-7 7-7", "M19 12H5")
    val ArrowRight = lucide("arrow-right", "M5 12h14", "m12 5 7 7-7 7")
    val CornerDownLeft = lucide("corner-down-left", "M20 4v7a4 4 0 0 1-4 4H4", "m9 10-5 5 5 5")

    /** Shows or hides the phone's keyboard under the terminal. Lucide's rect of 20 by 16 at 2,4 with corners of 2. */
    val Keyboard = lucide(
        "keyboard",
        "M10 8h.01", "M12 12h.01", "M14 8h.01", "M16 12h.01", "M18 8h.01", "M6 8h.01", "M7 16h10", "M8 12h.01",
        "M4 4h16a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2z",
    )

    /** Fits the terminal to this screen. */
    val Maximize2 = lucide("maximize-2", "M15 3h6v6", "m21 3-7 7", "m3 21 7-7", "M9 21H3v-6")
}

/**
 * An icon in the color of the text it is with. One that is a button by
 * itself is 20 to 22 dp, 22 in the tab bar, and says what it does in
 * [contentDescription] unless its tap target already does; one beside its
 * words is decoration and leaves it null.
 */
@Composable
fun Ic(icon: ImageVector, size: Dp, tint: Color, modifier: Modifier = Modifier, contentDescription: String? = null) {
    Icon(icon, contentDescription, modifier.size(size), tint)
}

/** The size of an icon in a line of text: as tall as [style]'s letters. */
@Composable
fun iconSize(style: TextStyle): Dp = with(LocalDensity.current) { style.fontSize.toDp() }
