package info.gluek.passage

import android.app.Activity
import android.content.Intent
import android.content.pm.PackageInstaller
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.text.format.Formatter
import android.view.MenuItem
import android.view.View
import android.widget.Button
import android.widget.CheckBox
import android.widget.ProgressBar
import android.widget.TextView
import java.io.File
import kotlin.concurrent.thread

/**
 * Checks for a new version, downloads it and starts the install. Not
 * exported: the installer's result intent names the next screen to open.
 */
class UpdateActivity : Activity() {
    private val main = Handler(Looper.getMainLooper())
    private lateinit var state: TextView
    private lateinit var bar: ProgressBar
    private lateinit var action: Button

    private val poll = object : Runnable {
        override fun run() {
            if (!downloading) return
            try {
                val p = Native.json(Native.updateProgress())
                showProgress(p.optLong("done"), p.optLong("total"))
            } catch (_: Throwable) {
            }
            main.postDelayed(this, POLL_MS)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        if (!downloading && ready == null) {
            offer = null
            error = null
            checked = false
        }
        actionBar?.setDisplayHomeAsUpEnabled(true)
        title = getString(R.string.updates_label)
        val settings = Settings(this)
        val col = Ui.column(this)
        state = TextView(this)
        col.addView(state)
        bar = ProgressBar(this, null, android.R.attr.progressBarStyleHorizontal).apply {
            max = 1000
            setPadding(0, Ui.dp(this@UpdateActivity, 8), 0, 0)
        }
        col.addView(bar)
        action = Button(this)
        col.addView(action)
        col.addView(CheckBox(this).apply {
            text = getString(R.string.update_auto)
            isChecked = settings.updateChecks
            setOnCheckedChangeListener { _, on -> settings.updateChecks = on }
        })
        col.addView(Ui.hint(this, getString(R.string.update_auto_help)))
        setContentView(Ui.scroll(this, col))
        handleResult(intent)
        render()
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handleResult(intent)
    }

    override fun onResume() {
        super.onResume()
        render()
        if (downloading) main.post(poll)
        else if (offer == null && error == null && !checking && !checked) check()
    }

    override fun onPause() {
        main.removeCallbacks(poll)
        super.onPause()
    }

    override fun onOptionsItemSelected(item: MenuItem): Boolean {
        if (item.itemId == android.R.id.home) {
            finish()
            return true
        }
        return super.onOptionsItemSelected(item)
    }

    private fun render() {
        val current = Updater.version(this)
        val o = offer
        val size = o?.let { Formatter.formatShortFileSize(this, it.size) }
        bar.visibility = if (downloading) View.VISIBLE else View.GONE
        action.isEnabled = !checking
        state.text = when {
            checking -> getString(R.string.update_checking)
            downloading -> getString(R.string.update_downloading, o?.version ?: "")
            error != null -> getString(R.string.update_failed, error)
            ready != null -> getString(R.string.update_ready, o?.version ?: "")
            o != null -> getString(R.string.update_available, o.version) + " ($size)"
            else -> getString(R.string.update_latest, current)
        }
        when {
            downloading -> button(android.R.string.cancel) { Native.updateCancel() }
            ready != null -> button(R.string.update_install) { install() }
            o != null -> button(R.string.update_download) { download() }
            else -> button(R.string.update_check) { check() }
        }
    }

    private fun button(label: Int, click: () -> Unit) {
        action.setText(label)
        action.setOnClickListener { click() }
    }

    private fun showProgress(done: Long, total: Long) {
        if (total > 0) bar.progress = (done * 1000 / total).toInt()
        state.text = getString(R.string.update_downloading, offer?.version ?: "") + "\n" +
            Formatter.formatShortFileSize(this, done) + " / " + Formatter.formatShortFileSize(this, total)
    }

    private fun check() {
        checking = true
        error = null
        ready = null
        render()
        val app = applicationContext
        thread(name = "passage-update-check") {
            val outcome = runCatching { Updater.check(app) }
            main.post {
                checking = false
                checked = true
                offer = outcome.getOrNull()
                error = outcome.exceptionOrNull()?.let { it.message ?: it.toString() }
                render()
            }
        }
    }

    private fun download() {
        val o = offer ?: return
        downloading = true
        error = null
        render()
        main.post(poll)
        val app = applicationContext
        thread(name = "passage-update-download") {
            val outcome = runCatching { Updater.download(app, o) }
            main.post {
                downloading = false
                ready = outcome.getOrNull()
                error = outcome.exceptionOrNull()?.let { it.message ?: it.toString() }
                    ?.takeIf { "canceled" !in it }
                Diagnostics.event("update download: ${error ?: "done"}")
                render()
            }
        }
    }

    private fun install() {
        val apk = ready ?: return
        try {
            Updater.install(this, apk)
        } catch (e: Exception) {
            error = e.message ?: e.toString()
            render()
        }
    }

    /** The installer's answer: open its confirmation, or show why it failed. */
    private fun handleResult(intent: Intent?) {
        if (intent?.action != Updater.ACTION_INSTALL_RESULT) return
        setIntent(Intent(this, UpdateActivity::class.java))
        when (val status = intent.getIntExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE)) {
            PackageInstaller.STATUS_PENDING_USER_ACTION -> {
                @Suppress("DEPRECATION")
                intent.getParcelableExtra<Intent>(Intent.EXTRA_INTENT)?.let { startActivity(it) }
            }
            PackageInstaller.STATUS_SUCCESS -> Updater.clear(this)
            PackageInstaller.STATUS_FAILURE_ABORTED -> {} // the user said no
            else -> {
                val message = intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE)
                error = message ?: status.toString()
                Diagnostics.event("update install failed: $error")
                render()
            }
        }
    }

    companion object {
        private const val POLL_MS = 500L

        // Survive leaving the screen: the download goes on meanwhile.
        @Volatile private var checking = false
        @Volatile private var checked = false
        @Volatile private var downloading = false
        @Volatile private var offer: Updater.Offer? = null
        @Volatile private var ready: File? = null
        @Volatile private var error: String? = null
    }
}
