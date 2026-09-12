package com.fly.httpcapture.config

import android.content.Context
import org.json.JSONArray
import org.json.JSONObject

class ConfigStore(context: Context) {
    private val preferences = context.getSharedPreferences("capture_config", Context.MODE_PRIVATE)

    fun load(): CaptureSettings {
        val profiles = runCatching {
            val array = JSONArray(preferences.getString(KEY_PROFILES, "[]"))
            buildList {
                for (index in 0 until array.length()) {
                    val item = array.getJSONObject(index)
                    add(
                        CaptureProfile(
                            id = item.getString("id"),
                            name = item.getString("name"),
                            host = item.getString("host"),
                            port = item.getInt("port"),
                            certificateDerBase64 = item.getString("certificateDer"),
                            certificateSha256 = item.getString("certificateSha256"),
                        )
                    )
                }
            }
        }.getOrDefault(emptyList())
        val packages = preferences.getStringSet(KEY_PACKAGES, emptySet()).orEmpty().toSet()
        val activeId = preferences.getString(KEY_ACTIVE_PROFILE, null)
        return CaptureSettings(profiles, activeId, packages)
    }

    fun upsertProfile(profile: CaptureProfile) {
        val current = load()
        val result = ProfileMergePolicy.upsert(current.profiles, current.activeProfileId, profile)
        saveProfiles(result.profiles)
        preferences.edit().putString(KEY_ACTIVE_PROFILE, result.activeProfileId).apply()
    }

    fun selectProfile(id: String) {
        preferences.edit().putString(KEY_ACTIVE_PROFILE, id).apply()
    }

    fun removeProfile(id: String) {
        val current = load()
        saveProfiles(current.profiles.filterNot { it.id == id })
        if (current.activeProfileId == id) {
            preferences.edit().putString(KEY_ACTIVE_PROFILE, current.profiles.firstOrNull { it.id != id }?.id).apply()
        }
    }

    fun updateProfileAddress(id: String, address: ProfileAddress) {
        val current = load()
        saveProfiles(ProfileAddressPolicy.update(current.profiles, id, address))
    }

    fun savePackages(packages: Set<String>) {
        preferences.edit().putStringSet(KEY_PACKAGES, packages).apply()
    }

    private fun saveProfiles(profiles: List<CaptureProfile>) {
        val array = JSONArray()
        profiles.forEach { profile ->
            array.put(JSONObject().apply {
                put("id", profile.id)
                put("name", profile.name)
                put("host", profile.host)
                put("port", profile.port)
                put("certificateDer", profile.certificateDerBase64)
                put("certificateSha256", profile.certificateSha256)
            })
        }
        preferences.edit().putString(KEY_PROFILES, array.toString()).apply()
    }

    companion object {
        private const val KEY_PROFILES = "profiles"
        private const val KEY_ACTIVE_PROFILE = "active_profile"
        private const val KEY_PACKAGES = "packages"
    }
}

internal object ProfileMergePolicy {
    data class Result(val profiles: List<CaptureProfile>, val activeProfileId: String)

    fun upsert(
        profiles: List<CaptureProfile>,
        activeProfileId: String?,
        incoming: CaptureProfile,
    ): Result {
        val matches = profiles.filter { existing ->
            existing.id == incoming.id || existing.name.trim().equals(incoming.name.trim(), ignoreCase = true)
        }
        val existing = matches.firstOrNull { it.id == activeProfileId }
            ?: matches.firstOrNull { it.id == incoming.id }
            ?: matches.firstOrNull()
        val updated = incoming.copy(id = existing?.id ?: incoming.id)
        return Result(profiles.filterNot(matches::contains) + updated, updated.id)
    }
}
