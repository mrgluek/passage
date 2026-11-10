package info.gluek.passage

import android.app.LocaleManager
import android.content.Context
import android.content.res.Configuration
import android.content.res.Resources
import android.os.Build
import android.os.LocaleList
import java.util.Locale

object AppLanguage {
    val choices = listOf("system", "en", "ru")

    fun resolve(choice: String, system: Locale): Locale = when (choice) {
        "en" -> Locale.ENGLISH
        "ru" -> Locale.forLanguageTag("ru")
        else -> system
    }

    fun wrap(context: Context): Context =
        if (Build.VERSION.SDK_INT >= 33) context else localizedContext(context)

    fun localizedContext(context: Context): Context {
        val system = if (Build.VERSION.SDK_INT >= 33) context.getSystemService(LocaleManager::class.java).systemLocales
            else Resources.getSystem().configuration.locales
        val choice = Settings(context).language
        val locales = if (choice == "system") system else LocaleList(resolve(choice, system[0]))
        val configuration = Configuration(context.resources.configuration).apply {
            setLocales(locales)
            setLayoutDirection(locales[0])
        }
        return context.createConfigurationContext(configuration)
    }

    fun applyPlatform(context: Context) {
        if (Build.VERSION.SDK_INT >= 33) {
            val manager = context.getSystemService(LocaleManager::class.java)
            val choice = Settings(context).language
            val locales = LocaleList.forLanguageTags(if (choice == "system") "" else choice)
            if (manager.applicationLocales != locales) manager.applicationLocales = locales
        }
    }

    fun select(context: Context, choice: String) {
        Settings(context).language = choice
        applyPlatform(context)
        PassageService.refreshNotification(context)
    }
}
