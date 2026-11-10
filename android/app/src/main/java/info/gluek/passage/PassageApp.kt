package info.gluek.passage

import android.app.Application
import android.content.Context
import android.os.Process
import kotlin.system.exitProcess

class PassageApp : Application() {
    override fun attachBaseContext(base: Context) = super.attachBaseContext(AppLanguage.wrap(base))

    override fun onCreate() {
        super.onCreate()
        AppLanguage.applyPlatform(this)
        Diagnostics.init(this)
        val previous = Thread.getDefaultUncaughtExceptionHandler()
        Thread.setDefaultUncaughtExceptionHandler { thread, error ->
            Diagnostics.error("uncaught exception on ${thread.name}", error, fatal = true)
            if (previous != null) {
                previous.uncaughtException(thread, error)
            } else {
                Process.killProcess(Process.myPid())
                exitProcess(10)
            }
        }
        val version = packageManager.getPackageInfo(packageName, 0).versionName
        Diagnostics.event("process started: version=$version sdk=${android.os.Build.VERSION.SDK_INT}")
        Thread({ Diagnostics.collectExits(this) }, "passage-exits").start()
    }
}
