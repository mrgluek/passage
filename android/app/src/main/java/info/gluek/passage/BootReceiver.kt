package info.gluek.passage

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.os.Build

/** After the phone restarts, turns the proxy back on if it was on and the setting allows it. */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED) return
        // The app was opened before this broadcast arrived: leave the running proxy alone.
        if (PassageService.active) return
        val settings = Settings(context)
        // The user opted out: the proxy stays off after the restart, and opening the app does not turn it on.
        if (!settings.startOnBoot && settings.running) {
            settings.running = false
            Diagnostics.event("device restarted: start after restart is off")
        }
        if (!shouldStart(settings.startOnBoot, settings.running)) return
        Diagnostics.event("device restarted")
        try {
            PassageService.start(context, "device restarted")
        } catch (e: Exception) {
            Diagnostics.error("start after restart failed", e)
            notifyOff(context)
        }
    }

    private fun notifyOff(context: Context) {
        val localized = AppLanguage.localizedContext(context)
        val nm = context.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        val b = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            if (nm.getNotificationChannel(CHANNEL) == null) {
                nm.createNotificationChannel(NotificationChannel(CHANNEL, localized.getString(R.string.restart_channel), NotificationManager.IMPORTANCE_DEFAULT))
            }
            Notification.Builder(context, CHANNEL)
        } else {
            @Suppress("DEPRECATION")
            Notification.Builder(context)
        }
        val open = PendingIntent.getActivity(
            context, 3, Intent(context, MainActivity::class.java),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        nm.notify(NOTIFICATION_ID, b.setSmallIcon(R.drawable.ic_notification)
            .setContentTitle(localized.getString(R.string.restart_off_title))
            .setContentText(localized.getString(R.string.restart_off_text))
            .setContentIntent(open)
            .setAutoCancel(true)
            .build())
    }

    companion object {
        private const val CHANNEL = "restart"
        private const val NOTIFICATION_ID = 3

        /** The proxy comes back only if the user left it on and the setting is on. */
        fun shouldStart(startOnBoot: Boolean, running: Boolean) = startOnBoot && running

        /** The proxy is on, so the "off after the restart" notice is stale. */
        fun clearNotification(context: Context) {
            (context.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager).cancel(NOTIFICATION_ID)
        }
    }
}
