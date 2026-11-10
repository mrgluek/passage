package info.gluek.passage

import org.junit.Assert.*
import org.junit.Test
import java.util.Locale

class AppLanguageTest {
    @Test fun systemChoicePreservesSystemLocaleIncludingRegion() {
        for (system in listOf(Locale.US, Locale.UK, Locale.forLanguageTag("ru-RU"), Locale.FRANCE)) {
            assertEquals(system, AppLanguage.resolve("system", system))
        }
    }

    @Test fun explicitLanguageOverridesSystemLanguage() {
        assertEquals(Locale.ENGLISH, AppLanguage.resolve("en", Locale.forLanguageTag("ru-RU")))
        assertEquals(Locale.forLanguageTag("ru"), AppLanguage.resolve("ru", Locale.US))
    }

    @Test fun unknownChoiceFallsBackToSystem() {
        assertEquals(Locale.UK, AppLanguage.resolve("unknown", Locale.UK))
        assertEquals(Locale.FRANCE, AppLanguage.resolve("", Locale.FRANCE))
    }
}
