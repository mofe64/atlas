use std::{
    collections::HashMap,
    sync::{
        atomic::{AtomicU64, Ordering},
        Arc, RwLock,
    },
};

use serde::Serialize;

use super::proto::pb::{
    IndoorExploreControlAction, IndoorExploreMissionState, IndoorExploreMissionUpdate,
};

const START_DEADLINE_MS: i64 = 5_000;
static NEXT_ID: AtomicU64 = AtomicU64::new(1);

#[derive(Clone, Default)]
pub(crate) struct IndoorExploreStore {
    missions: Arc<RwLock<HashMap<String, MissionRecord>>>,
}

#[derive(Clone)]
struct MissionRecord {
    snapshot: IndoorExploreSnapshot,
    state: MissionState,
    last_event_id: Option<String>,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
enum MissionState {
    Starting,
    TakingOff,
    Exploring,
    Returning,
    Complete,
    Holding,
    Failed,
}

impl MissionState {
    fn label(self) -> &'static str {
        match self {
            Self::Starting => "starting",
            Self::TakingOff => "taking_off",
            Self::Exploring => "exploring",
            Self::Returning => "returning",
            Self::Complete => "complete",
            Self::Holding => "holding",
            Self::Failed => "failed",
        }
    }

    fn terminal(self) -> bool {
        matches!(self, Self::Complete | Self::Failed)
    }
}

#[derive(Clone, Debug)]
pub(crate) struct IndoorExploreControl {
    pub(crate) operation_id: String,
    pub(crate) mission_id: String,
    pub(crate) drone_id: String,
    pub(crate) action: IndoorExploreControlAction,
    pub(crate) altitude_m: f64,
    pub(crate) requested_at_unix_ms: i64,
    pub(crate) deadline_at_unix_ms: i64,
    pub(crate) reason: String,
}

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub(crate) struct IndoorExploreSnapshot {
    pub(crate) mission_id: String,
    pub(crate) operation_id: String,
    pub(crate) drone_id: String,
    pub(crate) state: String,
    pub(crate) altitude_m: f64,
    pub(crate) requested_at_unix_ms: i64,
    pub(crate) updated_at_unix_ms: i64,
    pub(crate) error_code: String,
    pub(crate) message: String,
}

impl IndoorExploreStore {
    pub(crate) fn prepare_start(
        &self,
        drone_id: &str,
        altitude_m: f64,
        now: i64,
    ) -> Result<IndoorExploreControl, String> {
        if drone_id.trim().is_empty() {
            return Err("Indoor Explore requires a target aircraft".into());
        }
        validate_altitude(altitude_m)?;
        let mut missions = self
            .missions
            .write()
            .map_err(|_| "Indoor Explore state lock was poisoned".to_string())?;
        if missions
            .get(drone_id)
            .is_some_and(|mission| !mission.state.terminal())
        {
            return Err("an Indoor Explore mission is already active for this aircraft".into());
        }

        let mission_id = next_id("indoor-mission", now);
        let operation_id = next_id("indoor-start", now);
        missions.insert(
            drone_id.to_string(),
            MissionRecord {
                snapshot: IndoorExploreSnapshot {
                    mission_id: mission_id.clone(),
                    operation_id: operation_id.clone(),
                    drone_id: drone_id.to_string(),
                    state: MissionState::Starting.label().to_string(),
                    altitude_m,
                    requested_at_unix_ms: now,
                    updated_at_unix_ms: now,
                    error_code: String::new(),
                    message: "Start requested; waiting for Atlas Agent".into(),
                },
                state: MissionState::Starting,
                last_event_id: None,
            },
        );
        Ok(IndoorExploreControl {
            operation_id,
            mission_id,
            drone_id: drone_id.to_string(),
            action: IndoorExploreControlAction::Start,
            altitude_m,
            requested_at_unix_ms: now,
            deadline_at_unix_ms: now + START_DEADLINE_MS,
            reason: "Operator started Indoor Explore".into(),
        })
    }

    pub(crate) fn prepare_abort(
        &self,
        drone_id: &str,
        mission_id: &str,
        reason: &str,
        now: i64,
    ) -> Result<IndoorExploreControl, String> {
        let mut missions = self
            .missions
            .write()
            .map_err(|_| "Indoor Explore state lock was poisoned".to_string())?;
        let mission = missions
            .get_mut(drone_id)
            .filter(|mission| mission.snapshot.mission_id == mission_id)
            .ok_or_else(|| "no matching Indoor Explore mission is active".to_string())?;
        if mission.state.terminal() {
            return Err("the Indoor Explore mission is already terminal".into());
        }
        let operation_id = next_id("indoor-abort", now);
        mission.snapshot.operation_id.clone_from(&operation_id);
        mission.snapshot.updated_at_unix_ms = now;
        mission.snapshot.error_code.clear();
        mission.snapshot.message =
            "Abort-and-return requested; waiting for Atlas Agent".to_string();
        Ok(IndoorExploreControl {
            operation_id,
            mission_id: mission.snapshot.mission_id.clone(),
            drone_id: drone_id.to_string(),
            action: IndoorExploreControlAction::AbortAndReturn,
            altitude_m: mission.snapshot.altitude_m,
            requested_at_unix_ms: now,
            deadline_at_unix_ms: now + START_DEADLINE_MS,
            reason: reason.to_string(),
        })
    }

    pub(crate) fn snapshot(&self, drone_id: &str) -> Option<IndoorExploreSnapshot> {
        self.missions
            .read()
            .ok()?
            .get(drone_id)
            .map(|mission| mission.snapshot.clone())
    }

    pub(crate) fn record_delivery_failure(
        &self,
        drone_id: &str,
        operation_id: &str,
        error_code: &str,
        message: &str,
        now: i64,
    ) {
        if let Ok(mut missions) = self.missions.write() {
            if let Some(mission) = missions
                .get_mut(drone_id)
                .filter(|mission| mission.snapshot.operation_id == operation_id)
            {
                mission.state = MissionState::Failed;
                mission.snapshot.state = MissionState::Failed.label().to_string();
                mission.snapshot.updated_at_unix_ms = now;
                mission.snapshot.error_code = error_code.to_string();
                mission.snapshot.message = message.to_string();
            }
        }
    }

    pub(crate) fn apply_agent_update(
        &self,
        drone_id: &str,
        update: IndoorExploreMissionUpdate,
    ) -> Result<(), String> {
        validate_update(&update)?;
        let next_state = mission_state(update.state())?;
        let mut missions = self
            .missions
            .write()
            .map_err(|_| "Indoor Explore state lock was poisoned".to_string())?;

        if let Some(current) = missions.get_mut(drone_id) {
            if current.last_event_id.as_deref() == Some(update.event_id.as_str()) {
                return Ok(());
            }
            if current.snapshot.mission_id != update.mission_id {
                return Err("Indoor Explore update does not match the active mission".into());
            }
            if current.snapshot.altitude_m != update.altitude_m {
                return Err("Indoor Explore update changed the selected altitude".into());
            }
            if current.snapshot.operation_id != update.operation_id
                && !matches!(next_state, MissionState::Holding | MissionState::Failed)
            {
                return Err("Indoor Explore update does not match the active operation".into());
            }
            if !valid_transition(current.state, next_state) {
                return Err(format!(
                    "invalid Indoor Explore transition from {} to {}",
                    current.state.label(),
                    next_state.label()
                ));
            }
            current.state = next_state;
            current.snapshot.operation_id = update.operation_id;
            current.snapshot.state = next_state.label().to_string();
            current.snapshot.updated_at_unix_ms = update.observed_at_unix_ms;
            current.snapshot.error_code = update.error_code;
            current.snapshot.message = update.message;
            current.last_event_id = Some(update.event_id);
            return Ok(());
        }

        if !matches!(next_state, MissionState::Holding | MissionState::Failed) {
            return Err(
                "Native may recover only a safe holding or failed Indoor Explore mission".into(),
            );
        }
        missions.insert(
            drone_id.to_string(),
            MissionRecord {
                snapshot: IndoorExploreSnapshot {
                    mission_id: update.mission_id,
                    operation_id: update.operation_id,
                    drone_id: drone_id.to_string(),
                    state: next_state.label().to_string(),
                    altitude_m: update.altitude_m,
                    requested_at_unix_ms: update.observed_at_unix_ms,
                    updated_at_unix_ms: update.observed_at_unix_ms,
                    error_code: update.error_code,
                    message: update.message,
                },
                state: next_state,
                last_event_id: Some(update.event_id),
            },
        );
        Ok(())
    }

    pub(crate) fn agent_session_lost(&self, drone_id: &str, now: i64) {
        if let Ok(mut missions) = self.missions.write() {
            if let Some(mission) = missions
                .get_mut(drone_id)
                .filter(|mission| !mission.state.terminal())
            {
                mission.state = MissionState::Holding;
                mission.snapshot.state = MissionState::Holding.label().to_string();
                mission.snapshot.updated_at_unix_ms = now;
                mission.snapshot.error_code = "AGENT_SESSION_LOST".into();
                mission.snapshot.message =
                    "Agent session ended; the onboard contract must establish PX4 Hold".into();
            }
        }
    }
}

fn validate_altitude(altitude_m: f64) -> Result<(), String> {
    if altitude_m == 0.5 || altitude_m == 1.0 || altitude_m == 2.0 {
        Ok(())
    } else {
        Err("Indoor Explore altitude must be exactly 0.5 m, 1 m, or 2 m".into())
    }
}

fn validate_update(update: &IndoorExploreMissionUpdate) -> Result<(), String> {
    if update.event_id.trim().is_empty()
        || update.operation_id.trim().is_empty()
        || update.mission_id.trim().is_empty()
        || update.observed_at_unix_ms <= 0
    {
        return Err("Indoor Explore update identity and timestamp are required".into());
    }
    validate_altitude(update.altitude_m)
}

fn mission_state(state: IndoorExploreMissionState) -> Result<MissionState, String> {
    match state {
        IndoorExploreMissionState::Starting => Ok(MissionState::Starting),
        IndoorExploreMissionState::TakingOff => Ok(MissionState::TakingOff),
        IndoorExploreMissionState::Exploring => Ok(MissionState::Exploring),
        IndoorExploreMissionState::Returning => Ok(MissionState::Returning),
        IndoorExploreMissionState::Complete => Ok(MissionState::Complete),
        IndoorExploreMissionState::Holding => Ok(MissionState::Holding),
        IndoorExploreMissionState::Failed => Ok(MissionState::Failed),
        IndoorExploreMissionState::Unspecified => {
            Err("Indoor Explore update state is unspecified".into())
        }
    }
}

fn valid_transition(current: MissionState, next: MissionState) -> bool {
    current == next
        || matches!(
            (current, next),
            (MissionState::Starting, MissionState::TakingOff)
                | (MissionState::Starting, MissionState::Holding)
                | (MissionState::Starting, MissionState::Failed)
                | (MissionState::TakingOff, MissionState::Exploring)
                | (MissionState::TakingOff, MissionState::Returning)
                | (MissionState::TakingOff, MissionState::Holding)
                | (MissionState::TakingOff, MissionState::Failed)
                | (MissionState::Exploring, MissionState::Returning)
                | (MissionState::Exploring, MissionState::Complete)
                | (MissionState::Exploring, MissionState::Holding)
                | (MissionState::Exploring, MissionState::Failed)
                | (MissionState::Returning, MissionState::Complete)
                | (MissionState::Returning, MissionState::Holding)
                | (MissionState::Returning, MissionState::Failed)
                | (MissionState::Holding, MissionState::Returning)
                | (MissionState::Holding, MissionState::Complete)
                | (MissionState::Holding, MissionState::Failed)
        )
}

fn next_id(prefix: &str, now: i64) -> String {
    format!("{prefix}-{now}-{}", NEXT_ID.fetch_add(1, Ordering::Relaxed))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn update(
        control: &IndoorExploreControl,
        event_id: &str,
        state: IndoorExploreMissionState,
    ) -> IndoorExploreMissionUpdate {
        IndoorExploreMissionUpdate {
            event_id: event_id.into(),
            operation_id: control.operation_id.clone(),
            mission_id: control.mission_id.clone(),
            state: state as i32,
            altitude_m: control.altitude_m,
            observed_at_unix_ms: control.requested_at_unix_ms + 1,
            error_code: String::new(),
            message: state.as_str_name().into(),
        }
    }

    #[test]
    fn start_accepts_only_the_three_operator_altitudes() {
        for altitude in [0.5, 1.0, 2.0] {
            let store = IndoorExploreStore::default();
            assert!(store.prepare_start("drone-1", altitude, 1_000).is_ok());
        }
        let store = IndoorExploreStore::default();
        assert_eq!(
            store.prepare_start("drone-1", 1.5, 1_000).unwrap_err(),
            "Indoor Explore altitude must be exactly 0.5 m, 1 m, or 2 m"
        );
    }

    #[test]
    fn agent_updates_must_follow_the_explicit_state_machine() {
        let store = IndoorExploreStore::default();
        let control = store.prepare_start("drone-1", 1.0, 1_000).unwrap();
        store
            .apply_agent_update(
                "drone-1",
                update(
                    &control,
                    "event-starting",
                    IndoorExploreMissionState::Starting,
                ),
            )
            .unwrap();
        store
            .apply_agent_update(
                "drone-1",
                update(
                    &control,
                    "event-holding",
                    IndoorExploreMissionState::Holding,
                ),
            )
            .unwrap();
        let error = store
            .apply_agent_update(
                "drone-1",
                update(
                    &control,
                    "event-exploring",
                    IndoorExploreMissionState::Exploring,
                ),
            )
            .unwrap_err();
        assert!(error.contains("invalid Indoor Explore transition"));
        assert_eq!(store.snapshot("drone-1").unwrap().state, "holding");
    }

    #[test]
    fn restart_recovery_adopts_only_safe_agent_states() {
        let store = IndoorExploreStore::default();
        let control = IndoorExploreControl {
            operation_id: "operation-1".into(),
            mission_id: "mission-1".into(),
            drone_id: "drone-1".into(),
            action: IndoorExploreControlAction::Start,
            altitude_m: 0.5,
            requested_at_unix_ms: 1_000,
            deadline_at_unix_ms: 6_000,
            reason: String::new(),
        };
        assert!(store
            .apply_agent_update(
                "drone-1",
                update(
                    &control,
                    "event-exploring",
                    IndoorExploreMissionState::Exploring,
                ),
            )
            .is_err());
        store
            .apply_agent_update(
                "drone-1",
                update(
                    &control,
                    "event-holding",
                    IndoorExploreMissionState::Holding,
                ),
            )
            .unwrap();
        assert_eq!(store.snapshot("drone-1").unwrap().state, "holding");
    }

    #[test]
    fn abort_preserves_the_selected_altitude_and_mission_identity() {
        let store = IndoorExploreStore::default();
        let start = store.prepare_start("drone-1", 2.0, 1_000).unwrap();
        let abort = store
            .prepare_abort("drone-1", &start.mission_id, "operator abort", 2_000)
            .unwrap();
        assert_eq!(abort.action, IndoorExploreControlAction::AbortAndReturn);
        assert_eq!(abort.mission_id, start.mission_id);
        assert_eq!(abort.altitude_m, 2.0);
    }
}
