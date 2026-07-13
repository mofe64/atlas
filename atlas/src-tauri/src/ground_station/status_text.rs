use tonic::Status;

use crate::database::StatusEventInput;

use super::proto::pb;

const MAX_STATUS_TEXT_BYTES: usize = 1_024;

#[allow(clippy::result_large_err)]
pub(super) fn input(
    message: pb::AgentStatusText,
    received_at_unix_ms: i64,
) -> Result<StatusEventInput, Status> {
    let source = message.source.trim();
    let text = message.text.trim();
    let severity = message.severity.trim().to_ascii_uppercase();
    if message.observed_at_unix_ms <= 0 || source.is_empty() || text.is_empty() {
        return Err(Status::invalid_argument(
            "status text observation time, source, and text are required",
        ));
    }
    if text.len() > MAX_STATUS_TEXT_BYTES {
        return Err(Status::invalid_argument(
            "status text exceeds the 1024-byte limit",
        ));
    }
    if !matches!(
        severity.as_str(),
        "DEBUG"
            | "INFO"
            | "NOTICE"
            | "WARNING"
            | "ERROR"
            | "CRITICAL"
            | "ALERT"
            | "EMERGENCY"
            | "UNKNOWN"
    ) {
        return Err(Status::invalid_argument(
            "status text severity is not recognized",
        ));
    }

    Ok(StatusEventInput {
        origin: "px4".to_string(),
        event_type: "status_text".to_string(),
        code: None,
        details_json: None,
        source: source.to_string(),
        severity,
        message: text.to_string(),
        agent_observed_at_unix_ms: message.observed_at_unix_ms,
        received_at_unix_ms,
    })
}
