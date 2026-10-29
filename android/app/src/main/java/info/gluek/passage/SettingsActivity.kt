package info.gluek.passage

import android.app.Activity
import android.content.Intent
import android.os.Bundle
import android.text.InputType
import android.view.MenuItem
import android.view.View
import android.widget.Button
import android.widget.CheckBox
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.RadioButton
import android.widget.RadioGroup
import android.widget.TextView
import android.widget.Toast
import java.util.concurrent.Executors

/**
 * The link (pasted or opened once, kept in app-private storage), probe hosts,
 * routing mode, proxy access and startup. A valid link stays folded so its
 * key is not on screen.
 */
class SettingsActivity : Activity() {
    private val worker = Executors.newSingleThreadExecutor()
    private lateinit var settings: Settings

    private lateinit var linkInfo: TextView
    private lateinit var changeButton: Button
    private lateinit var linkBox: LinearLayout
    private lateinit var linkEdit: EditText
    private lateinit var probeEdit: EditText
    private lateinit var batteryButton: Button

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        settings = Settings(this)
        actionBar?.setDisplayHomeAsUpEnabled(true)
        setContentView(buildUi())
        linkEdit.setText(settings.link)
        probeEdit.setText(settings.probeHosts)
        // Folded at once when a link is saved, so the key never flashes on
        // screen while describeLink checks it (a bad link unfolds).
        setLinkOpen(settings.link.isEmpty())
        describeLink()
        takeLink(intent)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        takeLink(intent)
    }

    override fun onResume() {
        super.onResume()
        batteryButton.visibility = if (Ui.batteryExempt(this)) View.GONE else View.VISIBLE
    }

    override fun onDestroy() {
        worker.shutdown()
        super.onDestroy()
    }

    override fun onOptionsItemSelected(item: MenuItem): Boolean {
        if (item.itemId == android.R.id.home) {
            finish()
            return true
        }
        return super.onOptionsItemSelected(item)
    }

    /** A tapped passage:// link goes into the field; Save stores it. */
    private fun takeLink(intent: Intent?) {
        val link = intent?.takeIf { it.action == Intent.ACTION_VIEW }?.dataString ?: return
        setIntent(Intent(this, SettingsActivity::class.java))
        linkEdit.setText(link)
        setLinkOpen(true)
    }

    private fun buildUi(): View {
        val col = Ui.column(this)

        col.addView(Ui.heading(this, getString(R.string.link_label)).apply { setPadding(0, 0, 0, paddingBottom) })
        linkInfo = Ui.hint(this, "")
        col.addView(linkInfo)
        changeButton = Button(this).apply {
            text = getString(R.string.change_link)
            setOnClickListener { setLinkOpen(true) }
        }
        col.addView(changeButton)

        linkBox = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        col.addView(linkBox)
        linkEdit = EditText(this).apply {
            hint = getString(R.string.link_hint)
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_MULTI_LINE or
                InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS or InputType.TYPE_TEXT_VARIATION_URI
            minLines = 2
            maxLines = 4
            textSize = 13f
        }
        linkBox.addView(linkEdit)
        linkBox.addView(Ui.heading(this, getString(R.string.probe_label)))
        linkBox.addView(Ui.hint(this, getString(R.string.probe_help)))
        probeEdit = EditText(this).apply {
            hint = getString(R.string.probe_hint)
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS or
                InputType.TYPE_TEXT_VARIATION_URI
            isSingleLine = true
            textSize = 13f
        }
        linkBox.addView(probeEdit)
        linkBox.addView(CheckBox(this).apply {
            text = getString(R.string.short_names)
            textSize = 13f
            isChecked = settings.shortNames
            setOnCheckedChangeListener { _, on -> settings.shortNames = on }
        })
        linkBox.addView(Button(this).apply {
            text = getString(R.string.save)
            setOnClickListener { onSave() }
        })

        col.addView(Ui.heading(this, getString(R.string.route_label)))
        val modeGroup = RadioGroup(this).apply { orientation = RadioGroup.VERTICAL }
        MODE_LABELS.forEachIndexed { i, label ->
            modeGroup.addView(RadioButton(this).apply {
                id = i + 1
                text = getString(label)
            })
        }
        modeGroup.check(MODES.indexOf(settings.mode).coerceAtLeast(0) + 1)
        modeGroup.setOnCheckedChangeListener { _, id -> onMode(MODES[id - 1]) }
        col.addView(modeGroup)

        col.addView(Ui.heading(this, getString(R.string.access_label)))
        col.addView(CheckBox(this).apply {
            text = getString(R.string.auth_switch)
            isChecked = settings.auth
            setOnCheckedChangeListener { _, on ->
                settings.auth = on
                restartIfRunning()
            }
        })
        col.addView(Ui.hint(this, getString(R.string.auth_help)))
        col.addView(Button(this).apply {
            text = getString(R.string.new_access)
            setOnClickListener {
                settings.newAccess()
                restartIfRunning()
                Toast.makeText(this@SettingsActivity, getString(R.string.new_access_done), Toast.LENGTH_SHORT).show()
            }
        })
        col.addView(Ui.hint(this, getString(R.string.new_access_help)))

        col.addView(Ui.heading(this, getString(R.string.startup_label)))
        col.addView(CheckBox(this).apply {
            text = getString(R.string.start_on_boot)
            isChecked = settings.startOnBoot
            setOnCheckedChangeListener { _, on -> settings.startOnBoot = on }
        })
        batteryButton = Button(this).apply {
            text = getString(R.string.battery)
            setOnClickListener { Ui.askBatteryExemption(this@SettingsActivity) }
        }
        col.addView(batteryButton)

        return Ui.scroll(this, col)
    }

    /** Stores link and probe hosts, folds them if the link is valid, and restarts a running proxy. */
    private fun onSave() {
        settings.link = linkEdit.text.toString()
        settings.probeHosts = probeEdit.text.toString()
        val link = settings.link
        worker.execute {
            val err = try {
                Native.parse(link).optString("error")
            } catch (e: Throwable) {
                getString(R.string.native_broken, e.message ?: e.toString())
            }
            runOnUiThread {
                describeLink()
                if (err.isNotEmpty()) {
                    Toast.makeText(this, getString(R.string.link_error_toast, err), Toast.LENGTH_LONG).show()
                } else {
                    setLinkOpen(false)
                    restartIfRunning()
                    Toast.makeText(this, getString(R.string.saved), Toast.LENGTH_SHORT).show()
                }
            }
        }
    }

    private fun onMode(mode: String) {
        settings.mode = mode
        if (!PassageService.active) return
        worker.execute {
            try {
                Native.setMode(mode.toByteArray(Charsets.UTF_8))
            } catch (_: Throwable) {
            }
        }
    }

    /** Start while running = restart with the saved settings. */
    private fun restartIfRunning() {
        if (PassageService.active) PassageService.start(this)
    }

    /** Validates the saved link and says which zone it names; a bad one unfolds the fields. */
    private fun describeLink() {
        val link = settings.link
        if (link.isEmpty()) {
            linkInfo.text = getString(R.string.link_empty)
            setLinkOpen(true)
            return
        }
        worker.execute {
            var ok = false
            val text = try {
                val cfg = Native.parse(link)
                val err = cfg.optString("error")
                if (err.isNotEmpty()) getString(R.string.link_invalid, err)
                else {
                    ok = true
                    val n = cfg.optJSONArray("resolvers")?.length() ?: 0
                    val extra = if (n > 0) resources.getQuantityString(R.plurals.link_extra_resolvers, n, n) else ""
                    getString(R.string.link_ok_zone, cfg.optString("zone")) + extra
                }
            } catch (e: Throwable) {
                getString(R.string.native_broken, e.message ?: e.toString())
            }
            runOnUiThread {
                linkInfo.text = text
                if (!ok) setLinkOpen(true)
            }
        }
    }

    private fun setLinkOpen(open: Boolean) {
        linkBox.visibility = if (open) View.VISIBLE else View.GONE
        changeButton.visibility = if (open) View.GONE else View.VISIBLE
    }

    companion object {
        private val MODES = listOf("auto", "always", "off")
        private val MODE_LABELS = listOf(R.string.mode_auto, R.string.mode_always, R.string.mode_off)
    }
}
