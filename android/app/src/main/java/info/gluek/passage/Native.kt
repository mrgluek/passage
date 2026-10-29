package info.gluek.passage

import org.json.JSONObject

/**
 * JNI surface of libpassage.so (core/cmd/libpassage): the router. Text
 * crosses as UTF-8 bytes; every call that returns bytes returns JSON, errors
 * as {"error": "..."}.
 */
object Native {
    init {
        System.loadLibrary("passage")
    }

    @JvmStatic external fun parseLink(link: ByteArray): ByteArray
    @JvmStatic external fun start(config: ByteArray): ByteArray
    @JvmStatic external fun stop()
    @JvmStatic external fun status(events: Int): ByteArray
    @JvmStatic external fun setMode(mode: ByteArray): ByteArray
    @JvmStatic external fun networkChanged()
    @JvmStatic external fun versions(): ByteArray

    fun json(out: ByteArray): JSONObject = JSONObject(String(out, Charsets.UTF_8))

    fun parse(link: String): JSONObject = json(parseLink(link.trim().toByteArray(Charsets.UTF_8)))

    fun status(): JSONObject = json(status(40))
}
