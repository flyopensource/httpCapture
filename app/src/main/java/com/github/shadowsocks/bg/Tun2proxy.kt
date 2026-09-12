package com.github.shadowsocks.bg

object Tun2proxy {
    init {
        System.loadLibrary("tun2proxy")
    }

    @JvmStatic external fun run(cliArgs: String, tunMtu: Char): Int
    @JvmStatic external fun stop(): Int
}
