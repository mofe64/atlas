mod perception;
mod proto;
mod registration;
mod server;
mod session;
mod status_text;
mod telemetry;

pub(crate) use command_router::CommandRouter;
pub(crate) use perception::{AlignedPerceptionFrameSnapshot, PerceptionSnapshot, PerceptionStore};
pub(crate) use server::serve;

use std::time::{SystemTime, UNIX_EPOCH};

pub(super) fn unix_time_ms() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis() as i64
}

mod command_router;
#[cfg(test)]
mod tests;
