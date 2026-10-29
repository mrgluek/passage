package info.gluek.passage

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/** Starts the proxy after a reboot when the user asked for it and it was running. */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED) return
        val s = Settings(context)
        if (s.startOnBoot && s.running && s.link.isNotEmpty()) {
            PassageService.start(context)
        }
    }
}
