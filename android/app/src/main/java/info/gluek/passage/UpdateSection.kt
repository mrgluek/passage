package info.gluek.passage

import android.app.Activity
import android.os.Handler
import android.os.Looper
import android.text.format.Formatter
import android.view.View
import android.widget.Button
import android.widget.CheckBox
import android.widget.LinearLayout
import android.widget.ProgressBar
import android.widget.TextView
import kotlin.concurrent.thread

/** The Updates part of the settings screen. State lives in [Updater]. */
class UpdateSection(private val activity: Activity) {
    private val main = Handler(Looper.getMainLooper())
    private val settings = Settings(activity)
    private val state = TextView(activity).apply { setPadding(0, Ui.dp(activity, 4), 0, 0) }
    private val detail = Ui.hint(activity, "").apply { setTextIsSelectable(true) }
    private val bar = ProgressBar(activity, null, android.R.attr.progressBarStyleHorizontal).apply { max = 1000 }
    private val check = Button(activity).apply {
        setText(R.string.update_check)
        setOnClickListener { check() }
    }
    private val action = Button(activity)

    val view: LinearLayout = LinearLayout(activity).apply {
        orientation = LinearLayout.VERTICAL
        addView(Ui.heading(activity, activity.getString(R.string.updates_label)))
        addView(check)
        addView(state)
        addView(detail)
        addView(bar)
        addView(action)
        addView(CheckBox(activity).apply {
            text = activity.getString(R.string.update_auto)
            isChecked = settings.updateChecks
            setOnCheckedChangeListener { _, on -> settings.updateChecks = on }
        })
        addView(Ui.hint(activity, activity.getString(R.string.update_auto_help)))
    }

    private val poll = object : Runnable {
        override fun run() {
            render()
            if (Updater.downloading || Updater.checking) main.postDelayed(this, POLL_MS)
        }
    }

    fun resume() {
        render()
        // The app was restarted since the check that found this version.
        if (Updater.offer == null && !Updater.checking && Updater.pending(activity) != null) check()
        main.post(poll)
    }

    fun pause() = main.removeCallbacks(poll)

    private fun render() {
        val offer = Updater.offer
        val error = Updater.error
        val busy = Updater.checking || Updater.downloading
        check.isEnabled = !busy
        bar.visibility = if (Updater.downloading) View.VISIBLE else View.GONE
        detail.text = bullets(Updater.detail ?: "")
        detail.visibility = if (Updater.detail == null) View.GONE else View.VISIBLE
        state.text = when {
            Updater.checking -> activity.getString(R.string.update_checking)
            Updater.downloading -> progress(offer)
            error != null -> error
            Updater.ready != null -> activity.getString(R.string.update_ready, offer?.version ?: "")
            offer != null -> activity.getString(R.string.update_available, offer.version) + " (" + size(offer.size) + ")"
            Updater.checked -> activity.getString(R.string.update_latest, Updater.version(activity))
            else -> activity.getString(R.string.update_version, Updater.version(activity))
        }
        val (label, click) = when {
            Updater.downloading -> android.R.string.cancel to { Native.updateCancel() }
            Updater.ready != null -> R.string.update_install to { install() }
            offer != null && !Updater.checking -> R.string.update_download to { download() }
            else -> 0 to {}
        }
        action.visibility = if (label == 0) View.GONE else View.VISIBLE
        if (label != 0) {
            action.setText(label)
            action.setOnClickListener { click() }
        }
    }

    /** One failure per source: several become a list. */
    private fun bullets(text: String): String {
        val lines = text.lines().filter { it.isNotBlank() }
        return if (lines.size > 1) lines.joinToString("\n") { "• $it" } else text
    }

    private fun size(bytes: Long) = Formatter.formatShortFileSize(activity, bytes)

    private fun progress(offer: Updater.Offer?): String {
        var line = activity.getString(R.string.update_downloading, offer?.version ?: "")
        try {
            val p = Native.json(Native.updateProgress())
            val total = p.optLong("total")
            if (total > 0) {
                bar.progress = (p.optLong("done") * 1000 / total).toInt()
                line += "\n" + size(p.optLong("done")) + " / " + size(total)
            }
        } catch (_: Throwable) {
        }
        return line
    }

    private fun check() {
        Updater.begin()
        Updater.checking = true
        render()
        main.post(poll)
        val app = activity.applicationContext
        thread(name = "passage-update-check") {
            val outcome = runCatching { Updater.check(app) }
            Updater.checking = false
            Updater.checked = true
            outcome.exceptionOrNull()?.let { Updater.fail(app.getString(R.string.update_check_failed), it) }
            main.post { render() }
        }
    }

    private fun download() {
        val offer = Updater.offer ?: return
        Updater.begin()
        Updater.downloading = true
        render()
        main.post(poll)
        val app = activity.applicationContext
        thread(name = "passage-update-download") {
            val outcome = runCatching { Updater.download(app, offer) }
            Updater.downloading = false
            Updater.ready = outcome.getOrNull()
            outcome.exceptionOrNull()?.takeIf { "canceled" !in (it.message ?: "") }?.let {
                Updater.fail(app.getString(R.string.update_download_failed), it)
            }
            main.post { render() }
        }
    }

    private fun install() {
        val apk = Updater.ready ?: return
        Updater.begin()
        try {
            Updater.install(activity, apk)
        } catch (e: Exception) {
            Updater.fail(activity.getString(R.string.update_install_failed), e)
            render()
        }
    }

    companion object {
        private const val POLL_MS = 500L
    }
}
