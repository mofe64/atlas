mod perception;
mod proto;
mod registration;
mod server;
mod session;
mod spatial;
mod status_text;
mod telemetry;

pub(crate) use command_router::CommandRouter;
pub(crate) use indoor::{IndoorExploreControl, IndoorExploreSnapshot, IndoorExploreStore};
pub(crate) use perception::{AlignedPerceptionFrameSnapshot, PerceptionSnapshot, PerceptionStore};
pub(crate) use server::serve;
pub(crate) use spatial::{SpatialSnapshot, SpatialStore};

use std::time::{SystemTime, UNIX_EPOCH};

pub(super) fn unix_time_ms() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis() as i64
}

mod command_router;
mod indoor;
#[cfg(test)]
mod tests;
