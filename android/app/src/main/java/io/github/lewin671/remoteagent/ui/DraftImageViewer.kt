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
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.coreTextToMessageText
import io.github.lewin671.remoteagent.core.proto.ImageProblemView
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

@Composable
internal fun DraftImageViewer(
    draft: ImageDraft,
    remote: Remote,
    machineId: String?,
    start: Int,
    originalMaxBytes: Long,
    onClose: () -> Unit,
) {
    val copy = uiText()
    val context = LocalContext.current
    val images = draft.pictures
    val failedPictures = images.mapNotNull { img ->
        (img.state as? PendingImage.State.Failed)?.failure?.input?.let { img.id to it }
    }
    val problemEval = remember(remote, failedPictures) {
        evaluateImageProblems(remote, failedPictures.map { it.second })
    }
    val problemsById = remember(failedPictures, problemEval) {
        buildMap(failedPictures.size) {
            for (i in failedPictures.indices) {
                problemEval.problemsList.getOrNull(i)?.let { put(failedPictures[i].first, it) }
            }
        }
    }
    Dialog(onDismissRequest = onClose, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        PagedImageViewer(
            count = images.size,
            start = start,
            onClose = onClose,
            picture = { index -> key(images[index].id) { DraftViewerPage(images[index]) } },
            info = { index ->
                val img = images[index]
                val problem = problemsById[img.id]
                Text(copy(UiMessages.imagePosition(count = images.size, index = index + 1)), color = Color.White, style = T.s15b)
                val problemText = if (problem != null && problem.hasMessage()) uiText(problem.message) else null
                Text(problemText ?: imageStateWords(img, problem, copy = copy), color = Color.White, style = T.s15)
                Row {
                    if (problem?.retryable == true) {
                        TextButton(onClick = { draft.retry(context, img, remote, originalMaxBytes) { machineId } }) {
                            Text(copy(UiMessages.commonRetry()), color = Color(0xFF9EC1FF))
                        }
                    }
                    TextButton(onClick = { draft.remove(img); if (draft.pictures.isEmpty()) onClose() }) {
                        Text(copy(UiMessages.commonRemove()), color = Color(0xFF9EC1FF))
                    }
                }
            },
        )
    }
}

internal fun imageStateWords(
    image: PendingImage,
    problem: ImageProblemView? = null,
    copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText,
): String = when (val state = image.state) {
    PendingImage.State.Processing -> copy(UiMessages.imagePreparing())
    is PendingImage.State.Uploading -> copy(UiMessages.imageAndroidUploadProgress(percent = ((state.progress * 100).toInt()).toString()))
    PendingImage.State.Ready -> copy(UiMessages.imageUploaded())
    is PendingImage.State.Failed -> {
        val reason = if (problem != null && problem.hasMessage()) {
            coreTextToMessageText(problem.message).resolve(copy)
        } else {
            state.error.resolve(copy)
        }
        copy(UiMessages.attachmentFailure(reason = reason))
    }
}

@Composable
private fun DraftViewerPage(image: PendingImage) {
    val copy = uiText()
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
        bitmap?.let { ViewerBitmap(it, copy(UiMessages.imageDraft())) }
        if (image.state == PendingImage.State.Processing && bitmap == null) Text(copy(UiMessages.imagePreparing()), color = Color.White)
    }
}
