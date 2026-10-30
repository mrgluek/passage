package info.gluek.passage

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/** After an app update, turns the proxy back on if it was on before. */
class UpdateReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_MY_PACKAGE_REPLACED) return
        if (Settings(context).running) PassageService.start(context)
    }
}
