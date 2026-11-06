package info.gluek.passage

import android.app.ActivityManager
import android.app.ApplicationExitInfo
import android.content.Context
import android.os.Build
import android.os.Process
import android.util.AtomicFile
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import java.util.TimeZone

/** Bounded, local history that survives router and process restarts. */
object Diagnostics {
    private const val MAX_EVENTS = 128
    private const val MAX_BATTERY = 288 // 48 hours of 10-minute samples
    private var file: AtomicFile? = null
    private var history = JSONObject()
    private var version = "?"

    @Synchronized
    fun init(context: Context) {
        version = try {
            context.packageManager.getPackageInfo(context.packageName, 0).versionName ?: "?"
        } catch (_: Exception) {
            "?"
        }
        file = AtomicFile(File(context.filesDir, "diagnostics.json"))
        history = try {
            JSONObject(file!!.openRead().bufferedReader(Charsets.UTF_8).use { it.readText() })
        } catch (_: Exception) {
            JSONObject()
        }
    }

    @Synchronized
    fun event(message: String) {
        record(entry(message))
    }

    /** A battery sample (PassageService.sampleBattery), kept for the report. */
    @Synchronized
    fun battery(sample: JSONObject) {
        try {
            sample.put("t", timestamp(System.currentTimeMillis()))
            val old = history.optJSONArray("battery") ?: JSONArray()
            val samples = JSONArray()
            for (i in (old.length() - MAX_BATTERY + 1).coerceAtLeast(0) until old.length()) samples.put(old.getJSONObject(i))
            samples.put(sample)
            history.put("battery", samples)
            save()
        } catch (_: Exception) {
        }
    }

    @Synchronized
    fun error(message: String, error: Throwable, fatal: Boolean = false) {
        try {
            val item = entry("$message: ${error.javaClass.name}: ${error.message.orEmpty()}")
                .put("detail", redact(error.stackTraceToString()).take(8192))
            if (fatal) history.put("last_crash", item)
            record(item)
        } catch (_: Throwable) {
            // Recording an error must not replace the original failure.
        }
    }

    fun collectExits(context: Context) {
        if (Build.VERSION.SDK_INT < 30) return
        try {
            val manager = context.getSystemService(ActivityManager::class.java)
            val exits = JSONArray()
            for (exit in manager.getHistoricalProcessExitReasons(context.packageName, 0, 16)) {
                if (exit.processName != context.packageName) continue
                exits.put(JSONObject()
                    .put("t", timestamp(exit.timestamp))
                    .put("pid", exit.pid)
                    .put("reason", exit.reason)
                    .put("reason_name", exitReason(exit.reason))
                    .put("status", exit.status)
                    .put("description", redact(exit.description.orEmpty()).take(1024)))
                if (exits.length() == 8) break
            }
            synchronized(this) {
                history.put("process_exits", exits)
                save()
            }
        } catch (e: Exception) {
            error("process exit history unavailable", e)
        }
    }

    @Synchronized
    fun snapshot(): JSONObject = try {
        JSONObject(history.toString())
    } catch (_: Exception) {
        JSONObject()
    }

    fun text(context: Context): String {
        val events = snapshot().optJSONArray("events") ?: return ""
        return buildString {
            append('\n').append(context.getString(R.string.diagnostic_events)).append('\n')
            for (i in (events.length() - 32).coerceAtLeast(0) until events.length()) {
                val e = events.getJSONObject(i)
                append("  ${StatusText.clock(e.optString("t"))} [${e.optInt("pid")}] ${e.optString("msg")}\n")
            }
        }
    }

    fun device(): JSONObject = JSONObject()
        .put("manufacturer", Build.MANUFACTURER)
        .put("model", Build.MODEL)
        .put("android", Build.VERSION.RELEASE)
        .put("sdk", Build.VERSION.SDK_INT)
        .put("build", Build.DISPLAY)
        .put("supported_abis", JSONArray(Build.SUPPORTED_ABIS.toList()))
        .put("process_64_bit", Process.is64Bit())

    private fun entry(message: String): JSONObject = JSONObject()
        .put("t", timestamp(System.currentTimeMillis()))
        .put("pid", Process.myPid())
        .put("version", version)
        .put("msg", redact(message).take(1024))

    private fun record(item: JSONObject) {
        try {
            val old = history.optJSONArray("events") ?: JSONArray()
            val events = JSONArray()
            for (i in (old.length() - MAX_EVENTS + 1).coerceAtLeast(0) until old.length()) {
                events.put(old.getJSONObject(i))
            }
            events.put(item)
            history.put("events", events)
            save()
        } catch (_: Exception) {
        }
    }

    private fun save() {
        val target = file ?: return
        var stream: java.io.FileOutputStream? = null
        try {
            stream = target.startWrite()
            stream.write(history.toString().toByteArray(Charsets.UTF_8))
            target.finishWrite(stream)
        } catch (_: Exception) {
            target.failWrite(stream)
        }
    }

    fun redact(text: String): String = text
        .replace(Regex("[A-Za-z][A-Za-z0-9+.-]*://\\S+"), "[link]")
        .replace(Regex("(?i)(\"?(?:read_key|signer|key|pass(?:word)?|user(?:name)?|ticket|zone|link)\"?\\s*[:=]\\s*)(?:\"[^\"]*\"|[^\\s,}]+)"), "$1[redacted]")

    private fun exitReason(reason: Int): String = when (reason) {
        ApplicationExitInfo.REASON_EXIT_SELF -> "exit self"
        ApplicationExitInfo.REASON_SIGNALED -> "signal"
        ApplicationExitInfo.REASON_LOW_MEMORY -> "low memory"
        ApplicationExitInfo.REASON_CRASH -> "Java crash"
        ApplicationExitInfo.REASON_CRASH_NATIVE -> "native crash"
        ApplicationExitInfo.REASON_ANR -> "not responding"
        ApplicationExitInfo.REASON_INITIALIZATION_FAILURE -> "initialization failure"
        ApplicationExitInfo.REASON_PERMISSION_CHANGE -> "permission change"
        ApplicationExitInfo.REASON_EXCESSIVE_RESOURCE_USAGE -> "resource usage"
        ApplicationExitInfo.REASON_USER_REQUESTED -> "user requested"
        ApplicationExitInfo.REASON_USER_STOPPED -> "user stopped"
        ApplicationExitInfo.REASON_DEPENDENCY_DIED -> "dependency died"
        ApplicationExitInfo.REASON_PACKAGE_UPDATED -> "app updated"
        ApplicationExitInfo.REASON_PACKAGE_STATE_CHANGE -> "app state changed"
        ApplicationExitInfo.REASON_FREEZER -> "freezer"
        else -> "other"
    }

    private fun timestamp(time: Long): String =
        SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ss.SSS'Z'", Locale.ROOT).apply {
            timeZone = TimeZone.getTimeZone("UTC")
        }.format(Date(time))
}
