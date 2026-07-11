use serde::Serialize;

/// Values supplied by the compiled native host rather than the webview.
#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct RuntimeInfo {
    app_version: &'static str,
    target_arch: &'static str,
    target_os: &'static str,
}

/// Tauri commands are the deliberately small IPC boundary between React and Rust.
#[tauri::command]
fn runtime_info() -> RuntimeInfo {
    RuntimeInfo {
        app_version: env!("CARGO_PKG_VERSION"),
        target_arch: std::env::consts::ARCH,
        target_os: std::env::consts::OS,
    }
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .invoke_handler(tauri::generate_handler![runtime_info])
        .run(tauri::generate_context!())
        .expect("error while running Atlas");
}

#[cfg(test)]
mod tests {
    use super::runtime_info;

    #[test]
    fn runtime_info_comes_from_the_compiled_host() {
        let info = runtime_info();

        assert!(!info.app_version.is_empty());
        assert!(!info.target_arch.is_empty());
        assert!(!info.target_os.is_empty());
    }
}
