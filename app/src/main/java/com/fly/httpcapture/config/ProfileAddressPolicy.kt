package com.fly.httpcapture.config

data class ProfileAddress(val host: String, val port: Int)

object ProfileAddressPolicy {
    fun parse(hostInput: String, portInput: String): ProfileAddress {
        val host = hostInput.trim().removeSurrounding("[", "]")
        require(host.isNotEmpty()) { "IP/Host 不能为空" }
        require(host.length <= 253) { "IP/Host 过长" }
        require(host.none(Char::isWhitespace) && !host.contains(Regex("[/?#]")) && !host.contains("://")) {
            "IP/Host 格式无效，请勿包含协议、路径或空格"
        }
        if (host.all { it.isDigit() || it == '.' }) {
            val parts = host.split('.')
            require(parts.size == 4 && parts.all { part -> part.isNotEmpty() && part.toIntOrNull()?.let { it in 0..255 } == true }) {
                "IPv4 地址格式无效"
            }
        }
        val port = portInput.trim().toIntOrNull()
        require(port != null && port in 1..65535) { "端口必须是 1–65535 的整数" }
        return ProfileAddress(host, port)
    }

    fun update(profiles: List<CaptureProfile>, profileId: String, address: ProfileAddress): List<CaptureProfile> {
        require(profiles.any { it.id == profileId }) { "Charles 配置不存在" }
        return profiles.map { profile ->
            if (profile.id == profileId) profile.copy(host = address.host, port = address.port) else profile
        }
    }
}
