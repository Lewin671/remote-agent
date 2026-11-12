package io.github.lewin671.remoteagent.ui

import android.graphics.BitmapFactory
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.produceState
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import io.github.lewin671.remoteagent.core.Remote
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

@Composable
internal fun DraftImageViewer(draft: ImageDraft, remote: Remote, machineId: String?, start: Int, onClose: () -> Unit) {
    val context = LocalContext.current
    val images = draft.pictures
    Dialog(onDismissRequest = onClose, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        PagedImageViewer(images.size, start, onClose,
            picture = { index -> key(images[index].id) { DraftViewerPage(images[index]) } },
            info = { index ->
                val img = images[index]
                val problem = (img.state as? PendingImage.State.Failed)?.let { imageProblem(it.error) }
                Text("图片 ${index + 1}，共 ${images.size} 张", color = Color.White, style = T.s15b)
                Text(problem?.message ?: imageStateWords(img), color = Color.White, style = T.s15)
                Row {
                    if (problem?.retryable == true) TextButton(onClick = { draft.retry(context, img, remote) { machineId } }) { Text("重试", color = Color(0xFF9EC1FF)) }
                    TextButton(onClick = { draft.remove(img); if (draft.pictures.isEmpty()) onClose() }) { Text("移除", color = Color(0xFF9EC1FF)) }
                }
            })
    }
}

internal fun imageStateWords(image: PendingImage): String = when (val state = image.state) {
    PendingImage.State.Processing -> "正在处理"
    is PendingImage.State.Uploading -> "正在传到电脑，${(state.progress * 100).toInt()}%"
    PendingImage.State.Ready -> "已传到电脑"
    is PendingImage.State.Failed -> "失败：${imageProblem(state.error).message}"
}

@Composable
private fun DraftViewerPage(image: PendingImage) {
    val processed = image.processed
    val bitmap by produceState<ImageBitmap?>(image.thumb, processed) {
        val data = processed?.bytes ?: return@produceState
        value = withContext(Dispatchers.Default) {
            val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
            BitmapFactory.decodeByteArray(data, 0, data.size, bounds)
            var sample = 1
            while (maxOf(bounds.outWidth, bounds.outHeight) / sample > 2048) sample *= 2
            BitmapFactory.decodeByteArray(data, 0, data.size, BitmapFactory.Options().apply { inSampleSize = sample })?.asImageBitmap()
        }
    }
    Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
        bitmap?.let { ViewerBitmap(it, "待发送的图片") }
        if (image.state == PendingImage.State.Processing && bitmap == null) Text("正在处理", color = Color.White)
    }
}
