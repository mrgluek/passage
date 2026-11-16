package info.gluek.passage

import android.animation.ObjectAnimator
import android.animation.PropertyValuesHolder
import android.animation.ValueAnimator
import android.graphics.drawable.GradientDrawable
import android.os.SystemClock
import android.Manifest
import android.app.AlertDialog
import android.content.ActivityNotFoundException
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.Gravity
import android.view.Menu
import android.view.MenuItem
import android.view.View
import android.widget.Switch
import android.widget.Button
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import java.util.concurrent.Executors

class MainActivity : ThemedActivity() {
    private val worker = Executors.newSingleThreadExecutor()
    private val main = Handler(Looper.getMainLooper())
    private lateinit var settings: Settings

    private lateinit var stateView: TextView
    private lateinit var reasonView: TextView
    private lateinit var statusDot: View
    private lateinit var statusRing: View
    private lateinit var primary: Button
    private lateinit var secondary: Button
    private lateinit var manualBox: LinearLayout
    private lateinit var setup: LinearLayout
    private lateinit var batteryCard: LinearLayout
    private lateinit var backgroundCard: LinearLayout
    private lateinit var backgroundTitle: TextView
    private lateinit var backgroundText: TextView
    private var vendor = BackgroundHint.vendor(Build.MANUFACTURER, Build.BRAND)
    private var cleanerKill = 0L
    private var forcedKill = false
    private var backgroundShown = BackgroundHint.Card.NONE
    private lateinit var routesNotice: TextView
    private lateinit var routesNoticeBox: LinearLayout
    private lateinit var updates: UpdateSection
    private var resumed = false
    private var dotAnimator: ObjectAnimator? = null
    private var previousTraffic: HomeSetup.TrafficCounters? = null
    private var trafficUntil = 0L
    private var setupKey = ""
    private var expanded = false
    private var appTab = HomeSetup.AppTab.DELTA_CHAT
    private var passwordVisible = false
    /** A network test for a bug report; kept across screen rotation. */
    private class NetworkTest {
        @Volatile var result: String? = null
    }
    private var networkTest: NetworkTest? = null
    private var testDialog: AlertDialog? = null

    private val poll = object : Runnable {
        override fun run() {
            refresh()
            main.postDelayed(this, 2_000)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        expanded = savedInstanceState?.getBoolean("setup_expanded") ?: false
        networkTest = lastNonConfigurationInstance as? NetworkTest
        Diagnostics.event("main screen created: recreation=${savedInstanceState != null}")
        settings = Settings(this)
        settings.migrateAppSeen()
        applyDebugOverrides(intent)
        if (savedInstanceState == null) showUpdate(intent)
        setContentView(buildUi())
        title = "${getString(R.string.app_name)} ${appVersion()}"
        // The proxy was on and is not now: an app update or the system
        // stopped it. Opening the app turns it back on.
        if (settings.running && !PassageService.active) {
            PassageService.start(this, "restore on app open")
        }
    }

    override fun onSaveInstanceState(outState: Bundle) {
        outState.putBoolean("setup_expanded", expanded)
        super.onSaveInstanceState(outState)
    }

    /**
     * Only in a debuggable build (never in a release APK): force the maker and a
     * system stop to see the background hint on any phone, for example
     * `am start -n info.gluek.passage/.MainActivity --es debug_vendor xiaomi --ez debug_kill true`.
     */
    private fun applyDebugOverrides(intent: Intent?) {
        if (applicationInfo.flags and android.content.pm.ApplicationInfo.FLAG_DEBUGGABLE == 0) return
        intent?.getStringExtra("debug_vendor")?.let { vendor = BackgroundHint.vendor(it, it) }
        if (intent?.hasExtra("debug_kill") == true) forcedKill = intent.getBooleanExtra("debug_kill", false)
        if (intent?.getBooleanExtra("debug_reset_hint", false) == true) {
            settings.backgroundHintDismissed = false
            settings.backgroundKillSeen = 0
        }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        applyDebugOverrides(intent)
        showUpdate(intent)
        if (resumed) {
            cleanerKill = if (forcedKill) System.currentTimeMillis() else BackgroundHint.lastCleanerKill(this)
            updates.resume()
        }
        refresh()
    }

    /** The update notification brings back a card the user put aside. */
    private fun showUpdate(intent: Intent?) {
        if (intent?.action == Updater.ACTION_SHOW) settings.dismissedUpdate = ""
        UpdateDebug.apply(this, intent)
    }

    override fun onResume() {
        super.onResume()
        resumed = true
        cleanerKill = if (forcedKill) System.currentTimeMillis() else BackgroundHint.lastCleanerKill(this)
        updates.resume()
        main.post(poll)
    }

    override fun onPause() {
        resumed = false
        updates.pause()
        cancelDotAnimation()
        main.removeCallbacks(poll)
        super.onPause()
    }

    override fun onDestroy() {
        cancelDotAnimation()
        main.removeCallbacksAndMessages(null)
        testDialog?.setOnDismissListener(null)
        testDialog?.dismiss()
        testDialog = null
        Diagnostics.event("main screen destroyed: configuration=$isChangingConfigurations finishing=$isFinishing")
        worker.shutdown()
        super.onDestroy()
    }

    @Suppress("OVERRIDE_DEPRECATION")
    override fun onRetainNonConfigurationInstance(): Any? = networkTest

    override fun onCreateOptionsMenu(menu: Menu): Boolean {
        menuInflater.inflate(R.menu.main, menu)
        return true
    }

    override fun onOptionsItemSelected(item: MenuItem): Boolean {
        when (item.itemId) {
            R.id.action_settings -> startActivity(Intent(this, SettingsActivity::class.java))
            else -> return super.onOptionsItemSelected(item)
        }
        return true
    }

    private fun details() = startActivity(Intent(this, DetailsActivity::class.java))

    private fun buildUi(): View {
        val col = Ui.column(this)
        routesNotice = Ui.hint(this, "")
        routesNoticeBox = Ui.bubble(this, routesNotice) { settings.routesNotice = ""; refresh() }.apply {
            visibility = View.GONE
            layoutParams = LinearLayout.LayoutParams(-1, -2).apply { bottomMargin = Ui.dp(this@MainActivity, 12) }
        }
        col.addView(routesNoticeBox)
        val status = Ui.card(this).apply { setPadding(paddingLeft, paddingTop, paddingRight, Ui.dp(this@MainActivity, 12)) }
        val row = LinearLayout(this).apply { gravity = Gravity.CENTER_VERTICAL }
        statusDot = Ui.dot(this, getColor(R.color.proxy_off_dot)).apply {
            importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_YES
        }
        val dotBox = FrameLayout(this).apply {
            layoutParams = LinearLayout.LayoutParams(Ui.dp(this@MainActivity, 32), Ui.dp(this@MainActivity, 32)).apply {
                marginEnd = Ui.dp(this@MainActivity, 2)
            }
        }
        statusRing = Ui.dot(this, getColor(R.color.proxy_traffic_dot)).apply { alpha = 0f }
        for (dot in listOf(statusRing, statusDot)) {
            dotBox.addView(dot, FrameLayout.LayoutParams(Ui.dp(this, 14), Ui.dp(this, 14), Gravity.CENTER))
        }
        row.addView(dotBox)
        stateView = Ui.cardHeading(this, "")
        row.addView(stateView, LinearLayout.LayoutParams(0, -2, 1f))
        status.addView(row)
        reasonView = Ui.hint(this, "").apply {
            setPadding(0, Ui.dp(this@MainActivity, 10), 0, Ui.dp(this@MainActivity, 12))
        }
        status.addView(reasonView)
        status.setOnClickListener { details() }
        manualBox = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            visibility = View.GONE
            addView(Ui.hint(this@MainActivity, getString(R.string.home_manual_note)).apply {
                setPadding(0, 0, 0, Ui.dp(this@MainActivity, 10))
            })
            addView(Ui.chip(this@MainActivity, getString(R.string.home_manual_back)) { backToAuto() })
        }
        status.addView(manualBox)
        primary = Ui.primaryButton(this, "") { if (!settings.hasLink) addLink() else onStartClicked() }
        secondary = Ui.textButton(this, "") { onStartStop() }.apply {
            minHeight = Ui.dp(this@MainActivity, 40)
            minimumHeight = Ui.dp(this@MainActivity, 40)
            setPadding(Ui.dp(this@MainActivity, 16), 0, Ui.dp(this@MainActivity, 16), 0)
        }
        status.addView(primary)
        status.addView(secondary)
        col.addView(status)
        updates = UpdateSection(this, home = true)
        col.addView(updates.view)
        setup = Ui.card(this)
        col.addView(setup)
        batteryCard = Ui.card(this).apply { visibility = View.GONE }
        batteryCard.addView(Ui.heading(this, getString(R.string.home_battery_title)).apply { setPadding(0, 0, 0, 0) })
        batteryCard.addView(Ui.hint(this, getString(R.string.home_battery_reason)).apply {
            setPadding(0, Ui.dp(this@MainActivity, 8), 0, Ui.dp(this@MainActivity, 12))
        })
        batteryCard.addView(Ui.outlinedButton(this, getString(R.string.home_battery_allow)) { Ui.askBatteryExemption(this) })
        col.addView(batteryCard)
        backgroundCard = Ui.card(this).apply { visibility = View.GONE }
        backgroundTitle = Ui.heading(this, "").apply { setPadding(0, 0, 0, 0) }
        backgroundCard.addView(backgroundTitle)
        backgroundText = Ui.hint(this, "").apply { setPadding(0, Ui.dp(this@MainActivity, 8), 0, Ui.dp(this@MainActivity, 12)) }
        backgroundCard.addView(backgroundText)
        backgroundCard.addView(Ui.outlinedButton(this, getString(R.string.bg_open_settings)) { BackgroundHint.openSettings(this, vendor) })
        backgroundCard.addView(Ui.textButton(this, getString(R.string.bg_dismiss)) {
            settings.backgroundHintDismissed = true
            settings.backgroundKillSeen = maxOf(settings.backgroundKillSeen, cleanerKill)
            refresh()
        }.apply { gravity = Gravity.CENTER })
        col.addView(backgroundCard)
        col.addView(Ui.card(this).apply {
            addView(Ui.heading(this@MainActivity, getString(R.string.home_help)).apply { setPadding(0, 0, 0, Ui.dp(this@MainActivity, 12)) })
            addView(Ui.hint(this@MainActivity, getString(R.string.home_report_what)).apply {
                setPadding(0, 0, 0, Ui.dp(this@MainActivity, 4))
            })
            val reportInfo = Ui.hint(this@MainActivity, getString(R.string.home_report_not)).apply { visibility = View.GONE }
            addView(Ui.textButton(this@MainActivity, getString(R.string.home_report_link)) {
                reportInfo.visibility = if (reportInfo.visibility == View.VISIBLE) View.GONE else View.VISIBLE
            }.apply {
                layoutParams = LinearLayout.LayoutParams(-2, -2).apply { gravity = Gravity.CENTER_HORIZONTAL }
            })
            addView(reportInfo)
            addView(Ui.outlinedButton(this@MainActivity, getString(R.string.home_test_report)) { sendReport() }.apply {
                layoutParams = LinearLayout.LayoutParams(-1, -2).apply { topMargin = Ui.dp(this@MainActivity, 12) }
            })
            addView(LinearLayout(this@MainActivity).apply {
                orientation = LinearLayout.HORIZONTAL
                layoutParams = LinearLayout.LayoutParams(-1, -2).apply { topMargin = Ui.dp(this@MainActivity, 18) }
                gravity = Gravity.CENTER_VERTICAL
                minimumHeight = Ui.dp(this@MainActivity, 64)
                setPadding(0, Ui.dp(this@MainActivity, 8), 0, Ui.dp(this@MainActivity, 8))
                val labels = LinearLayout(this@MainActivity).apply {
                    orientation = LinearLayout.VERTICAL
                    addView(Ui.body(this@MainActivity, getString(R.string.home_details)).apply {
                        setTextColor(Ui.accent(this@MainActivity))
                    })
                    addView(Ui.hint(this@MainActivity, getString(R.string.home_details_sub)))
                }
                addView(labels, LinearLayout.LayoutParams(0, -2, 1f))
                addView(Ui.body(this@MainActivity, "›").apply {
                    setTextColor(Ui.accent(this@MainActivity))
                    importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
                })
                isFocusable = true
                setOnClickListener { details() }
            })
        })
        return Ui.scroll(this, col, bottomSpacing = 24)
    }

    private fun renderSetup() {
        val step = HomeSetup.step(settings.hasLink, PassageService.active, settings.appSeen)
        val key = "$step ${settings.appSeen} $expanded $appTab $passwordVisible ${settings.whatsappEnabled} ${settings.auth} ${settings.port} ${settings.user} ${settings.pass}"
        if (setupKey == key) return
        setupKey = key
        whatsappStatusView = null
        setup.removeAllViews()
        setup.addView(Ui.heading(this, getString(HomeSetup.heading(settings.hasLink, PassageService.active, settings.appSeen))).apply { setPadding(0, 0, 0, 0) })
        val labels = listOf(R.string.home_step_link, R.string.home_step_on, R.string.home_step_app)
        val doneLabels = listOf(R.string.home_step_link_done, R.string.home_step_on_done, R.string.home_step_app_done)
        val rows = labels.mapIndexed { i, label ->
            val number = i + 1
            val done = when (number) { 1 -> settings.hasLink; 2 -> PassageService.active; else -> settings.appSeen }
            val click: (() -> Unit)? = if (number == 3 && done) ({
                expanded = !expanded; passwordVisible = false; renderSetup()
            }) else null
            Ui.setupRow(this, getString(if (done) doneLabels[i] else label), number, done, number == step, expanded, click)
        }
        setup.addView(Ui.setupRows(this, rows))
        if ((step == 3 || settings.appSeen) && HomeSetup.appTabsVisible(settings.appSeen, expanded)) {
            val tabs = HomeSetup.AppTab.entries
            setup.addView(Ui.appTabs(this, tabs.map { getString(it.label) }, appTab.ordinal) { index ->
                appTab = tabs[index]
                passwordVisible = false
                renderSetup()
            })
            val tabContent = LinearLayout(this).apply {
                orientation = LinearLayout.VERTICAL
                setPadding(0, Ui.dp(this@MainActivity, 12), 0, 0)
            }
            setup.addView(tabContent)
            val content = HomeSetup.connect(appTab, settings.socksLink(), settings.tgLink(), settings.veltaLink())
            for ((blockIndex, block) in listOfNotNull(content, content.secondary).withIndex()) {
                block.instruction?.takeIf { appTab != HomeSetup.AppTab.OTHER && appTab != HomeSetup.AppTab.WHATSAPP }?.let { instruction ->
                    tabContent.addView(Ui.hint(this, getString(instruction)).apply {
                        if (blockIndex > 0) setPadding(paddingLeft, paddingTop + Ui.dp(this@MainActivity, 16), paddingRight, paddingBottom)
                    })
                }
                block.action?.let { action ->
                    tabContent.addView(Ui.outlinedButton(this, getString(action)) {
                        block.clipboard?.let { Ui.copy(this, "Passage proxy", it, getString(R.string.proxy_link_copied), sensitive = HomeSetup.isCredentialLink(it)) }
                        block.open?.let { openProxyLink(it, getString(action)) }
                    }.apply {
                        layoutParams = LinearLayout.LayoutParams(-1, -2).apply { topMargin = Ui.dp(this@MainActivity, 12) }
                    })
                }
                block.download?.let { url ->
                    tabContent.addView(Ui.downloadLine(this, getString(R.string.home_download), url).apply {
                        setPadding(0, Ui.dp(this@MainActivity, 10), 0, 0)
                    })
                }
            }
            if (appTab == HomeSetup.AppTab.WHATSAPP) {
                tabContent.addView(WhatsAppUi.controls(this, settings, true) {
                    whatsappStarted = if (settings.whatsappEnabled && PassageService.active) SystemClock.elapsedRealtime() else 0L
                    whatsappForwards = null
                    renderSetup()
                })
                whatsappStatusView = Ui.hint(this, "").also { tabContent.addView(it) }
                renderWhatsAppStatus()
            }
            if (appTab == HomeSetup.AppTab.OTHER) {
                tabContent.addView(Ui.hint(this, getString(R.string.home_molly_intro)).apply {
                    setPadding(paddingLeft, paddingTop, paddingRight, paddingBottom + Ui.dp(this@MainActivity, 12))
                })
                tabContent.addView(Switch(this).apply {
                    text = getString(R.string.allow_no_password)
                    isChecked = !settings.auth
                    setOnCheckedChangeListener { _, on ->
                        settings.setAuthentication(this@MainActivity, !on)
                        renderSetup()
                    }
                })
                tabContent.addView(Ui.hint(this, getString(R.string.allow_no_password_help)))
                tabContent.addView(Ui.hint(this, getString(R.string.home_molly_how)).apply {
                    setPadding(paddingLeft, paddingTop + Ui.dp(this@MainActivity, 16), paddingRight, paddingBottom)
                })
                tabContent.addView(Ui.valueBlock(this, listOf(
                    getString(R.string.proxy_host) to "127.0.0.1",
                    getString(R.string.proxy_port) to settings.port.toString()
                ), null).apply {
                    layoutParams = LinearLayout.LayoutParams(-1, -2).apply { topMargin = Ui.dp(this@MainActivity, 12) }
                })
                tabContent.addView(Ui.downloadLine(this, getString(R.string.home_download), HomeSetup.MOLLY_DOWNLOAD).apply {
                    setPadding(0, Ui.dp(this@MainActivity, 10), 0, 0)
                })
                tabContent.addView(Ui.heading(this, getString(R.string.home_other_app_title)))
                tabContent.addView(Ui.hint(this, getString(R.string.home_molly_other_apps)))
                val rows = mutableListOf(getString(R.string.proxy_host) to Settings.HOST, getString(R.string.proxy_port) to settings.port.toString())
                if (settings.auth) rows += getString(R.string.proxy_user) to settings.user
                tabContent.addView(Ui.valueBlock(this, rows, if (settings.auth) null else getString(R.string.proxy_no_password)).apply {
                    layoutParams = LinearLayout.LayoutParams(-1, -2).apply { topMargin = Ui.dp(this@MainActivity, 12) }
                    if (settings.auth) addView(Ui.passwordRow(this@MainActivity, settings.pass, passwordVisible) {
                        passwordVisible = !passwordVisible; renderSetup()
                    })
                })
            }
            if (!settings.appSeen) {
                setup.addView(Ui.hint(this, getString(R.string.home_app_not_seen)).apply {
                    setPadding(0, Ui.dp(this@MainActivity, 12), 0, 0)
                })
            }
        }
        HomeSetup.appToggleLabel(settings.appSeen, expanded)?.let { label ->
            setup.addView(Ui.textButton(this, getString(label)) {
                expanded = !expanded; passwordVisible = false; renderSetup()
            }.apply { gravity = Gravity.CENTER })
        }
    }

    private fun addLink() {
        val content = Ui.column(this)
        val edit = android.widget.EditText(this).apply {
            hint = getString(R.string.home_add_hint)
            inputType = android.text.InputType.TYPE_CLASS_TEXT or android.text.InputType.TYPE_TEXT_FLAG_MULTI_LINE
            Ui.dimHint(this)
        }
        content.addView(edit)
        content.addView(Ui.textButton(this, getString(R.string.home_paste)) {
            val clipboard = getSystemService(CLIPBOARD_SERVICE) as android.content.ClipboardManager
            clipboard.primaryClip?.takeIf { it.itemCount > 0 }?.getItemAt(0)?.coerceToText(this)?.let { edit.setText(it) }
        })
        val dialog = AlertDialog.Builder(this).setTitle(R.string.home_add_title).setView(content)
            .setNegativeButton(android.R.string.cancel, null).setPositiveButton(R.string.home_continue, null).create()
        dialog.setOnShowListener {
            dialog.getButton(AlertDialog.BUTTON_POSITIVE).setOnClickListener {
                if (edit.text.toString().isBlank()) { edit.error = getString(R.string.home_add_hint); return@setOnClickListener }
                LinkImporter.importLink(this, settings, worker, edit.text.toString()) { subscribed ->
                    dialog.dismiss()
                    if (subscribed) refreshImportedSubscription() else if (PassageService.active) PassageService.start(this, "settings changed")
                    refresh()
                }
            }
        }
        dialog.showStyled(primary = true)
    }

    private fun refreshImportedSubscription() {
        worker.execute {
            val previous = settings.routesConfig
            var restart = false
            try {
                PassageService.withSubscriptionRouter(previous) {
                    val request = Subscriptions.request(previous).put("initial", true)
                    val result = Native.json(Native.refreshSubscriptions(request.toString().toByteArray(Charsets.UTF_8)))
                    if (result.has("error")) throw IllegalStateException()
                    val config = result.getJSONObject("config")
                    synchronized(Settings.configLock) {
                        if (!settings.saveSubscriptionRefresh(previous, config)) throw IllegalStateException()
                        if (!result.optBoolean("changed")) PassageService.savedRefreshMetadata(previous, config.toString())
                    }
                    restart = result.optBoolean("changed")
                }
                if (restart && PassageService.active) PassageService.start(this, "settings changed")
            } catch (_: Throwable) {
                runOnUiThread { Toast.makeText(this, R.string.sub_save_failed, Toast.LENGTH_LONG).show() }
            }
            runOnUiThread { if (!isFinishing && !isDestroyed) refresh() }
        }
    }

    private fun openProxyLink(link: String, title: String) {
        val view = Intent(Intent.ACTION_VIEW, Uri.parse(link))
        val handlers = packageManager.queryIntentActivities(view, PackageManager.MATCH_DEFAULT_ONLY)
        fun unavailable() {
            Toast.makeText(this, getString(R.string.no_app_for_link), Toast.LENGTH_LONG).show()
        }
        fun choose(target: Intent) {
            try {
                val chooser = Intent.createChooser(target, null)
                chooser.putExtra(Intent.EXTRA_AUTO_LAUNCH_SINGLE_CHOICE, false)
                startActivity(chooser)
            } catch (_: ActivityNotFoundException) {
                unavailable()
            }
        }
        when {
            handlers.isEmpty() -> unavailable()
            Build.VERSION.SDK_INT >= 29 -> choose(view)
            else -> AlertDialog.Builder(this)
                .setTitle(title)
                .setItems(handlers.map { it.loadLabel(packageManager) }.toTypedArray()) { _, which ->
                    val activity = handlers[which].activityInfo
                    choose(view.setClassName(activity.packageName, activity.name))
                }
                .show()
        }
    }

    private fun onStartStop() {
        if (PassageService.active) {
            PassageService.stop(this)
            main.postDelayed({ refresh() }, 300)
        } else {
            onStartClicked()
        }
    }

    private fun onStartClicked() {
        PassageService.start(this)
        main.postDelayed({ refresh() }, 500)
        askNotifications()
    }

    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<out String>, grantResults: IntArray) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults)
        if (grantResults.firstOrNull() == PackageManager.PERMISSION_GRANTED) PassageService.refreshNotification(this)
    }

    /**
     * Android 13+ asks before an app may show notifications. The proxy runs
     * without it, but its notification (route, Stop button) stays hidden, so
     * say why first; once, since Android stops asking after two refusals.
     */
    private fun askNotifications() {
        if (Build.VERSION.SDK_INT < 33 ||
            checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED ||
            settings.askedNotifications
        ) {
            return
        }
        settings.askedNotifications = true
        AlertDialog.Builder(this)
            .setTitle(R.string.notifications_title)
            .setMessage(R.string.notifications_why)
            .setPositiveButton(R.string.notifications_continue) { _, _ ->
                requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
            }
            .setNegativeButton(R.string.notifications_not_now, null)
            .showStyled()
    }

    /** Leaves a manually chosen route: the same change the route selector makes. */
    private fun backToAuto() {
        settings.mode = "auto"
        if (PassageService.active) worker.execute {
            try {
                Native.setMode("auto".toByteArray(Charsets.UTF_8))
            } catch (_: Throwable) {
            }
        }
        refresh()
    }

    private fun refresh() {
        if (isFinishing || isDestroyed) return
        showNetworkTest()
        updates.render()
        val active = PassageService.active
        val hasLink = settings.hasLink
        val error = PassageService.lastError != null
        if (!active) { previousTraffic = null; trafficUntil = 0L; healthState = ""; healthReason = "" }
        applyPlain()
        primary.visibility = if (!hasLink || !active || error) View.VISIBLE else View.GONE
        primary.setText(if (!hasLink) R.string.home_add_link else if (error) R.string.home_try_again else R.string.home_turn_on)
        secondary.visibility = if (active || !hasLink) View.VISIBLE else View.GONE
        secondary.setText(if (active) R.string.home_turn_off else R.string.home_turn_on)
        manualBox.visibility = if (settings.mode == "auto") View.GONE else View.VISIBLE
        val notice = Subscriptions.decodeNotice(settings.routesNotice)
        routesNoticeBox.visibility = if (notice == null) View.GONE else View.VISIBLE
        if (notice != null) routesNotice.text = notice.clock()?.let {
            getString(R.string.sub_routes_timed, notice.added, notice.removed, it)
        } ?: getString(R.string.sub_routes_updated, notice.added, notice.removed)
        batteryCard.visibility = if (hasLink && active && !Ui.batteryExempt(this)) View.VISIBLE else View.GONE
        val hint = BackgroundHint.card(hasLink, active, vendor, settings.backgroundHintDismissed, cleanerKill, settings.backgroundKillSeen)
        backgroundCard.visibility = if (hint == BackgroundHint.Card.NONE) View.GONE else View.VISIBLE
        if (hint != BackgroundHint.Card.NONE) {
            backgroundTitle.setText(if (hint == BackgroundHint.Card.KILLED) R.string.bg_killed_title else R.string.bg_advance_title)
            backgroundText.text = getString(if (hint == BackgroundHint.Card.KILLED) R.string.bg_killed_reason else R.string.bg_advance_reason) +
                "\n\n" + getString(vendor.steps)
        }
        if (hint != backgroundShown) {
            backgroundShown = hint
            if (hint != BackgroundHint.Card.NONE) Diagnostics.event("background hint shown: ${hint.name.lowercase()} vendor=${vendor.name.lowercase()}")
        }
        renderSetup()
        if (!active) {
            whatsappStarted = 0L
            whatsappForwards = null
            renderWhatsAppStatus()
        } else if (whatsappStarted == 0L) whatsappStarted = SystemClock.elapsedRealtime()
        if (active) worker.execute {
            var health = "" to ""
            var channels = ""
            var forwards: org.json.JSONArray? = null
            val counters = try {
                val status = Native.status()
                forwards = status.optJSONArray("forwards")
                channels = StatusText.channels(this, status, homeChannels)
                status.optJSONObject("health")?.let { health = it.optString("state") to it.optString("reason") }
                val stats = status.optJSONObject("stats")
                val routes = stats?.keys()?.asSequence()?.mapNotNull { stats.optJSONObject(it) }?.toList().orEmpty()
                HomeSetup.TrafficCounters(routes.sumOf { it.optLong("conns") }, routes.sumOf { it.optLong("up") }, routes.sumOf { it.optLong("down") })
            } catch (_: Throwable) { null }
            runOnUiThread {
                if (isFinishing || isDestroyed || !PassageService.active) return@runOnUiThread
                healthState = health.first
                healthReason = health.second
                channelLine = channels
                whatsappForwards = forwards
                renderWhatsAppStatus()
                if (counters != null) {
                    trafficUntil = HomeSetup.trafficDeadline(previousTraffic, counters, SystemClock.elapsedRealtime(), trafficUntil)
                    previousTraffic = counters
                    if (!settings.appSeen && counters.connections > 0) {
                        settings.appSeen = true
                        refresh()
                        return@runOnUiThread
                    }
                }
                applyPlain()
            }
        }
    }

    private var whatsappStatusView: TextView? = null
    private var whatsappForwards: org.json.JSONArray? = null
    private var whatsappStarted = 0L

    private fun renderWhatsAppStatus() {
        val view = whatsappStatusView ?: return
        if (!PassageService.active || !settings.whatsappEnabled) {
            view.text = ""
            return
        }
        val status = WhatsApp.status(whatsappForwards, WhatsApp.Labels(
            getString(R.string.whatsapp_chat), getString(R.string.whatsapp_media),
            getString(R.string.whatsapp_waiting), getString(R.string.whatsapp_last), getString(R.string.whatsapp_down)))
        val lines = (status.lines + status.warnings).toMutableList()
        if (WhatsApp.showHint(settings.whatsappEnabled, PassageService.active, status.connected,
                SystemClock.elapsedRealtime() - whatsappStarted)) lines += getString(R.string.whatsapp_check)
        view.text = lines.joinToString("\n")
    }

    private val homeChannels = StatusChannels.Display()
    private var channelLine = ""
    @Volatile private var healthState = ""
    @Volatile private var healthReason = ""

    private fun applyPlain() {
        val active = PassageService.active
        val error = PassageService.lastError != null
        val plain = HomeSetup.plain(active, error, settings.hasLink, healthState, healthReason)
        stateView.text = getString(plain.title)
        val reason = getString(plain.reason(settings.appSeen))
        reasonView.text = if (active) plain.detail(reason, channelLine) else reason
        renderDot(HomeSetup.dotFor(plain, SystemClock.elapsedRealtime() < trafficUntil))
    }

    private fun cancelDotAnimation() {
        dotAnimator?.cancel()
        dotAnimator = null
        if (::statusRing.isInitialized) {
            statusRing.alpha = 0f
            statusRing.scaleX = 1f
            statusRing.scaleY = 1f
        }
    }

    private fun renderDot(state: HomeSetup.DotState) {
        val color = getColor(when (state) {
            HomeSetup.DotState.OFF -> R.color.proxy_off_dot
            HomeSetup.DotState.ON, HomeSetup.DotState.TRAFFIC -> R.color.proxy_traffic_dot
            HomeSetup.DotState.WARN -> R.color.proxy_warn_dot
            HomeSetup.DotState.PROBLEM -> R.color.proxy_problem_dot
        })
        statusDot.contentDescription = getString(when (state) {
            HomeSetup.DotState.OFF -> R.string.home_dot_off
            HomeSetup.DotState.TRAFFIC -> R.string.home_dot_traffic
            HomeSetup.DotState.PROBLEM -> R.string.home_start_failed_title
            HomeSetup.DotState.WARN -> R.string.home_dot_warn
            else -> R.string.home_dot_on
        })
        val enabled = android.provider.Settings.Global.getFloat(contentResolver,
            android.provider.Settings.Global.ANIMATOR_DURATION_SCALE, 1f) > 0f
        (statusDot.background as GradientDrawable).setColor(color)
        if (HomeSetup.ringRequested(state, resumed) && PassageService.active && enabled) {
            if (dotAnimator == null) dotAnimator = ObjectAnimator.ofPropertyValuesHolder(statusRing,
                PropertyValuesHolder.ofFloat(View.SCALE_X, 1f, 2.2f),
                PropertyValuesHolder.ofFloat(View.SCALE_Y, 1f, 2.2f),
                PropertyValuesHolder.ofFloat(View.ALPHA, .5f, 0f)).apply {
                duration = 1_200
                repeatCount = ValueAnimator.INFINITE
                start()
            }
        } else {
            cancelDotAnimation()
        }
    }

    /** The report goes through the share sheet, so a tester can send it straight from here. */
    private fun shareReport(networkTest: String? = null) {
        worker.execute {
            val status = try { Native.status() } catch (e: Throwable) {
                org.json.JSONObject().put("running", false).put("error", e.toString())
            }
            val report = try {
                StatusText.report(this, status, NetInfo.snapshot(this), settings, appVersion(), networkTest)
            } catch (e: Throwable) {
                Diagnostics.error("report failed", e)
                org.json.JSONObject().put("error", e.javaClass.name)
                    .put("device", Diagnostics.device()).put("diagnostics", Diagnostics.snapshot()).toString(2)
            }
            val uri = try { ReportFiles.write(this, report) } catch (e: Exception) {
                Diagnostics.error("report attachment failed", e)
                runOnUiThread { if (!isFinishing && !isDestroyed) Toast.makeText(this, R.string.report_failed, Toast.LENGTH_LONG).show() }
                return@execute
            }
            runOnUiThread {
                if (isFinishing || isDestroyed) return@runOnUiThread
                val send = Intent(Intent.ACTION_SEND)
                    .setType("text/plain")
                    .putExtra(Intent.EXTRA_SUBJECT, "Passage report")
                    .putExtra(Intent.EXTRA_STREAM, uri)
                    .apply { clipData = android.content.ClipData.newUri(contentResolver, "Passage report", uri) }
                    .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
                @Suppress("DEPRECATION")
                val handlers = packageManager.queryIntentActivities(send, PackageManager.MATCH_DEFAULT_ONLY).size
                if (ReportFiles.delivery(handlers) == ReportFiles.Delivery.COPY) {
                    copyReportFallback(report)
                    return@runOnUiThread
                }
                try {
                    startActivity(Intent.createChooser(send, getString(R.string.share_report)))
                } catch (_: ActivityNotFoundException) {
                    copyReportFallback(report)
                }
            }
        }
    }

    private fun copyReportFallback(report: String) {
        Ui.copy(this, getString(R.string.share_report), report, getString(R.string.report_copied))
    }

    private fun appVersion(): String = try {
        packageManager.getPackageInfo(packageName, 0).versionName ?: "?"
    } catch (_: Exception) {
        "?"
    }

    /** Runs the network test before sharing a report. */
    private fun sendReport() {
        if (networkTest != null) return
        if (!PassageService.active) {
            shareReport()
            return
        }
        val test = NetworkTest()
        networkTest = test
        val context = applicationContext
        showNetworkTest()
        worker.execute {
            test.result = try {
                val d = Native.json(Native.diagnose())
                Diagnostics.event("network test: ${StatusText.diagnosisSummary(d)}")
                StatusText.diagnosis(context, d)
            } catch (e: Throwable) {
                Diagnostics.error("network test failed", e)
                Diagnostics.redact(e.message ?: e.toString())
            }
        }
    }

    /** While a test runs, a progress dialog; when it ends, the report. */
    private fun showNetworkTest() {
        val test = networkTest ?: return
        val text = test.result
        if (text == null) {
            if (testDialog == null) {
                testDialog = AlertDialog.Builder(this)
                    .setTitle(R.string.test_network)
                    .setMessage(R.string.testing_network)
                    .setCancelable(false)
                    .show()
            }
            return
        }
        testDialog?.dismiss()
        testDialog = null
        networkTest = null
        shareReport(text)
    }

}
