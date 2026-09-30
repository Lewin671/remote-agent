package io.github.lewin671.remoteagent.ui

import android.Manifest
import android.content.pm.PackageManager
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.camera.core.CameraSelector
import androidx.camera.core.ImageAnalysis
import androidx.camera.core.Preview
import androidx.camera.lifecycle.ProcessCameraProvider
import androidx.camera.view.PreviewView
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Button
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalLifecycleOwner
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.core.content.ContextCompat
import com.google.zxing.BarcodeFormat
import com.google.zxing.BinaryBitmap
import com.google.zxing.DecodeHintType
import com.google.zxing.MultiFormatReader
import com.google.zxing.NotFoundException
import com.google.zxing.PlanarYUVLuminanceSource
import com.google.zxing.common.HybridBinarizer
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean

/** Scans one QR code with the camera, decoding on the device with ZXing. */
@Composable
fun QrScanner(onResult: (String) -> Unit, onCancel: () -> Unit) {
    val ctx = LocalContext.current
    var granted by remember {
        mutableStateOf(ContextCompat.checkSelfPermission(ctx, Manifest.permission.CAMERA) == PackageManager.PERMISSION_GRANTED)
    }
    val ask = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { granted = it }
    LaunchedEffect(Unit) { if (!granted) ask.launch(Manifest.permission.CAMERA) }
    Box(Modifier.fillMaxSize()) {
        if (granted) CameraPreview(onResult)
        else Column(Modifier.align(Alignment.Center).padding(24.dp), horizontalAlignment = Alignment.CenterHorizontally) {
            Text("需要相机权限才能扫码；也可以返回手动输入配对码。")
            Button(onClick = { ask.launch(Manifest.permission.CAMERA) }) { Text("允许使用相机") }
        }
        TextButton(onClick = onCancel, modifier = Modifier.align(Alignment.TopStart).padding(8.dp)) { Text("取消", color = Color.White) }
    }
}

@Composable
private fun CameraPreview(onResult: (String) -> Unit) {
    val ctx = LocalContext.current
    val owner = LocalLifecycleOwner.current
    val done = remember { AtomicBoolean(false) }
    val executor = remember { Executors.newSingleThreadExecutor() }
    val disposed = remember { AtomicBoolean(false) }
    val bound = remember { java.util.concurrent.atomic.AtomicReference<ProcessCameraProvider?>(null) }
    // Leaving the scanner releases the camera: the activity stays started,
    // so the use cases would otherwise stay bound to it.
    DisposableEffect(Unit) {
        onDispose {
            disposed.set(true)
            bound.getAndSet(null)?.unbindAll()
            executor.shutdown()
        }
    }
    AndroidView(factory = { c ->
        val view = PreviewView(c)
        val future = ProcessCameraProvider.getInstance(c)
        future.addListener({
            if (disposed.get()) return@addListener
            val provider = future.get()
            val preview = Preview.Builder().build().also { it.surfaceProvider = view.surfaceProvider }
            val reader = MultiFormatReader().apply { setHints(mapOf(DecodeHintType.POSSIBLE_FORMATS to listOf(BarcodeFormat.QR_CODE))) }
            val analysis = ImageAnalysis.Builder().setBackpressureStrategy(ImageAnalysis.STRATEGY_KEEP_ONLY_LATEST).build()
            analysis.setAnalyzer(executor) { img ->
                try {
                    val plane = img.planes[0]
                    val buf = plane.buffer
                    val data = ByteArray(buf.remaining()).also { buf.get(it) }
                    val src = PlanarYUVLuminanceSource(data, plane.rowStride, img.height, 0, 0, img.width, img.height, false)
                    val text = reader.decodeWithState(BinaryBitmap(HybridBinarizer(src))).text
                    if (done.compareAndSet(false, true)) ContextCompat.getMainExecutor(ctx).execute { onResult(text) }
                } catch (e: NotFoundException) {
                    // No code in this frame.
                } catch (e: Exception) {
                    // Try the next frame.
                } finally {
                    reader.reset()
                    img.close()
                }
            }
            provider.unbindAll()
            provider.bindToLifecycle(owner, CameraSelector.DEFAULT_BACK_CAMERA, preview, analysis)
            bound.set(provider)
        }, ContextCompat.getMainExecutor(c))
        view
    }, modifier = Modifier.fillMaxSize())
}
