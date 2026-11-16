package info.gluek.passage

import android.content.Context
import android.content.Intent
import android.content.pm.ApplicationInfo
import java.io.File
import java.security.MessageDigest

/**
 * Only in a debuggable build (never in a release APK): fakes an update so the
 * update card can be seen without a release, for example
 * `am start -n info.gluek.passage/.MainActivity --es debug_update available`.
 * Values: available, downloading, ready, failed, clear; `--es debug_update_version`
 * sets the offered version, `--ez debug_update_fail true` makes the fake download
 * end with a bad file. The fake "release" is a copy of the installed APK, written
 * slowly and checked with the normal hash check; nothing goes to the network.
 */
object UpdateDebug {
    @Volatile var active = false
        private set
    @Volatile var progress: Pair<Long, Long>? = null
        private set
    @Volatile private var canceled = false
    @Volatile private var corrupt = false

    private fun debuggable(context: Context) =
        context.applicationInfo.flags and ApplicationInfo.FLAG_DEBUGGABLE != 0

    fun apply(context: Context, intent: Intent?) {
        val state = intent?.getStringExtra("debug_update") ?: return
        if (!debuggable(context)) return
        val settings = Settings(context)
        if (Updater.downloading) cancel()
        Updater.begin()
        Updater.ready = null
        Updater.checked = false
        settings.dismissedUpdate = ""
        if (state == "clear") {
            active = false
            Updater.offer = null
            settings.availableVersion = ""
            Updater.clear(context)
            return
        }
        active = true
        corrupt = intent.getBooleanExtra("debug_update_fail", false)
        val apk = File(context.applicationInfo.sourceDir)
        val version = intent.getStringExtra("debug_update_version") ?: "9.9.9"
        val offer = Updater.Offer(version, "debug-update.apk", apk.length(), sha256(apk))
        Updater.offer = offer
        settings.availableVersion = version
        when (state) {
            "ready" -> Updater.ready = copy(context, apk, offer, 0)
            "failed" -> Updater.fail(context.getString(R.string.update_download_failed), IllegalStateException("debug failure"))
            "downloading" -> Updater.startDownload(context) {}
        }
    }

    fun cancel() {
        canceled = true
    }

    /** Stands in for [Updater.download] while [active]: same result checks, no network. */
    fun download(context: Context, offer: Updater.Offer): File {
        check(active && debuggable(context))
        canceled = false
        val file = copy(context, File(context.applicationInfo.sourceDir), offer, 15_000)
        if (corrupt) file.appendText("x")
        check(Updater.whileVerifying { Thread.sleep(1_500); Updater.verified(file, offer) }) { "Invalid update file" }
        return file
    }

    private fun copy(context: Context, apk: File, offer: Updater.Offer, millis: Long): File {
        val dir = File(context.cacheDir, "update").apply { mkdirs() }
        val dest = File(dir, offer.name)
        val total = apk.length()
        val step = maxOf(total / 100, 1)
        var done = 0L
        try {
            apk.inputStream().use { input ->
                dest.outputStream().use { output ->
                    val buffer = ByteArray(step.toInt().coerceAtMost(1 shl 20))
                    while (true) {
                        if (canceled) throw IllegalStateException("canceled")
                        val n = input.read(buffer)
                        if (n < 0) break
                        output.write(buffer, 0, n)
                        done += n
                        progress = done to total
                        if (millis > 0) Thread.sleep(millis * n / total)
                    }
                }
            }
        } finally {
            progress = null
        }
        return dest
    }

    private fun sha256(file: File): String {
        val digest = MessageDigest.getInstance("SHA-256")
        file.inputStream().use { input ->
            val buffer = ByteArray(64 * 1024)
            while (true) {
                val n = input.read(buffer)
                if (n < 0) break
                digest.update(buffer, 0, n)
            }
        }
        return digest.digest().joinToString("") { "%02x".format(it.toInt() and 0xff) }
    }
}
