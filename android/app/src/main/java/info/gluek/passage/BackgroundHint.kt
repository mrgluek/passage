package info.gluek.passage

import android.app.Activity
import android.app.ActivityManager
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import java.util.Locale

/**
 * Some phone makers stop background apps beyond what Android itself does, and
 * their settings cannot be read. This picks a short setup hint by maker and
 * notices when the system stopped Passage by its own cleaner.
 */
object BackgroundHint {
    enum class Vendor(val steps: Int) {
        XIAOMI(R.string.bg_steps_xiaomi),
        HUAWEI(R.string.bg_steps_huawei),
        OPPO(R.string.bg_steps_oppo),
        VIVO(R.string.bg_steps_vivo),
        SAMSUNG(R.string.bg_steps_samsung),
        TRANSSION(R.string.bg_steps_transsion),
        OTHER(R.string.bg_steps_other),
    }

    enum class Card { NONE, ADVANCE, KILLED }

    const val WINDOW_MS = 7L * 24 * 3600 * 1000

    fun vendor(manufacturer: String, brand: String): Vendor {
        // Whole words: a maker such as OUKITEL must not match "itel".
        val names = listOf(manufacturer, brand).flatMap { it.lowercase(Locale.ROOT).split(Regex("[^a-z0-9]+")) }
        fun has(vararg words: String) = names.any { it in words }
        return when {
            has("xiaomi", "redmi", "poco") -> Vendor.XIAOMI
            has("huawei", "honor") -> Vendor.HUAWEI
            has("oppo", "realme", "oneplus") -> Vendor.OPPO
            has("vivo", "iqoo") -> Vendor.VIVO
            has("samsung") -> Vendor.SAMSUNG
            has("tecno", "infinix", "itel", "transsion") -> Vendor.TRANSSION
            else -> Vendor.OTHER
        }
    }

    /**
     * Exit reasons 10 (user requested) and 13 (other) both occur for the same
     * cleaner on different devices; the description names it, for example
     * "OneKeyClean" or "SwipeUpClean".
     */
    fun stoppedByCleaner(reason: Int, description: String): Boolean =
        (reason == 10 || reason == 13) && description.contains("clean", ignoreCase = true)

    /** The time of the newest cleaner stop in the last week, or 0. Needs Android 11. */
    fun lastCleanerKill(context: Context, now: Long = System.currentTimeMillis()): Long {
        if (Build.VERSION.SDK_INT < 30) return 0
        return try {
            context.getSystemService(ActivityManager::class.java)
                .getHistoricalProcessExitReasons(context.packageName, 0, 16)
                .filter {
                    it.processName == context.packageName && now - it.timestamp <= WINDOW_MS &&
                        stoppedByCleaner(it.reason, it.description.orEmpty())
                }
                .maxOfOrNull { it.timestamp } ?: 0
        } catch (_: Exception) {
            0
        }
    }

    fun card(hasLink: Boolean, active: Boolean, vendor: Vendor, advanceDismissed: Boolean, killTime: Long, killSeen: Long): Card = when {
        !hasLink -> Card.NONE
        killTime > killSeen -> Card.KILLED
        active && vendor != Vendor.OTHER && !advanceDismissed -> Card.ADVANCE
        else -> Card.NONE
    }

    /** Vendor screens are undocumented and change between versions, so the app's own settings page is the fallback. */
    fun openSettings(activity: Activity, vendor: Vendor) {
        val candidates = mutableListOf<Intent>()
        if (vendor == Vendor.XIAOMI) candidates += Intent().setComponent(
            ComponentName("com.miui.securitycenter", "com.miui.permcenter.autostart.AutoStartManagementActivity"))
        candidates += Intent(android.provider.Settings.ACTION_APPLICATION_DETAILS_SETTINGS)
            .setData(Uri.parse("package:${activity.packageName}"))
        for (intent in candidates) {
            try {
                activity.startActivity(intent)
                return
            } catch (_: Exception) {
                // Try the next screen.
            }
        }
    }
}
