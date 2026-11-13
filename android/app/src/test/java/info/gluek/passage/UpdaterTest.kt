package info.gluek.passage

import java.io.File
import java.security.MessageDigest
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class UpdaterTest {
    @Test fun cachedFileMustMatchSignedHash() {
        val file = File.createTempFile("update", ".apk")
        try {
            val data = "valid".toByteArray()
            val hash = MessageDigest.getInstance("SHA-256").digest(data)
                .joinToString("") { "%02x".format(it.toInt() and 0xff) }
            val offer = Updater.Offer("1.2.0", "app.apk", data.size.toLong(), hash)
            file.writeBytes(data)
            assertTrue(Updater.verified(file, offer))
            file.writeText("other")
            assertFalse(Updater.verified(file, offer))
            file.writeText("shorter file")
            assertFalse(Updater.verified(file, offer))
            file.delete()
            assertFalse(Updater.verified(file, offer))
        } finally {
            file.delete()
        }
    }
}
