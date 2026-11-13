package io.github.lewin671.remoteagent.ui

import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.StrokeJoin
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.graphics.vector.PathParser
import androidx.compose.ui.graphics.vector.path
import androidx.compose.ui.unit.dp

/** Vector icons matching the exact geometry and stroke widths in the shared-file design draft. */
internal object SharedFileIcons {
    val Video: ImageVector by lazy {
        strokeIcon(
            "SharedFileVideo",
            1.8f,
            "M5.5 6h7a3 3 0 0 1 3 3v6a3 3 0 0 1-3 3h-7a3 3 0 0 1-3-3V9a3 3 0 0 1 3-3z",
            "m15.5 10.6 4.7-2.9a.6.6 0 0 1 .9.5v7.6a.6.6 0 0 1-.9.5l-4.7-2.9",
        )
    }

    val Image: ImageVector by lazy {
        strokeIcon(
            "SharedFileImage",
            1.8f,
            "M6.5 3.5h11A3.5 3.5 0 0 1 21 7v10a3.5 3.5 0 0 1-3.5 3.5h-11A3.5 3.5 0 0 1 3 17V7A3.5 3.5 0 0 1 6.5 3.5z",
            "M8.8 9.2m-1.7 0a1.7 1.7 0 1 0 3.4 0a1.7 1.7 0 1 0 -3.4 0",
            "m21 15.5-4.3-4.3a1.5 1.5 0 0 0-2.1 0L5.5 20.3",
        )
    }

    val Audio: ImageVector by lazy {
        strokeIcon(
            "SharedFileAudio",
            1.8f,
            "M9 18V5l12-2v13",
            "M6 18m-3 0a3 3 0 1 0 6 0a3 3 0 1 0 -6 0",
            "M18 16m-3 0a3 3 0 1 0 6 0a3 3 0 1 0 -6 0",
        )
    }

    val Zip: ImageVector by lazy {
        strokeIcon(
            "SharedFileZip",
            1.8f,
            "M7.5 3h9A3.5 3.5 0 0 1 20 6.5v11a3.5 3.5 0 0 1-3.5 3.5h-9A3.5 3.5 0 0 1 4 17.5v-11A3.5 3.5 0 0 1 7.5 3z",
            "M12 3v2.5M12 8v2.5M12 13v2.5",
        )
    }

    val Doc: ImageVector by lazy {
        strokeIcon(
            "SharedFileDoc",
            1.8f,
            "M14 2.8H8a2.5 2.5 0 0 0-2.5 2.5v13.4A2.5 2.5 0 0 0 8 21.2h8a2.5 2.5 0 0 0 2.5-2.5V7.3z",
            "M14 2.8v4.5h4.5",
        )
    }

    val Down: ImageVector by lazy {
        strokeIcon(
            "SharedFileDown",
            2.4f,
            "M12 5v13m0 0-5-5m5 5 5-5",
        )
    }

    val Pause: ImageVector by lazy {
        strokeIcon(
            "SharedFilePause",
            1.9f,
            "M9 5v14",
            "M15 5v14",
        )
    }

    val Close: ImageVector by lazy {
        strokeIcon(
            "SharedFileClose",
            1.9f,
            "M6 6l12 12",
            "M18 6L6 18",
        )
    }

    val Share: ImageVector by lazy {
        strokeIcon(
            "SharedFileShare",
            1.8f,
            "M12 14.5V3.5m0 0L8.2 7.3M12 3.5l3.8 3.8M6 11.5v7a2 2 0 0 0 2 2h8a2 2 0 0 0 2-2v-7",
        )
    }

    val Save: ImageVector by lazy {
        strokeIcon(
            "SharedFileSave",
            1.8f,
            "M12 3.5v11m0 0-4-4m4 4 4-4M4.5 15.5v3a2 2 0 0 0 2 2h11a2 2 0 0 0 2-2v-3",
        )
    }

    val Trash: ImageVector by lazy {
        strokeIcon(
            "SharedFileTrash",
            1.8f,
            "M4.5 6.5h15M9.5 6.5v-2h5v2M6.5 6.5l.9 12.6a1.5 1.5 0 0 0 1.5 1.4h6.2a1.5 1.5 0 0 0 1.5-1.4l.9-12.6",
        )
    }

    val PlayFilled: ImageVector by lazy {
        ImageVector.Builder(
            name = "SharedFilePlayFilled",
            defaultWidth = 24.dp,
            defaultHeight = 24.dp,
            viewportWidth = 24f,
            viewportHeight = 24f,
        ).apply {
            val nodes = PathParser().parsePathString(
                "M8 5.2v13.6a.8.8 0 0 0 1.2.7l11-6.8a.8.8 0 0 0 0-1.4l-11-6.8A.8.8 0 0 0 8 5.2z",
            ).toNodes()
            path(fill = SolidColor(Color.White)) {
                nodes.forEach { addPathNodes(it) }
            }
        }.build()
    }

    private fun strokeIcon(name: String, strokeWidth: Float, vararg paths: String): ImageVector =
        ImageVector.Builder(
            name = name,
            defaultWidth = 24.dp,
            defaultHeight = 24.dp,
            viewportWidth = 24f,
            viewportHeight = 24f,
        ).apply {
            for (d in paths) {
                val nodes = PathParser().parsePathString(d).toNodes()
                path(
                    stroke = SolidColor(Color.White),
                    strokeLineWidth = strokeWidth,
                    strokeLineCap = StrokeCap.Round,
                    strokeLineJoin = StrokeJoin.Round,
                ) {
                    nodes.forEach { addPathNodes(it) }
                }
            }
        }.build()

    private fun androidx.compose.ui.graphics.vector.PathBuilder.addPathNodes(
        node: androidx.compose.ui.graphics.vector.PathNode,
    ) {
        when (node) {
            is androidx.compose.ui.graphics.vector.PathNode.Close -> close()
            is androidx.compose.ui.graphics.vector.PathNode.RelativeMoveTo -> moveToRelative(node.dx, node.dy)
            is androidx.compose.ui.graphics.vector.PathNode.MoveTo -> moveTo(node.x, node.y)
            is androidx.compose.ui.graphics.vector.PathNode.RelativeLineTo -> lineToRelative(node.dx, node.dy)
            is androidx.compose.ui.graphics.vector.PathNode.LineTo -> lineTo(node.x, node.y)
            is androidx.compose.ui.graphics.vector.PathNode.RelativeHorizontalTo -> horizontalLineToRelative(node.dx)
            is androidx.compose.ui.graphics.vector.PathNode.HorizontalTo -> horizontalLineTo(node.x)
            is androidx.compose.ui.graphics.vector.PathNode.RelativeVerticalTo -> verticalLineToRelative(node.dy)
            is androidx.compose.ui.graphics.vector.PathNode.VerticalTo -> verticalLineTo(node.y)
            is androidx.compose.ui.graphics.vector.PathNode.RelativeCurveTo -> curveToRelative(node.dx1, node.dy1, node.dx2, node.dy2, node.dx3, node.dy3)
            is androidx.compose.ui.graphics.vector.PathNode.CurveTo -> curveTo(node.x1, node.y1, node.x2, node.y2, node.x3, node.y3)
            is androidx.compose.ui.graphics.vector.PathNode.RelativeReflectiveCurveTo -> reflectiveCurveToRelative(node.dx1, node.dy1, node.dx2, node.dy2)
            is androidx.compose.ui.graphics.vector.PathNode.ReflectiveCurveTo -> reflectiveCurveTo(node.x1, node.y1, node.x2, node.y2)
            is androidx.compose.ui.graphics.vector.PathNode.RelativeQuadTo -> quadToRelative(node.dx1, node.dy1, node.dx2, node.dy2)
            is androidx.compose.ui.graphics.vector.PathNode.QuadTo -> quadTo(node.x1, node.y1, node.x2, node.y2)
            is androidx.compose.ui.graphics.vector.PathNode.RelativeReflectiveQuadTo -> reflectiveQuadToRelative(node.dx, node.dy)
            is androidx.compose.ui.graphics.vector.PathNode.ReflectiveQuadTo -> reflectiveQuadTo(node.x, node.y)
            is androidx.compose.ui.graphics.vector.PathNode.RelativeArcTo -> arcToRelative(node.horizontalEllipseRadius, node.verticalEllipseRadius, node.theta, node.isMoreThanHalf, node.isPositiveArc, node.arcStartDx, node.arcStartDy)
            is androidx.compose.ui.graphics.vector.PathNode.ArcTo -> arcTo(node.horizontalEllipseRadius, node.verticalEllipseRadius, node.theta, node.isMoreThanHalf, node.isPositiveArc, node.arcStartX, node.arcStartY)
        }
    }
}
