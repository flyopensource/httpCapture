package com.fly.httpcapture.sample

import android.app.Activity
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.ViewGroup
import android.widget.Button
import android.widget.LinearLayout
import android.widget.TextView
import java.net.HttpURLConnection
import java.net.URL
import kotlin.concurrent.thread

class MainActivity : Activity() {
    private lateinit var result: TextView

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val padding = (24 * resources.displayMetrics.density).toInt()
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(padding, padding, padding, padding)
            addView(TextView(context).apply {
                text = "HTTP Capture 测试客户端\nDebug 包接入 debug-trust；Release 包不接入。"
                textSize = 18f
            })
            addView(Button(context).apply {
                text = "发送 HTTP 请求"
                setOnClickListener { request("http://httpbin.org/get?source=httpcapture") }
            }, ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT)
            addView(Button(context).apply {
                text = "发送 HTTPS 请求"
                setOnClickListener { request("https://httpbin.org/get?source=httpcapture") }
            }, ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT)
            result = TextView(context).apply { text = "等待请求" }
            addView(result)
        }
        setContentView(root)
    }

    private fun request(url: String) {
        result.text = "请求中：$url"
        thread {
            val message = runCatching {
                val connection = URL(url).openConnection() as HttpURLConnection
                connection.connectTimeout = 8_000
                connection.readTimeout = 8_000
                connection.setRequestProperty("X-HttpCapture-Sample", "android")
                val code = connection.responseCode
                connection.inputStream.bufferedReader().use { it.readText().take(300) }
                "HTTP $code\n$url"
            }.getOrElse { "失败：${it.javaClass.simpleName}: ${it.message}" }
            Handler(Looper.getMainLooper()).post { result.text = message }
        }
    }
}
