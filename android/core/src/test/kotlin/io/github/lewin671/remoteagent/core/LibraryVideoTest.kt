package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.assertMessageEquals as assertEquals

import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** session-files 「从相册选视频」: videos from the library go as files, beside the images. */
class LibraryVideoTest {
    @Test fun aVideoIsKnownByItsTypeOrElseItsName() {
        assertTrue(isVideo("video/mp4", "clip"))
        assertFalse(isVideo("image/jpeg", "photo.mp4"))
        assertTrue(isVideo(null, "IMG_0001.MOV"))
        assertFalse(isVideo(null, "notes.txt"))
        assertFalse(isVideo(null, "mp4"))
    }

    @Test fun aVideoOverTheLimitIsTurnedAwayWhenPicked() {
        assertNull(pickedVideoError("a.mp4", MAX_FILE_BYTES, MAX_FILE_BYTES))
        assertEquals("「a.mp4」超过单个文件 100 MB 的上限", pickedVideoError("a.mp4", MAX_FILE_BYTES + 1, MAX_FILE_BYTES))
        // An unknown size is checked once the bytes are read.
        assertNull(pickedVideoError("a.mp4", 0, MAX_FILE_BYTES))
    }

    @Test fun theLibraryOffersVideosOnlyWhileFilesFit() {
        assertEquals(3, libraryRoom(images = 6, maxImages = 9, files = 0, maxFiles = 6, videos = false))
        assertEquals(9, libraryRoom(images = 6, maxImages = 9, files = 0, maxFiles = 6, videos = true))
        assertEquals(1, libraryRoom(images = 9, maxImages = 9, files = 5, maxFiles = 6, videos = true))
        assertEquals(0, libraryRoom(images = 9, maxImages = 9, files = 6, maxFiles = 6, videos = true))
    }

    @Test fun lengthsReadLikeAClock() {
        assertEquals("0:00", videoLength(0))
        assertEquals("0:42", videoLength(42_300))
        assertEquals("12:05", videoLength(725_000))
        assertEquals("1:02:03", videoLength(3_723_000))
    }
}
