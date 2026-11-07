package info.gluek.passage

import android.annotation.SuppressLint
import android.app.AlertDialog
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Paint
import android.graphics.Typeface
import android.os.Build
import android.os.Bundle
import android.text.Editable
import android.text.InputType
import android.text.TextUtils
import android.text.TextWatcher
import android.text.util.Linkify
import android.view.Gravity
import android.view.MenuItem
import android.view.View
import android.widget.Button
import android.widget.CheckBox
import android.widget.EditText
import android.widget.ImageButton
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import android.widget.Toast
import java.security.MessageDigest
import java.util.concurrent.Executors

/**
 * The link (pasted or opened once, kept in app-private storage),
 * routing mode, proxy access and background running. A valid link stays
 * folded so its key is not on screen.
 */
class SettingsActivity : ThemedActivity() {
    private val worker = Executors.newSingleThreadExecutor()
    private lateinit var settings: Settings

    private lateinit var linkHeading: TextView
    private lateinit var linkInfo: TextView
    private lateinit var changeButton: Button
    private lateinit var linkBox: LinearLayout
    private lateinit var linkEdit: EditText
    private lateinit var myEdit: EditText

    private lateinit var savedRoutesSection: LinearLayout
    private lateinit var savedRoutesHeading: TextView
    private lateinit var savedRoutesList: LinearLayout
    private lateinit var editRoutesButton: Button
    private lateinit var editRoutesBar: LinearLayout
    private lateinit var routesEditHint: TextView

    private lateinit var subscriptionsSection: LinearLayout
    private var shownSubscriptionConfig = ""
    private var subscriptionStatus = emptyList<org.json.JSONObject>()
    private var refreshingSubscriptions = false
    private var refreshResult = ""
    private var pollingSubscriptions = false
    private var subscriptionPollGeneration = 0
    private val main = android.os.Handler(android.os.Looper.getMainLooper())
    private val subscriptionPoll = object : Runnable {
        override fun run() {
            val generation = subscriptionPollGeneration
            worker.execute {
                val rows = try { Subscriptions.rows(org.json.JSONObject().put("subscriptions", Native.status().optJSONArray("subscriptions"))) }
                    catch (_: Throwable) { emptyList() }
                runOnUiThread {
                    if (isFinishing || isDestroyed || generation != subscriptionPollGeneration) return@runOnUiThread
                    subscriptionStatus = rows
                    if (editingRoutes) renderSubscriptions() else renderSavedRoutes()
                    if (shownSubscriptionConfig != settings.routesConfig) {
                        shownSubscriptionConfig = settings.routesConfig
                        describeLink()
                        showRoutes()
                    }
                    if (pollingSubscriptions) main.postDelayed(this, 2000)
                }
            }
        }
    }

    private var editingRoutes = false
    private var routeDraft: MutableList<Settings.SavedRoute>? = null

    private data class Draft(
        val link: String,
        val myRoute: String,
        val routeDraft: List<Settings.SavedRoute>? = null,
        val editingRoutes: Boolean = false,
    )
    private lateinit var batteryButton: Button
    private lateinit var backgroundState: TextView
    private lateinit var routeBox: LinearLayout
    private lateinit var aboutView: TextView
    private lateinit var scrollView: ScrollView
    private lateinit var updates: UpdateSection

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        settings = Settings(this)
        actionBar?.setDisplayHomeAsUpEnabled(true)
        setContentView(buildUi())
        val draft = lastNonConfigurationInstance as? Draft
        linkEdit.setText(draft?.link ?: "")
        myEdit.setText(draft?.myRoute ?: settings.myRoute)
        editingRoutes = draft?.editingRoutes ?: savedInstanceState?.getBoolean("editing_routes") ?: false
        routeDraft = draft?.routeDraft?.map { it.copy() }?.toMutableList()
        if (editingRoutes && routeDraft == null) {
            routeDraft = settings.savedRoutes.map { it.copy() }.toMutableList()
        }
        // Folded at once when a link is saved, so the key never flashes on
        // screen while describeLink checks it (a bad link unfolds).
        setLinkOpen(savedInstanceState?.getBoolean("link_open") ?: (settings.link.isEmpty() && settings.routesConfig.isEmpty()))
        renderSavedRoutes()
        showRoutes()
        describeLink()
        takeLink(intent)
    }

    // Keep unsaved fields in memory instead of the system instance-state bundle.
    @Suppress("OVERRIDE_DEPRECATION")
    override fun onRetainNonConfigurationInstance(): Any =
        Draft(linkEdit.text.toString(), myEdit.text.toString(), routeDraft?.map { it.copy() }, editingRoutes)

    override fun onSaveInstanceState(outState: Bundle) {
        outState.putBoolean("link_open", linkBox.visibility == View.VISIBLE)
        outState.putBoolean("editing_routes", editingRoutes)
        super.onSaveInstanceState(outState)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        takeLink(intent)
        if (intent.action == Updater.ACTION_SHOW) showUpdates()
    }

    override fun onResume() {
        super.onResume()
        val exempt = Ui.batteryExempt(this)
        batteryButton.visibility = if (exempt) View.GONE else View.VISIBLE
        backgroundState.visibility = if (exempt) View.VISIBLE else View.GONE
        if (updates.view.visibility == View.VISIBLE) updates.resume()
        main.removeCallbacks(subscriptionPoll)
        subscriptionPollGeneration++
        pollingSubscriptions = true
        main.post(subscriptionPoll)
    }

    override fun onPause() {
        subscriptionPollGeneration++
        pollingSubscriptions = false
        main.removeCallbacks(subscriptionPoll)
        updates.pause()
        super.onPause()
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

    /** A tapped passage:// or https://passage.gluek.info/link#… link goes into the field; Save stores it. */
    private fun takeLink(intent: Intent?) {
        val data = intent?.takeIf { it.action == Intent.ACTION_VIEW }?.data ?: return
        if (data.scheme == "https" && data.encodedFragment.isNullOrEmpty()) return // /link without fields
        val link = data.toString()
        setIntent(Intent(this, SettingsActivity::class.java))
        linkEdit.setText(link)
        setLinkOpen(true)
    }

    private fun buildUi(): View {
        val col = Ui.column(this)

        col.addView(Ui.heading(this, getString(R.string.route_label)))
        routeBox = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        col.addView(routeBox)
        col.addView(Ui.hint(this, getString(R.string.route_hint)).apply {
            setPadding(0, Ui.dp(this@SettingsActivity, 6), 0, 0)
        })

        col.addView(Ui.heading(this, getString(R.string.my_route_label)))
        myEdit = EditText(this).apply {
            id = R.id.my_route_edit
            isSaveEnabled = false
            hint = getString(R.string.my_route_hint)
            Ui.dimHint(this)
            setText(settings.myRoute)
            isSingleLine = true
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS or InputType.TYPE_TEXT_VARIATION_URI
            textSize = 14f
        }
        val myClear = ImageButton(this).apply {
            setImageResource(android.R.drawable.ic_menu_close_clear_cancel)
            background = null
            imageTintList = android.content.res.ColorStateList.valueOf(Ui.accent(this@SettingsActivity))
            contentDescription = getString(R.string.clear)
            setOnClickListener { myEdit.text.clear(); myEdit.requestFocus() }
        }
        myClear.visibility = if (myEdit.text.isEmpty()) View.INVISIBLE else View.VISIBLE
        myEdit.addTextChangedListener(object : TextWatcher {
            override fun beforeTextChanged(s: CharSequence?, start: Int, count: Int, after: Int) {}
            override fun onTextChanged(s: CharSequence?, start: Int, before: Int, count: Int) {}
            override fun afterTextChanged(s: Editable?) {
                myClear.visibility = if (s.isNullOrEmpty()) View.INVISIBLE else View.VISIBLE
            }
        })
        col.addView(LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            addView(myEdit, LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f))
            addView(myClear)
        })
        col.addView(Button(this).apply {
            text = getString(R.string.save)
            setOnClickListener { saveMyRoute(myEdit) }
        })
        col.addView(Ui.hint(this, getString(R.string.my_route_help)))

        savedRoutesSection = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
        }
        col.addView(savedRoutesSection)

        savedRoutesHeading = Ui.heading(this, getString(R.string.saved_routes_label)).apply {
            setPadding(0, paddingTop, 0, paddingBottom)
        }
        savedRoutesSection.addView(savedRoutesHeading)

        savedRoutesList = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
        }
        savedRoutesSection.addView(savedRoutesList)

        editRoutesButton = Button(this).apply {
            text = getString(R.string.edit_routes)
            setOnClickListener {
                routeDraft = settings.savedRoutes.map { it.copy() }.toMutableList()
                editingRoutes = true
                renderSavedRoutes()
            }
        }
        savedRoutesSection.addView(editRoutesButton)

        editRoutesBar = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            val saveBtn = Button(this@SettingsActivity).apply {
                text = getString(R.string.save)
                setOnClickListener {
                    val draft = routeDraft
                    if (draft != null) {
                        settings.saveRouteEdits(draft)
                        Toast.makeText(this@SettingsActivity, getString(R.string.saved), Toast.LENGTH_SHORT).show()
                        restartIfRunning()
                    }
                    editingRoutes = false
                    routeDraft = null
                    renderSavedRoutes()
                    showRoutes()
                    describeLink()
                }
            }
            val cancelBtn = Button(this@SettingsActivity).apply {
                text = getString(android.R.string.cancel)
                setOnClickListener {
                    editingRoutes = false
                    routeDraft = null
                    renderSavedRoutes()
                }
            }
            addView(saveBtn, LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f))
            addView(cancelBtn, LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f))
        }
        savedRoutesSection.addView(editRoutesBar)

        routesEditHint = Ui.hint(this, getString(R.string.routes_edit_hint)).apply {
            setPadding(0, Ui.dp(this@SettingsActivity, 6), 0, 0)
        }
        savedRoutesSection.addView(routesEditHint)

        linkHeading = Ui.heading(this, getString(R.string.link_label))
        col.addView(linkHeading)
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
            id = R.id.link_edit
            isSaveEnabled = false
            hint = getString(R.string.link_hint)
            Ui.dimHint(this)
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_MULTI_LINE or
                InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS or InputType.TYPE_TEXT_VARIATION_URI
            minLines = 2
            maxLines = 4
            textSize = 13f
        }
        linkBox.addView(linkEdit)
        linkBox.addView(Button(this).apply {
            text = getString(R.string.save)
            setOnClickListener { onSave() }
        })


        subscriptionsSection = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        col.addView(subscriptionsSection)

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

        col.addView(Ui.heading(this, getString(R.string.theme_label)))
        col.addView(Ui.segments(this, listOf(
            getString(R.string.theme_system), getString(R.string.theme_light), getString(R.string.theme_dark),
        ), Settings.THEMES.indexOf(settings.theme)) { index ->
            val theme = Settings.THEMES[index]
            if (settings.theme != theme) {
                settings.theme = theme
                recreate()
            }
        })

        updates = UpdateSection(this)
        updates.view.visibility = View.GONE
        col.addView(updates.view)

        col.addView(Ui.heading(this, getString(R.string.background_label)))
        col.addView(Ui.hint(this, getString(R.string.background_help)))
        backgroundState = Ui.hint(this, getString(R.string.background_allowed)).apply {
            setPadding(0, Ui.dp(this@SettingsActivity, 8), 0, 0)
        }
        col.addView(backgroundState)
        batteryButton = Button(this).apply {
            text = getString(R.string.battery)
            setOnClickListener { Ui.askBatteryExemption(this@SettingsActivity) }
        }
        col.addView(batteryButton)

        col.addView(Ui.heading(this, getString(R.string.about_label)))
        aboutView = Ui.hint(this, "")
        col.addView(aboutView)
        col.addView(Ui.hint(this, getString(R.string.about_cert)).apply {
            setPadding(0, Ui.dp(this@SettingsActivity, 12), 0, 0)
        })
        val certView = Ui.hint(this, certFingerprint()).apply {
            typeface = Typeface.MONOSPACE
            setTextColor(linkTextColors)
            setOnClickListener {
                Ui.copy(this@SettingsActivity, "Passage verification info",
                    "$packageName\n${certFingerprint()}", getString(R.string.about_cert_copied))
            }
        }
        col.addView(certView)
        col.addView(Ui.hint(this, getString(R.string.about_cert_help)))
        col.addView(Ui.hint(this, getString(R.string.about_site, SITE_URL)).apply {
            setPadding(0, Ui.dp(this@SettingsActivity, 12), 0, 0)
            Linkify.addLinks(this, Linkify.WEB_URLS)
        })
        col.addView(Ui.hint(this, getString(R.string.about_source, SOURCE_URL)).apply {
            Linkify.addLinks(this, Linkify.WEB_URLS)
        })
        col.addView(Ui.hint(this, getString(R.string.about_licenses)).apply {
            setPadding(0, Ui.dp(this@SettingsActivity, 12), 0, 0)
            setTextColor(linkTextColors)
            paintFlags = paintFlags or Paint.UNDERLINE_TEXT_FLAG
            setOnClickListener { showLicenses() }
        })
        worker.execute {
            val text = about()
            val enabled = Updater.enabled()
            runOnUiThread {
                if (isFinishing || isDestroyed) return@runOnUiThread
                aboutView.text = text
                if (enabled) {
                    updates.view.visibility = View.VISIBLE
                    updates.resume()
                    if (intent?.action == Updater.ACTION_SHOW) showUpdates()
                }
            }
        }

        scrollView = Ui.scroll(this, col).apply { id = R.id.settings_scroll }
        return scrollView
    }

    private fun showUpdates() {
        if (intent?.action == Updater.ACTION_SHOW) intent.action = null
        scrollView.post { scrollView.smoothScrollTo(0, updates.view.top) }
    }

    /** Imports the link after confirmation, folds it if valid, and restarts a running proxy. */
    private fun onSave() {
        val incoming = linkEdit.text.toString().trim()
        val previous = settings.routesConfig
        val previousLink = settings.link
        worker.execute {
            try {
                val existing = if (previous.isNotEmpty()) org.json.JSONObject(previous)
                    else if (previousLink.isNotEmpty()) Native.parse(previousLink) else org.json.JSONObject()
                fun prepare(replace: Boolean, subscribe: Boolean = false): org.json.JSONObject {
                    val request = org.json.JSONObject().put("existing", existing).put("link", incoming).put("replace", replace).put("subscribe", subscribe)
                    return Native.json(Native.importLink(request.toString().toByteArray(Charsets.UTF_8)))
                }
                val preview = prepare(false)
                if (preview.has("error")) throw IllegalArgumentException(preview.optString("error"))
                runOnUiThread {
                    if (isFinishing || isDestroyed) return@runOnUiThread
                    val subscribing = preview.optString("mode") == "subscribe"
                    val trust = if (preview.optString("signature") == "trusted_author") getString(R.string.import_trusted)
                        else getString(R.string.import_untrusted)
                    val collision = if (preview.optBoolean("collision")) "\n" + getString(R.string.import_collision) else ""
                    val operation = if (incoming.isEmpty()) getString(R.string.import_clear)
                        else if (preview.optString("mode") == "replace") getString(R.string.import_replace)
                        else getString(R.string.import_merge)
                    AlertDialog.Builder(this).setTitle(if (subscribing) R.string.subscribe else R.string.import_title)
                        .setMessage(if (subscribing) subscriptionPreview(preview) else trust + "\n\n" + operation + collision + "\n" + (preview.optJSONArray("route_ids")?.join(", ") ?: "") + "\n\n" + getString(R.string.import_personal))
                        .setNegativeButton(android.R.string.cancel, null)
                        .setPositiveButton(if (subscribing) R.string.subscribe else R.string.save) { _, _ ->
                            worker.execute {
                                try {
                                    if (settings.routesConfig != previous || settings.link != previousLink) throw IllegalStateException("Settings changed; retry import")
                                    val result = prepare(if (subscribing) preview.optBoolean("collision") else true, subscribing)
                                    if (result.has("error")) throw IllegalArgumentException(result.optString("error"))
                                    synchronized(Settings.configLock) {
                                        if (settings.routesConfig != previous || settings.link != previousLink) throw IllegalStateException("Settings changed; retry import")
                                        settings.saveRoutes(if (subscribing) previousLink else incoming, result.getJSONObject("config").toString())
                                    }
                                    runOnUiThread {
                                        restartIfRunning()
                                        if (isFinishing || isDestroyed) return@runOnUiThread
                                        editingRoutes = false
                                        routeDraft = null
                                        renderSavedRoutes()
                                        linkEdit.setText("")
                                        describeLink(); setLinkOpen(false); showRoutes()
                                    }
                                } catch (e: Throwable) { runOnUiThread { Toast.makeText(this, getString(R.string.link_error_toast, e.message), Toast.LENGTH_LONG).show() } }
                            }
                        }.show()
                }
            } catch (e: Throwable) { runOnUiThread { Toast.makeText(this, getString(R.string.link_error_toast, e.message), Toast.LENGTH_LONG).show() } }
        }
    }

    private fun subscriptionPreview(preview: org.json.JSONObject): String {
        val sub = preview.getJSONObject("subscription")
        return listOf(
            sub.optString("name"),
            resources.getQuantityString(R.plurals.sub_addresses, sub.optInt("sources"), sub.optInt("sources")),
            getString(if (sub.optBoolean("has_seed")) R.string.sub_seed else R.string.sub_no_seed),
            *Subscriptions.inviteSignerResources(preview.optString("signature")).map { getString(it) }.toTypedArray(),
        ).joinToString("\n") + if (preview.optBoolean("collision")) "\n\n" + getString(R.string.sub_collision) else ""
    }

    private fun renderSubscriptions() {
        if (!::subscriptionsSection.isInitialized) return
        val rows = settings.subscriptions
        subscriptionsSection.removeAllViews()
        subscriptionsSection.addView(Ui.heading(this, getString(R.string.subscriptions)))
        if (rows.isEmpty()) {
            subscriptionsSection.addView(Ui.hint(this, getString(R.string.subscriptions_empty)))
            return
        }
        for (sub in rows) {
            val id = sub.getString("id")
            val status = if (Subscriptions.isClosed(sub)) sub else subscriptionStatus.firstOrNull { it.optString("id") == id.take(8) } ?: sub
            subscriptionsSection.addView(TextView(this).apply {
                text = sub.optString("name")
                textSize = 16f
            })
            subscriptionsSection.addView(Ui.hint(this, Subscriptions.detail(this, status)))
            if (!Subscriptions.isClosed(status) && (refreshingSubscriptions || refreshResult.isNotEmpty())) {
                subscriptionsSection.addView(Ui.hint(this, if (refreshingSubscriptions) getString(R.string.sub_refreshing) else refreshResult))
            }
            val buttons = LinearLayout(this).apply { orientation = LinearLayout.HORIZONTAL }
            val gap = LinearLayout.LayoutParams(LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT)
                .apply { marginEnd = Ui.dp(this@SettingsActivity, 12) }
            if (!Subscriptions.isClosed(status)) buttons.addView(Button(this).apply {
                text = getString(R.string.sub_refresh)
                isEnabled = !refreshingSubscriptions && !editingRoutes
                setOnClickListener { refreshSubscriptions() }
            }, gap)
            buttons.addView(Button(this).apply {
                text = getString(R.string.sub_remove)
                isEnabled = !refreshingSubscriptions && !editingRoutes
                setOnClickListener {
                    AlertDialog.Builder(this@SettingsActivity)
                        .setMessage(getString(R.string.sub_remove_confirm, sub.optString("name")))
                        .setNegativeButton(android.R.string.cancel, null)
                        .setPositiveButton(R.string.sub_remove) { _, _ -> removeSubscription(id) }.show()
                }
            })
            subscriptionsSection.addView(buttons)
        }
    }

    private fun refreshSubscriptions() {
        val previous = settings.routesConfig
        refreshingSubscriptions = true
        refreshResult = ""
        renderSubscriptions()
        worker.execute {
            var restart = false
            val message = try {
                if (PassageService.active && PassageService.runningRoutesConfig != previous) throw IllegalStateException()
                val result = Native.json(Native.refreshSubscriptions(Subscriptions.request(previous).toString().toByteArray(Charsets.UTF_8)))
                if (result.has("error")) throw IllegalStateException()
                val config = result.getJSONObject("config")
                val changed = result.optBoolean("changed")
                synchronized(Settings.configLock) {
                    if (!settings.saveSubscriptionRefresh(previous, config)) throw IllegalStateException()
                    if (!changed) PassageService.savedRefreshMetadata(previous, config.toString())
                }
                restart = changed
                val errors = Subscriptions.rows(config).mapNotNull { Subscriptions.errorResource(it.optString("last_error")) }
                if (errors.isNotEmpty()) getString(R.string.sub_failed, getString(errors.first()))
                else getString(if (restart) R.string.sub_updated else R.string.sub_current)
            } catch (_: Throwable) { getString(R.string.sub_save_failed) }
            // Restart even if the activity was closed while fetching.
            if (restart) restartIfRunning()
            runOnUiThread {
                if (isFinishing || isDestroyed) return@runOnUiThread
                refreshingSubscriptions = false
                refreshResult = message
                subscriptionStatus = emptyList()
                renderSavedRoutes(); showRoutes(); describeLink()
            }
        }
    }

    private fun removeSubscription(id: String) {
        val previous = settings.routesConfig
        worker.execute {
            val ok = try {
                val request = Subscriptions.request(previous).put("id", id)
                val result = Native.json(Native.removeSubscription(request.toString().toByteArray(Charsets.UTF_8)))
                !result.has("error") && settings.saveSubscriptionRemoval(previous, result.getJSONObject("config"))
            } catch (_: Throwable) { false }
            if (ok) restartIfRunning()
            runOnUiThread {
                if (isFinishing || isDestroyed) return@runOnUiThread
                if (!ok) Toast.makeText(this, R.string.sub_save_failed, Toast.LENGTH_LONG).show()
                subscriptionStatus = emptyList()
                refreshResult = ""
                renderSavedRoutes(); showRoutes(); describeLink()
            }
        }
    }

    /** Saves My route in its short form (empty removes it) and restarts a running proxy. */
    private fun saveMyRoute(edit: EditText) {
        val text = edit.text.toString().trim()
        val value = if (text.isEmpty()) "" else Settings.normalizeMyRoute(text) ?: run {
            Toast.makeText(this, getString(R.string.my_route_invalid), Toast.LENGTH_LONG).show()
            return
        }
        if (edit.text.toString() != value) edit.setText(value)
        if (value == settings.myRoute) return
        settings.myRoute = value
        Toast.makeText(this, getString(if (value.isEmpty()) R.string.my_route_removed else R.string.my_route_saved), Toast.LENGTH_SHORT).show()
        showRoutes()
        restartIfRunning()
    }

    /** Start while running = restart with the saved settings. */
    private fun restartIfRunning() {
        if (PassageService.active) PassageService.start(this, "settings changed")
    }

    /** Validates the saved link and says which zone it names; a bad one unfolds the fields. */
    private fun describeLink() {
        val link = settings.link
        if (link.isEmpty() && settings.savedRoutes.isEmpty() && settings.subscriptions.isEmpty()) {
            linkInfo.text = getString(R.string.link_empty)
            linkInfo.visibility = View.VISIBLE
            setLinkOpen(true)
            renderSavedRoutes()
            return
        }
        worker.execute {
            var ok = false
            val text = try {
                val cfg = if (settings.routesConfig.isNotEmpty()) org.json.JSONObject(settings.routesConfig) else Native.parse(link)
                val err = cfg.optString("error")
                if (err.isNotEmpty()) getString(R.string.link_invalid, err)
                else {
                    ok = true
                    val routes = cfg.optJSONArray("routes")
                    if (routes != null && routes.length() > 0) {
                        getString(R.string.import_route_count, routes.length(), settings.savedRoutes.joinToString(", ") { "${it.priority} ${it.id} (${it.type}${it.subscription.takeIf { n -> n.isNotEmpty() }?.let { n -> " · $n" }.orEmpty()})" })
                    } else if (Subscriptions.rows(cfg).isNotEmpty()) {
                        ""
                    } else {
                        val n = cfg.optJSONArray("resolvers")?.length() ?: 0
                        val extra = if (n > 0) resources.getQuantityString(R.plurals.link_extra_resolvers, n, n) else ""
                        val ipv6 = getString(if (cfg.optBoolean("tunnel_ipv6")) R.string.server_ipv6_on else R.string.server_ipv6_off)
                        getString(R.string.link_ok_zone, cfg.optString("zone")) + extra + "\n" + ipv6
                    }
                }
            } catch (e: Throwable) {
                getString(R.string.native_broken, e.message ?: e.toString())
            }
            runOnUiThread {
                linkInfo.text = text
                linkInfo.visibility = if (text.isEmpty()) View.GONE else View.VISIBLE
                if (!ok) setLinkOpen(true)
                renderSavedRoutes()
            }
        }
    }

    /** The licenses of the open-source code built into the app (res/raw/licenses.txt). */
    private fun showLicenses() {
        val text = resources.openRawResource(R.raw.licenses).bufferedReader().use { it.readText() }
        val view = TextView(this).apply {
            this.text = text
            typeface = Typeface.MONOSPACE
            textSize = 11f
            setTextIsSelectable(true)
            val pad = Ui.dp(this@SettingsActivity, 16)
            setPadding(pad, pad, pad, pad)
        }
        AlertDialog.Builder(this)
            .setTitle(R.string.about_licenses)
            .setView(ScrollView(this).apply { addView(view) })
            .setPositiveButton(android.R.string.ok, null)
            .show()
    }

    /** Version and soar version, so a tester can say what they run. */
    private fun about(): String {
        val soar = try {
            Native.json(Native.versions()).optString("soar", "?")
        } catch (_: Throwable) {
            "?"
        }
        val info = packageManager.getPackageInfo(packageName, 0)
        val code = if (Build.VERSION.SDK_INT >= 28) info.longVersionCode else {
            @Suppress("DEPRECATION")
            info.versionCode.toLong()
        }
        return getString(R.string.about_version, info.versionName, code, soar)
    }

    /** SHA-256 of the signing certificate; tapping it copies the AppVerifier format. */
    private fun certFingerprint(): String = try {
        val sig = if (Build.VERSION.SDK_INT >= 28) {
            packageManager.getPackageInfo(packageName, PackageManager.GET_SIGNING_CERTIFICATES)
                .signingInfo?.apkContentsSigners?.firstOrNull()
        } else {
            // Our own package: the single signer is the one Android verified at install.
            @Suppress("DEPRECATION")
            @SuppressLint("PackageManagerGetSignatures")
            val sigs = packageManager.getPackageInfo(packageName, PackageManager.GET_SIGNATURES).signatures
            sigs?.firstOrNull()
        }
        val d = MessageDigest.getInstance("SHA-256").digest(sig!!.toByteArray())
        d.joinToString(":") { "%02X".format(it) }
    } catch (_: Exception) {
        "?"
    }

    /** "Always tunnel" only makes sense with a link; without one the router runs auto. */
    /** The route choice for the saved link; a removed route type shows as Auto. */
    private fun showRoutes() {
        routeBox.removeAllViews()
        routeBox.addView(Ui.routeSelector(this, settings, worker::execute) { showRoutes() })
    }

    private fun renderSavedRoutes() {
        renderSubscriptions()
        val routes = if (editingRoutes) routeDraft ?: emptyList() else settings.savedRoutes
        savedRoutesSection.visibility = View.VISIBLE
        linkHeading.setPadding(0, Ui.dp(this, 20), 0, linkHeading.paddingBottom)

        editRoutesButton.visibility = if (editingRoutes || routes.isEmpty()) View.GONE else View.VISIBLE
        editRoutesBar.visibility = if (editingRoutes) View.VISIBLE else View.GONE
        routesEditHint.visibility = if (editingRoutes) View.VISIBLE else View.GONE

        savedRoutesList.removeAllViews()
        if (routes.isEmpty()) {
            savedRoutesList.addView(Ui.hint(this, getString(R.string.saved_routes_empty)))
        }
        for (r in routes) {
            savedRoutesList.addView(buildRouteRow(r, editingRoutes))
        }
    }

    private fun buildRouteRow(r: Settings.SavedRoute, editing: Boolean): View {
        val row = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(0, Ui.dp(this@SettingsActivity, 2), 0, Ui.dp(this@SettingsActivity, 2))
            alpha = if (r.on) 1f else 0.5f
        }
        if (!editing) {
            val check = CheckBox(this).apply {
                isChecked = r.on
                isClickable = false
                isFocusable = false
                text = "${r.priority} ${r.id} (${r.type}${r.subscription.takeIf { it.isNotEmpty() }?.let { " · $it" }.orEmpty()})"
                textSize = 14f
            }
            row.addView(check)
            return row
        }

        val check = CheckBox(this).apply {
            isChecked = r.on
            setOnCheckedChangeListener { _, isChecked ->
                r.on = isChecked
                row.alpha = if (r.on) 1f else 0.5f
            }
        }
        row.addView(check)

        val label = TextView(this).apply {
            text = "${r.id} (${r.type}${r.subscription.takeIf { it.isNotEmpty() }?.let { " · $it" }.orEmpty()})"
            textSize = 14f
            maxLines = 1
            ellipsize = TextUtils.TruncateAt.END
        }
        row.addView(label, LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f))

        lateinit var numView: TextView

        val minus = Button(this).apply {
            text = "−"
            textSize = 16f
            minWidth = 0
            minimumWidth = 0
            minHeight = Ui.dp(this@SettingsActivity, 36)
            minimumHeight = Ui.dp(this@SettingsActivity, 36)
            val padH = Ui.dp(this@SettingsActivity, 10)
            setPadding(padH, 0, padH, 0)
            setOnClickListener {
                r.priority = (r.priority - 5).coerceIn(0, 65535)
                numView.text = r.priority.toString()
            }
        }
        row.addView(minus)

        numView = TextView(this).apply {
            text = r.priority.toString()
            textSize = 14f
            gravity = Gravity.CENTER
            minWidth = Ui.dp(this@SettingsActivity, 44)
            val padH = Ui.dp(this@SettingsActivity, 4)
            setPadding(padH, 0, padH, 0)
            setOnClickListener {
                showPriorityDialog(r) {
                    numView.text = r.priority.toString()
                }
            }
        }
        row.addView(numView)

        val plus = Button(this).apply {
            text = "+"
            textSize = 16f
            minWidth = 0
            minimumWidth = 0
            minHeight = Ui.dp(this@SettingsActivity, 36)
            minimumHeight = Ui.dp(this@SettingsActivity, 36)
            val padH = Ui.dp(this@SettingsActivity, 10)
            setPadding(padH, 0, padH, 0)
            setOnClickListener {
                r.priority = (r.priority + 5).coerceIn(0, 65535)
                numView.text = r.priority.toString()
            }
        }
        row.addView(plus)

        return row
    }

    private fun showPriorityDialog(r: Settings.SavedRoute, onUpdated: () -> Unit) {
        val input = EditText(this).apply {
            inputType = InputType.TYPE_CLASS_NUMBER
            setText(r.priority.toString())
            selectAll()
            gravity = Gravity.CENTER
        }
        val pad = Ui.dp(this, 20)
        val frame = LinearLayout(this).apply {
            setPadding(pad, pad / 2, pad, 0)
            addView(input, LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT))
        }
        AlertDialog.Builder(this)
            .setTitle("${r.id} (${r.type}${r.subscription.takeIf { it.isNotEmpty() }?.let { " · $it" }.orEmpty()})")
            .setView(frame)
            .setPositiveButton(R.string.save) { _, _ ->
                val v = input.text.toString().trim().toIntOrNull()
                if (v != null) {
                    r.priority = v.coerceIn(0, 65535)
                    onUpdated()
                }
            }
            .setNegativeButton(android.R.string.cancel, null)
            .show()
    }

    private fun setLinkOpen(open: Boolean) {
        linkBox.visibility = if (open) View.VISIBLE else View.GONE
        changeButton.visibility = if (open) View.GONE else View.VISIBLE
    }

    companion object {
        private const val SITE_URL = "https://passage.gluek.info"
        private const val SOURCE_URL = "https://github.com/mrgluek/passage"
    }
}
