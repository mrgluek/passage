package info.gluek.passage

object HomeSetup {
    enum class DotState { OFF, ON, TRAFFIC, WARN, PROBLEM }
    data class TrafficCounters(val connections: Long, val up: Long, val down: Long)

    fun trafficChanged(previous: TrafficCounters?, current: TrafficCounters): Boolean = previous != null && previous != current

    fun trafficDeadline(previous: TrafficCounters?, current: TrafficCounters, now: Long, deadline: Long): Long =
        if (trafficChanged(previous, current)) now + 4_000 else deadline

    fun dotState(running: Boolean, startFailed: Boolean, flowing: Boolean): DotState = when {
        startFailed -> DotState.PROBLEM
        !running -> DotState.OFF
        flowing -> DotState.TRAFFIC
        else -> DotState.ON
    }

    /** The plain verdict the status card shows, from the app's facts and the core's health. */
    enum class Plain(val title: Int, private val reason: Int) {
        NOT_SET_UP(R.string.home_setup_title, R.string.home_setup_reason),
        OFF(R.string.home_off_title, R.string.home_off_reason),
        START_FAILED(R.string.home_start_failed_title, R.string.home_start_failed_reason),
        CONNECTING(R.string.home_connecting_title, R.string.home_connecting_reason),
        READY(R.string.home_ready_title, R.string.home_ready_reason),
        WORKING(R.string.home_working_title, R.string.home_working_reason),
        LIMITED(R.string.home_working_title, R.string.home_limited_reason),
        NO_INTERNET(R.string.home_no_internet_title, R.string.home_no_internet_reason),
        SERVER_DOWN(R.string.home_server_down_title, R.string.home_server_down_reason),
        NO_ROUTE(R.string.home_no_route_title, R.string.home_no_route_reason),
        SLOW(R.string.home_slow_title, R.string.home_slow_reason);

        fun reason(appSeen: Boolean): Int =
            if (this == READY && appSeen) R.string.home_working_reason else reason

        /** The line under the title: the channels while Passage works, else the reason. */
        fun detail(reasonText: String, channels: String): String = when {
            channels.isEmpty() -> reasonText
            this == WORKING -> channels
            this == LIMITED -> "$reasonText $channels"
            else -> reasonText
        }
    }

    fun plain(running: Boolean, startFailed: Boolean, hasLink: Boolean, health: String, reason: String): Plain = when {
        startFailed -> Plain.START_FAILED
        running && health == "starting" -> Plain.CONNECTING
        running && health == "problem" -> when (reason) {
            "no_internet" -> Plain.NO_INTERNET
            "server_down" -> Plain.SERVER_DOWN
            "no_route" -> Plain.NO_ROUTE
            else -> Plain.SLOW
        }
        running && health == "limited" -> Plain.LIMITED
        running && health == "working" -> Plain.WORKING
        running -> Plain.READY
        !hasLink -> Plain.NOT_SET_UP
        else -> Plain.OFF
    }

    fun dotFor(plain: Plain, flowing: Boolean): DotState = when (plain) {
        Plain.START_FAILED, Plain.NO_ROUTE -> DotState.PROBLEM
        Plain.CONNECTING, Plain.NO_INTERNET, Plain.SERVER_DOWN, Plain.SLOW -> DotState.WARN
        Plain.NOT_SET_UP, Plain.OFF -> DotState.OFF
        else -> if (flowing) DotState.TRAFFIC else DotState.ON
    }

    enum class AppTab(val label: Int) {
        DELTA_CHAT(R.string.home_tab_delta),
        VELTA(R.string.home_tab_velta),
        TELEGRAM(R.string.home_tab_telegram),
        OTHER(R.string.home_tab_other),
    }

    const val DELTA_CHAT_DOWNLOAD = "https://download.delta.chat"
    const val VELTA_DOWNLOAD = "https://velta.cc/#download"

    data class ConnectContent(val instruction: Int?, val action: Int?, val clipboard: String?, val open: String?, val download: String?)

    fun connect(tab: AppTab, socksLink: String, telegramLink: String, veltaLink: String): ConnectContent = when (tab) {
        AppTab.DELTA_CHAT -> ConnectContent(R.string.home_delta_how, R.string.copy_proxy_link, socksLink, null, DELTA_CHAT_DOWNLOAD)
        AppTab.VELTA -> ConnectContent(R.string.home_velta_how, R.string.home_velta_open, veltaLink, veltaLink, VELTA_DOWNLOAD)
        AppTab.TELEGRAM -> ConnectContent(R.string.home_telegram_how, R.string.home_telegram_open, telegramLink, telegramLink, null)
        AppTab.OTHER -> ConnectContent(R.string.home_other_how, null, null, null, null)
    }

    fun isCredentialLink(link: String): Boolean = when (link.substringBefore("://")) {
        "socks5" -> '@' in link.substringAfter("://").substringBefore('/')
        "tg" -> link.substringAfter('?', "").split('&').any { it.startsWith("user=") || it.startsWith("pass=") }
        "velta" -> "/U/" in link || "/W/" in link
        else -> false
    }

    fun step(hasLink: Boolean, running: Boolean, appSeen: Boolean): Int = when {
        !hasLink -> 1
        !running -> 2
        !appSeen -> 3
        else -> 4
    }

    fun heading(hasLink: Boolean, running: Boolean, appSeen: Boolean): Int =
        if (step(hasLink, running, appSeen) == 4) R.string.home_steps_done else R.string.home_steps_pending

    fun migrateAppSeen(saved: Boolean?, hasLink: Boolean, wasRunning: Boolean): Boolean =
        saved ?: (hasLink && wasRunning)

    fun appTabColumns(fontScale: Float, availableWidth: Int, minimumLabelWidth: Int): Int =
        if (fontScale > 1.2f || minimumLabelWidth * 4 > availableWidth) 2 else 4

    fun appTabsVisible(connected: Boolean, expanded: Boolean): Boolean = !connected || expanded

    fun appToggleLabel(connected: Boolean, expanded: Boolean): Int? = when {
        !connected -> null
        expanded -> R.string.home_hide
        else -> R.string.home_connect_another
    }

    fun password(value: String, visible: Boolean): String = if (visible) value else "••••••••"
}
