package info.gluek.passage

import android.app.ActivityOptions
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInstaller
import android.os.Build
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.util.Random

/** Checks for, downloads and installs new versions; the work is in the core. */
object Updater {
    /** Opens the settings screen at the Updates part. */
    const val ACTION_SHOW = "info.gluek.passage.SHOW_UPDATES"
    private const val CHANNEL = "updates"
    private const val NOTIFICATION_ID = 2
    private const val CHECK_INTERVAL_MS = 20 * 3_600_000L
    private const val CHECK_JITTER_MS = 8 * 3_600_000L

    data class Offer(val version: String, val name: String, val size: Long)

    // Survives leaving the screen: a download goes on meanwhile.
    @Volatile var offer: Offer? = null
    @Volatile var checking = false
    @Volatile var checked = false
    @Volatile var downloading = false
    @Volatile var ready: File? = null
    @Volatile var error: String? = null
    @Volatile var detail: String? = null

    fun begin() {
        error = null
        detail = null
    }

    fun fail(message: String, cause: Throwable) {
        error = message
        detail = cause.message ?: cause.toString()
        Diagnostics.event("update failed: $detail")
    }

    /** The newer version last found, after an app restart too. */
    fun pending(context: Context): String? =
        Settings(context).availableVersion.takeIf { keyed && newer(it, version(context)) }

    private val keyed by lazy { enabled() }

    /** Compares dotted numbers; anything after them, like -beta1, is ignored. */
    private fun newer(a: String, b: String): Boolean {
        fun parts(v: String) = v.substringBefore('-').split('.').map { it.toIntOrNull() ?: 0 }
        val x = parts(a)
        val y = parts(b)
        for (i in 0 until maxOf(x.size, y.size)) {
            val p = x.getOrElse(i) { 0 }
            val q = y.getOrElse(i) { 0 }
            if (p != q) return p > q
        }
        return false
    }

    /** This build can update itself: it has the update key. */
    fun enabled(): Boolean = try {
        Native.json(Native.versions()).optString("updates").isNotEmpty()
    } catch (_: Throwable) {
        false
    }

    /** The release file for this phone: the first ABI that has one. */
    private fun target(): String = Build.SUPPORTED_ABIS
        .firstNotNullOfOrNull { mapOf("arm64-v8a" to "arm64", "armeabi-v7a" to "armv7")[it] } ?: "arm64"

    fun version(context: Context): String =
        context.packageManager.getPackageInfo(context.packageName, 0).versionName ?: ""

    /** Blocks. The newer release, or null when this is the latest one. */
    fun check(context: Context): Offer? {
        val request = JSONObject().put("version", version(context)).put("target", target())
            .put("sources", linkSources(context))
        val out = Native.json(Native.updateCheck(request.toString().toByteArray(Charsets.UTF_8)))
        out.optString("error").takeIf { it.isNotEmpty() }?.let { throw IllegalStateException(it) }
        val file = out.optJSONObject("file")
        val found = file?.let { Offer(out.getString("version"), it.getString("name"), it.getLong("size")) }
        if (found != offer) ready = null
        offer = found
        Settings(context).availableVersion = found?.version ?: ""
        return found
    }

    private fun linkSources(context: Context): JSONArray {
        val config = Settings(context).routesConfig
        if (config.isEmpty()) return JSONArray()
        return try {
            JSONObject(config).optJSONArray("update_sources") ?: JSONArray()
        } catch (_: Exception) {
            JSONArray()
        }
    }

    private fun directory(context: Context) = File(context.cacheDir, "update")

    /** Blocks until the file is complete. Other versions' files are removed. */
    fun download(context: Context, offer: Offer): File {
        val dir = directory(context).apply { mkdirs() }
        val dest = File(dir, offer.name)
        dir.listFiles()?.filter { !it.name.startsWith(offer.name) }?.forEach { it.delete() }
        if (dest.length() == offer.size) return dest
        val out = Native.json(Native.updateDownload(dest.path.toByteArray(Charsets.UTF_8)))
        out.optString("error").takeIf { it.isNotEmpty() }?.let { throw IllegalStateException(it) }
        return dest
    }

    fun clear(context: Context) {
        directory(context).deleteRecursively()
    }

    /**
     * Hands the APK to Android, which asks the user. The answer comes to
     * InstallResultActivity, the only place that trusts its extras.
     */
    fun install(context: Context, apk: File) {
        val installer = context.packageManager.packageInstaller
        val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL)
            .apply { setAppPackageName(context.packageName); setSize(apk.length()) }
        val id = installer.createSession(params)
        installer.openSession(id).use { session ->
            apk.inputStream().use { input ->
                session.openWrite("update.apk", 0, apk.length()).use { output ->
                    input.copyTo(output)
                    session.fsync(output)
                }
            }
            val result = Intent(context, InstallResultActivity::class.java)
            // Mutable: the installer adds the outcome to this intent.
            val flags = PendingIntent.FLAG_UPDATE_CURRENT or
                if (Build.VERSION.SDK_INT >= 31) PendingIntent.FLAG_MUTABLE else 0
            // Android blocks this start otherwise: the system sends the answer.
            val options = if (Build.VERSION.SDK_INT >= 34) ActivityOptions.makeBasic()
                .setPendingIntentCreatorBackgroundActivityStartMode(ActivityOptions.MODE_BACKGROUND_ACTIVITY_START_ALLOWED)
                .toBundle() else null
            session.commit(PendingIntent.getActivity(context, id, result, flags, options).intentSender)
        }
    }

    fun due(context: Context): Boolean {
        val settings = Settings(context)
        return settings.updateChecks && System.currentTimeMillis() >= settings.nextUpdateCheck
    }

    /** The daily check. Blocks, so not on the service's worker. */
    fun checkNow(context: Context) {
        val settings = Settings(context)
        settings.nextUpdateCheck = System.currentTimeMillis() + CHECK_INTERVAL_MS + Random().nextInt(CHECK_JITTER_MS.toInt())
        if (!enabled()) return
        try {
            val offer = check(context) ?: return
            Diagnostics.event("update available: ${offer.version}")
            if (settings.notifiedVersion != offer.version) {
                notify(context, offer)
                settings.notifiedVersion = offer.version
            }
        } catch (e: Throwable) {
            Diagnostics.event("update check failed: ${e.message}")
        }
    }

    private fun notify(context: Context, offer: Offer) {
        val localized = AppLanguage.localizedContext(context)
        val nm = context.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        val b = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            if (nm.getNotificationChannel(CHANNEL) == null) {
                nm.createNotificationChannel(NotificationChannel(CHANNEL, localized.getString(R.string.updates_label), NotificationManager.IMPORTANCE_DEFAULT))
            }
            Notification.Builder(context, CHANNEL)
        } else {
            @Suppress("DEPRECATION")
            Notification.Builder(context)
        }
        val open = PendingIntent.getActivity(
            context, 2, Intent(context, SettingsActivity::class.java).setAction(ACTION_SHOW),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        nm.notify(NOTIFICATION_ID, b.setSmallIcon(R.drawable.ic_notification)
            .setContentTitle(localized.getString(R.string.update_available, offer.version))
            .setContentText(localized.getString(R.string.update_tap))
            .setContentIntent(open)
            .setAutoCancel(true)
            .build())
    }
}
