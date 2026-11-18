package info.gluek.passage

import android.app.Activity
import android.os.Handler
import android.os.Looper
import android.text.format.Formatter
import android.view.Gravity
import android.view.View
import android.widget.Button
import android.widget.CheckBox
import android.widget.LinearLayout
import android.widget.ProgressBar
import android.widget.TextView

/**
 * The update controls: a card on the main screen ([home]) or the Updates part
 * of the settings screen. Both run the same steps in [Updater], where the state
 * lives, so a download started on one screen shows on the other.
 */
class UpdateSection(private val activity: Activity, private val home: Boolean) {
    private val main = Handler(Looper.getMainLooper())
    private val settings = Settings(activity)
    private val title = Ui.cardHeading(activity, "")
    private val state: TextView = if (home) Ui.hint(activity, "").apply {
        setPadding(0, Ui.dp(activity, 8), 0, Ui.dp(activity, 12))
    } else TextView(activity).apply { setPadding(0, Ui.dp(activity, 4), 0, 0) }
    private val detail = Ui.hint(activity, "").apply { setTextIsSelectable(true) }
    private val bar = ProgressBar(activity, null, android.R.attr.progressBarStyleHorizontal).apply { max = 1000 }
    private val check = Ui.outlinedButton(activity, activity.getString(R.string.update_check)) { check() }
    private val action: Button = if (home) Ui.primaryButton(activity, "") {} else Ui.outlinedButton(activity, "") {}
    private val secondary = Ui.textButton(activity, "") {}

    val view: LinearLayout = if (home) Ui.card(activity).apply {
        visibility = View.GONE
        addView(title)
        addView(state)
        addView(bar, LinearLayout.LayoutParams(-1, -2).apply { bottomMargin = Ui.dp(activity, 12) })
        addView(action, LinearLayout.LayoutParams(-1, -2))
        addView(secondary, LinearLayout.LayoutParams(-2, -2).apply {
            gravity = Gravity.CENTER_HORIZONTAL
            topMargin = Ui.dp(activity, 4)
        })
    } else LinearLayout(activity).apply {
        orientation = LinearLayout.VERTICAL
        addView(Ui.heading(activity, activity.getString(R.string.updates_label)).apply {
            setPadding(0, 0, 0, Ui.dp(activity, 8))
        })
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
        // The app was restarted since the check that found this version. The
        // main screen does not check on its own: its Download button does.
        if (!home && Updater.offer == null && !Updater.checking && Updater.pending(activity) != null) check()
        main.removeCallbacks(poll)
        main.post(poll)
    }

    fun pause() = main.removeCallbacks(poll)

    fun render() {
        val s = Updater.snapshot(activity)
        val stage = UpdateFlow.stage(s)
        if (home) {
            view.visibility = if (UpdateFlow.homeVisible(s, settings.dismissedUpdate)) View.VISIBLE else View.GONE
            if (view.visibility != View.VISIBLE) return
            title.text = activity.getString(R.string.update_available, s.available ?: "")
        } else {
            check.isEnabled = !s.checking && !s.downloading
            detail.text = bullets(Updater.detail ?: "")
            detail.visibility = if (Updater.detail == null) View.GONE else View.VISIBLE
        }
        val busy = stage == UpdateFlow.Stage.DOWNLOADING || stage == UpdateFlow.Stage.VERIFYING ||
            (home && stage == UpdateFlow.Stage.CHECKING)
        bar.visibility = if (busy) View.VISIBLE else View.GONE
        state.text = text(stage, s)
        val act = UpdateFlow.action(s)
        val label = when {
            act == UpdateFlow.Action.NONE || (home && act == UpdateFlow.Action.CANCEL) -> null
            act == UpdateFlow.Action.CANCEL -> activity.getString(android.R.string.cancel)
            s.failed && home -> activity.getString(R.string.home_try_again)
            act == UpdateFlow.Action.INSTALL -> activity.getString(R.string.update_install)
            home -> Updater.offer?.let { activity.getString(R.string.update_download_size, size(it.size)) }
                ?: activity.getString(R.string.update_download)
            else -> activity.getString(R.string.update_download)
        }
        action.visibility = if (label == null) View.GONE else View.VISIBLE
        if (label != null) {
            action.text = label
            action.setOnClickListener { perform(act) }
        }
        if (home) {
            val second = when {
                act == UpdateFlow.Action.CANCEL -> android.R.string.cancel to { Updater.cancel() }
                UpdateFlow.canDismiss(s) -> R.string.update_skip to { dismiss(s.available) }
                else -> null
            }
            secondary.visibility = if (second == null) View.GONE else View.VISIBLE
            if (second != null) {
                secondary.setText(second.first)
                secondary.setOnClickListener { second.second() }
            }
        }
    }

    private fun text(stage: UpdateFlow.Stage, s: UpdateFlow.Snapshot): String = when (stage) {
        UpdateFlow.Stage.CHECKING -> activity.getString(R.string.update_checking).also { bar.isIndeterminate = true }
        UpdateFlow.Stage.DOWNLOADING -> progress(s)
        UpdateFlow.Stage.VERIFYING -> activity.getString(R.string.update_verifying).also { bar.isIndeterminate = true }
        UpdateFlow.Stage.FAILED -> Updater.error ?: ""
        UpdateFlow.Stage.READY -> if (home) activity.getString(R.string.update_home_ready)
            else activity.getString(R.string.update_ready, s.available ?: "")
        UpdateFlow.Stage.AVAILABLE -> if (home) activity.getString(R.string.update_home_text)
            else activity.getString(R.string.update_available, s.available) + (Updater.offer?.let { " (" + size(it.size) + ")" } ?: "")
        UpdateFlow.Stage.LATEST -> activity.getString(R.string.update_latest, Updater.version(activity))
        UpdateFlow.Stage.IDLE -> activity.getString(R.string.update_version, Updater.version(activity))
    }

    private fun progress(s: UpdateFlow.Snapshot): String {
        val (done, total) = Updater.progress()
        val permille = UpdateFlow.permille(done, total)
        bar.isIndeterminate = permille < 0
        if (permille >= 0) bar.progress = permille
        val line = if (home) activity.getString(R.string.update_home_downloading)
            else activity.getString(R.string.update_downloading, s.available ?: "")
        return if (permille < 0) line else line + "\n" + size(done) + " / " + size(total)
    }

    private fun perform(act: UpdateFlow.Action) {
        when (act) {
            UpdateFlow.Action.DOWNLOAD -> Updater.startDownload(activity) { main.post { render() } }
            UpdateFlow.Action.INSTALL -> Updater.installReady(activity)
            UpdateFlow.Action.CANCEL -> Updater.cancel()
            UpdateFlow.Action.NONE -> {}
        }
        render()
        main.removeCallbacks(poll)
        main.post(poll)
    }

    private fun dismiss(version: String?) {
        settings.dismissedUpdate = version ?: return
        render()
    }

    /** One failure per source: several become a list. */
    private fun bullets(text: String): String {
        val lines = text.lines().filter { it.isNotBlank() }
        return if (lines.size > 1) lines.joinToString("\n") { "• $it" } else text
    }

    private fun size(bytes: Long) = Formatter.formatShortFileSize(activity, bytes)

    private fun check() {
        Updater.startCheck(activity) { main.post { render() } }
        render()
        main.removeCallbacks(poll)
        main.post(poll)
    }

    companion object {
        private const val POLL_MS = 500L
    }
}
