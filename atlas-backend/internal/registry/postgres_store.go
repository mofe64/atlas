package registry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/domain"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type storeDialect string

const (
	storeDialectPostgres storeDialect = "postgres"
	storeDialectSQLite   storeDialect = "sqlite"
)

type PostgresStore struct {
	db      *sql.DB
	dialect storeDialect
}

type SQLiteStore = PostgresStore

func OpenPostgresStore(ctx context.Context, dsn string) (*PostgresStore, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("postgres DSN is required")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres store: %w", err)
	}

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping postgres store: %w", err)
	}

	return &PostgresStore{db: db, dialect: storeDialectPostgres}, nil
}

func OpenSQLiteStore(ctx context.Context, path string) (*SQLiteStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("sqlite path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("prepare sqlite store directory: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite store: %w", err)
	}
	db.SetMaxOpenConns(1)

	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable sqlite foreign keys: %w", err)
	}
	if err := applySQLiteSchema(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite store: %w", err)
	}

	return &SQLiteStore{db: db, dialect: storeDialectSQLite}, nil
}

func (s *PostgresStore) Close() error {
	return s.db.Close()
}

func (s *PostgresStore) RegisterAgent(input RegisterAgentInput, now time.Time) domain.Agent {
	ctx := context.Background()
	agent := domain.Agent{}

	err := withTx(ctx, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO drones (id, name, last_seen_at)
			VALUES ($1, $2, $3)
			ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, last_seen_at = EXCLUDED.last_seen_at
		`, input.DroneID, input.DroneName, now); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agents (
			  id, drone_id, version, registered_at, command_channel_state
			) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (id) DO UPDATE SET
			  drone_id = EXCLUDED.drone_id,
			  version = EXCLUDED.version
		`, input.AgentID, input.DroneID, input.AgentVersion, now, string(domain.CommandChannelDisconnected)); err != nil {
			return err
		}

		var err error
		agent, err = scanAgent(tx.QueryRowContext(ctx, agentByIDSQL, input.AgentID))
		return err
	})
	if err != nil {
		return domain.Agent{}
	}

	return agent
}

func (s *PostgresStore) RecordHeartbeat(input HeartbeatInput, now time.Time) (domain.Agent, error) {
	var agent domain.Agent
	err := withTx(context.Background(), s.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(context.Background(), `
			UPDATE agents SET version = $2, last_heartbeat_at = $3 WHERE id = $1
		`, input.AgentID, input.AgentVersion, now)
		if err != nil {
			return err
		}
		if rows, _ := res.RowsAffected(); rows == 0 {
			return ErrAgentNotFound
		}

		agent, err = scanAgent(tx.QueryRowContext(context.Background(), agentByIDSQL, input.AgentID))
		if err != nil {
			return err
		}

		_, err = tx.ExecContext(context.Background(), `
			UPDATE drones SET last_seen_at = $2 WHERE id = $1
		`, agent.DroneID, now)
		return err
	})
	return agent, err
}

func (s *PostgresStore) ListDrones(now time.Time) []DroneSnapshot {
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, name, last_seen_at FROM drones ORDER BY id
	`)
	if err != nil {
		return nil
	}

	var drones []domain.Drone
	for rows.Next() {
		var drone domain.Drone
		if err := rows.Scan(&drone.ID, &drone.Name, &drone.LastSeenAt); err != nil {
			rows.Close()
			return nil
		}
		drones = append(drones, drone)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil
	}
	rows.Close()

	var snapshots []DroneSnapshot
	for _, drone := range drones {
		agent := s.agentForDrone(context.Background(), drone.ID)
		telemetry, _ := s.telemetryForDrone(context.Background(), drone.ID)
		snapshots = append(snapshots, DroneSnapshot{
			ID:                     drone.ID,
			Name:                   drone.Name,
			AgentID:                agent.ID,
			Status:                 domain.StatusFromHeartbeat(agent.LastHeartbeatAt, now),
			LastSeenAt:             drone.LastSeenAt,
			LastHeartbeatAt:        agent.LastHeartbeatAt,
			Telemetry:              telemetry,
			TelemetryState:         domain.TelemetryStateFromReceivedAt(telemetry.ReceivedAt, now),
			CommandChannel:         commandChannelSnapshot(agent),
			LatestMissionExecution: s.latestMissionExecutionForDrone(context.Background(), drone.ID),
		})
	}

	return snapshots
}

func (s *PostgresStore) RecordTelemetry(snapshot domain.TelemetrySnapshot, now time.Time) (domain.TelemetrySnapshot, error) {
	err := withTx(context.Background(), s.db, func(tx *sql.Tx) error {
		agent, err := scanAgent(tx.QueryRowContext(context.Background(), agentByIDSQL, snapshot.AgentID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrAgentNotFound
		}
		if err != nil {
			return err
		}

		snapshot.DroneID = agent.DroneID
		snapshot.ReceivedAt = now
		if _, err := tx.ExecContext(context.Background(), `
			INSERT INTO telemetry_latest (
			  drone_id, agent_id, observed_at, received_at, battery_percent, relative_altitude_m,
			  flight_mode, armed, in_air, latitude, longitude, heading_deg, ground_speed_mps,
			  gps_fix, satellites_visible, home_position_set, source
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
			ON CONFLICT (drone_id) DO UPDATE SET
			  agent_id = EXCLUDED.agent_id,
			  observed_at = EXCLUDED.observed_at,
			  received_at = EXCLUDED.received_at,
			  battery_percent = EXCLUDED.battery_percent,
			  relative_altitude_m = EXCLUDED.relative_altitude_m,
			  flight_mode = EXCLUDED.flight_mode,
			  armed = EXCLUDED.armed,
			  in_air = EXCLUDED.in_air,
			  latitude = EXCLUDED.latitude,
			  longitude = EXCLUDED.longitude,
			  heading_deg = EXCLUDED.heading_deg,
			  ground_speed_mps = EXCLUDED.ground_speed_mps,
			  gps_fix = EXCLUDED.gps_fix,
			  satellites_visible = EXCLUDED.satellites_visible,
			  home_position_set = EXCLUDED.home_position_set,
			  source = EXCLUDED.source
		`, snapshot.DroneID, snapshot.AgentID, snapshot.ObservedAt, snapshot.ReceivedAt, snapshot.BatteryPercent,
			snapshot.RelativeAltitudeM, snapshot.FlightMode, snapshot.Armed, snapshot.InAir, snapshot.Latitude,
			snapshot.Longitude, snapshot.HeadingDeg, snapshot.GroundSpeedMPS, snapshot.GPSFix,
			snapshot.SatellitesVisible, snapshot.HomePositionSet, snapshot.Source); err != nil {
			return err
		}

		if _, err := tx.ExecContext(context.Background(), `UPDATE drones SET last_seen_at = $2 WHERE id = $1`, snapshot.DroneID, now); err != nil {
			return err
		}

		return s.confirmCommandsFromTelemetryTx(context.Background(), tx, snapshot, now)
	})

	return snapshot, err
}

func (s *PostgresStore) RecordCommandChannelConnected(agentID string, now time.Time) (domain.Agent, error) {
	return s.updateCommandChannel(agentID, domain.CommandChannelConnected, now, true)
}

func (s *PostgresStore) RecordCommandChannelDisconnected(agentID string, now time.Time) (domain.Agent, error) {
	return s.updateCommandChannel(agentID, domain.CommandChannelDisconnected, now, false)
}

func (s *PostgresStore) RequestCommand(input RequestCommandInput, now time.Time) (domain.OperatorCommand, error) {
	var command domain.OperatorCommand
	err := withTx(context.Background(), s.db, func(tx *sql.Tx) error {
		if !rowExists(tx, `SELECT 1 FROM drones WHERE id = $1`, input.DroneID) {
			return ErrDroneNotFound
		}

		agent := s.agentForDroneTx(context.Background(), tx, input.DroneID)
		if agent.ID == "" {
			return ErrAgentNotFound
		}
		telemetry, _ := s.telemetryForDroneTx(context.Background(), tx, input.DroneID)
		agentStatus := domain.StatusFromHeartbeat(agent.LastHeartbeatAt, now)
		telemetryState := domain.TelemetryStateFromReceivedAt(telemetry.ReceivedAt, now)

		command = domain.OperatorCommand{
			ID:             s.nextIDTx(context.Background(), tx, "operator_command_seq", "cmd"),
			DroneID:        input.DroneID,
			AgentID:        agent.ID,
			Type:           input.Type,
			State:          domain.CommandStateAuthorized,
			RequestedBy:    input.RequestedBy,
			RequestedAt:    now,
			UpdatedAt:      now,
			TelemetryState: telemetryState,
			AgentStatus:    agentStatus,
		}
		if agentStatus != domain.AgentStatusOnline {
			command.State = domain.CommandStateRejectedByPolicy
			command.PolicyReason = "agent must be online"
		} else if telemetryState != domain.TelemetryStateFresh {
			command.State = domain.CommandStateRejectedByPolicy
			command.PolicyReason = "telemetry must be fresh"
		}

		if err := insertCommandTx(context.Background(), tx, command); err != nil {
			return err
		}
		return insertCommandEventTx(context.Background(), tx, s.nextIDTx(context.Background(), tx, "command_event_seq", "cev"), command, "requested", "backend", command.PolicyReason, now)
	})
	return command, err
}

func (s *PostgresStore) NextCommandForAgent(agentID string, now time.Time) (domain.OperatorCommand, bool, error) {
	var command domain.OperatorCommand
	ok := false
	err := withTx(context.Background(), s.db, func(tx *sql.Tx) error {
		if !rowExists(tx, `SELECT 1 FROM agents WHERE id = $1`, agentID) {
			return ErrAgentNotFound
		}

		row := tx.QueryRowContext(context.Background(), commandSelectSQL+`
			WHERE agent_id = $1
			  AND (state = $2 OR (state = $3 AND lease_until IS NOT NULL AND lease_until <= $4))
			ORDER BY requested_at, id
			LIMIT 1
		`+s.forUpdate(), agentID, string(domain.CommandStateAuthorized), string(domain.CommandStateSentToAgent), now)
		var err error
		command, err = scanCommand(row)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}

		command = markCommandSentToAgent(command, now)
		if err := updateCommandTx(context.Background(), tx, command); err != nil {
			return err
		}
		if err := insertCommandEventTx(context.Background(), tx, s.nextIDTx(context.Background(), tx, "command_event_seq", "cev"), command, "sent_to_agent", "backend", "command sent to agent", now); err != nil {
			return err
		}
		ok = true
		return nil
	})
	return command, ok, err
}

func (s *PostgresStore) ClaimCommandForAgent(agentID string, commandID string, now time.Time) (domain.OperatorCommand, error) {
	var command domain.OperatorCommand
	err := withTx(context.Background(), s.db, func(tx *sql.Tx) error {
		if !rowExists(tx, `SELECT 1 FROM agents WHERE id = $1`, agentID) {
			return ErrAgentNotFound
		}

		var err error
		command, err = scanCommand(tx.QueryRowContext(context.Background(), commandSelectSQL+`WHERE id = $1`+s.forUpdate(), commandID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCommandNotFound
		}
		if err != nil {
			return err
		}
		if command.AgentID != agentID {
			return ErrCommandNotAssigned
		}
		if !isCommandDeliverable(command, now) {
			return ErrInvalidCommandTransition
		}

		command = markCommandSentToAgent(command, now)
		if err := updateCommandTx(context.Background(), tx, command); err != nil {
			return err
		}
		return insertCommandEventTx(context.Background(), tx, s.nextIDTx(context.Background(), tx, "command_event_seq", "cev"), command, "sent_to_agent", "backend", "command sent to agent", now)
	})
	return command, err
}

func (s *PostgresStore) UpdateCommandStatus(input UpdateCommandStatusInput, now time.Time) (domain.OperatorCommand, error) {
	var command domain.OperatorCommand
	err := withTx(context.Background(), s.db, func(tx *sql.Tx) error {
		var err error
		command, err = scanCommand(tx.QueryRowContext(context.Background(), commandSelectSQL+`WHERE id = $1`+s.forUpdate(), input.CommandID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCommandNotFound
		}
		if err != nil {
			return err
		}
		if command.AgentID != input.AgentID {
			return ErrCommandNotAssigned
		}
		if !isAgentReportedCommandState(input.State) {
			return ErrInvalidCommandState
		}
		if !canApplyAgentReportedCommandState(command.State, input.State) {
			return ErrInvalidCommandTransition
		}

		command.State = input.State
		command.ResultMessage = input.ResultMessage
		command.UpdatedAt = now
		command.LeaseUntil = time.Time{}
		if input.State == domain.CommandStateVehicleAcked {
			command.VehicleAckedAt = now
			command.ConfirmationBaseline, _ = s.telemetryForDroneTx(context.Background(), tx, command.DroneID)
			if err := s.supersedeOlderAckedCommandsTx(context.Background(), tx, command, now); err != nil {
				return err
			}
		}

		if err := updateCommandTx(context.Background(), tx, command); err != nil {
			return err
		}
		return insertCommandEventTx(context.Background(), tx, s.nextIDTx(context.Background(), tx, "command_event_seq", "cev"), command, string(input.State), "agent", input.ResultMessage, now)
	})
	return command, err
}

func (s *PostgresStore) CommandByID(commandID string) (domain.OperatorCommand, bool) {
	command, err := scanCommand(s.db.QueryRowContext(context.Background(), commandSelectSQL+`WHERE id = $1`, commandID))
	return command, err == nil
}

func (s *PostgresStore) ListCommandsForDrone(droneID string, limit int) ([]domain.OperatorCommand, error) {
	if !rowExists(s.db, `SELECT 1 FROM drones WHERE id = $1`, droneID) {
		return nil, ErrDroneNotFound
	}

	query := commandSelectSQL + `WHERE drone_id = $1 ORDER BY requested_at DESC, id DESC`
	args := []any{droneID}
	if limit > 0 {
		query += ` LIMIT $2`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(context.Background(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCommands(rows)
}

func (s *PostgresStore) CreateMission(input CreateMissionInput, now time.Time) (domain.Mission, error) {
	var mission domain.Mission
	err := withTx(context.Background(), s.db, func(tx *sql.Tx) error {
		if !rowExists(tx, `SELECT 1 FROM drones WHERE id = $1`, input.DroneID) {
			return ErrDroneNotFound
		}

		mission = domain.Mission{
			ID:               s.nextIDTx(context.Background(), tx, "mission_seq", "msn"),
			DroneID:          input.DroneID,
			Name:             strings.TrimSpace(input.Name),
			CreatedBy:        input.CreatedBy,
			CreatedAt:        now,
			UpdatedAt:        now,
			CompletionAction: normalizeMissionCompletionAction(input.CompletionAction),
			ValidationStatus: domain.MissionValidationStatusValidated,
		}
		for i, waypoint := range input.Waypoints {
			mission.Waypoints = append(mission.Waypoints, domain.MissionWaypoint{
				Sequence:          i + 1,
				Latitude:          waypoint.Latitude,
				Longitude:         waypoint.Longitude,
				RelativeAltitudeM: waypoint.RelativeAltitudeM,
				SpeedMPS:          waypoint.SpeedMPS,
				LoiterTimeS:       waypoint.LoiterTimeS,
			})
		}

		mission.ValidationErrors = s.validateMissionTx(context.Background(), tx, mission, now)
		if len(mission.ValidationErrors) > 0 {
			mission.ValidationStatus = domain.MissionValidationStatusRejected
		}

		if err := insertMissionTx(context.Background(), tx, mission); err != nil {
			return err
		}
		for _, waypoint := range mission.Waypoints {
			if err := insertMissionWaypointTx(context.Background(), tx, mission.ID, waypoint); err != nil {
				return err
			}
		}
		return nil
	})
	return mission, err
}

func (s *PostgresStore) ListMissionsForDrone(droneID string) ([]domain.Mission, error) {
	if !rowExists(s.db, `SELECT 1 FROM drones WHERE id = $1`, droneID) {
		return nil, ErrDroneNotFound
	}
	rows, err := s.db.QueryContext(context.Background(), missionSelectSQL+`
		WHERE drone_id = $1
		ORDER BY created_at DESC, id DESC
	`, droneID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	missions, err := scanMissions(rows)
	if err != nil {
		return nil, err
	}
	for i := range missions {
		missions[i].Waypoints, err = s.listMissionWaypoints(context.Background(), missions[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return missions, nil
}

func (s *PostgresStore) MissionByID(missionID string) (domain.Mission, bool) {
	mission, err := scanMission(s.db.QueryRowContext(context.Background(), missionSelectSQL+`WHERE id = $1`, missionID))
	if err != nil {
		return domain.Mission{}, false
	}
	waypoints, err := s.listMissionWaypoints(context.Background(), missionID)
	if err != nil {
		return domain.Mission{}, false
	}
	mission.Waypoints = waypoints
	return mission, true
}

func (s *PostgresStore) RequestMissionUpload(input RequestMissionUploadInput, now time.Time) (domain.MissionExecution, error) {
	var execution domain.MissionExecution
	err := withTx(context.Background(), s.db, func(tx *sql.Tx) error {
		mission, err := s.missionByIDTx(context.Background(), tx, input.MissionID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrMissionNotFound
		}
		if err != nil {
			return err
		}
		if mission.ValidationStatus != domain.MissionValidationStatusValidated {
			return ErrMissionNotValidated
		}

		agent := s.agentForDroneTx(context.Background(), tx, mission.DroneID)
		if agent.ID == "" {
			return ErrAgentNotFound
		}
		active := s.operationalMissionExecutionForDroneTx(context.Background(), tx, mission.DroneID, "")
		if active.ID != "" {
			return ErrDroneMissionActive
		}

		execution = domain.MissionExecution{
			ID:                s.nextIDTx(context.Background(), tx, "mission_execution_seq", "mex"),
			MissionID:         mission.ID,
			DroneID:           mission.DroneID,
			AgentID:           agent.ID,
			RequestedBy:       input.RequestedBy,
			UploadRequestedBy: input.RequestedBy,
			State:             domain.MissionExecutionStateUploadRequested,
			CreatedAt:         now,
			UpdatedAt:         now,
			UploadRequestedAt: now,
		}
		if err := insertMissionExecutionTx(context.Background(), tx, execution); err != nil {
			return err
		}
		return insertMissionExecutionEventTx(context.Background(), tx, s.nextIDTx(context.Background(), tx, "mission_execution_event_seq", "mev"), execution, "upload_requested", "backend", "mission upload requested", now)
	})
	return execution, err
}

func (s *PostgresStore) RecordMissionExecutionUploaded(executionID string, resultMessage string, now time.Time) (domain.MissionExecution, error) {
	var execution domain.MissionExecution
	err := withTx(context.Background(), s.db, func(tx *sql.Tx) error {
		var err error
		execution, err = s.missionExecutionByIDTx(context.Background(), tx, executionID, true)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrMissionExecutionNotFound
		}
		if err != nil {
			return err
		}
		if execution.State != domain.MissionExecutionStateUploadRequested &&
			execution.State != domain.MissionExecutionStateUploading {
			return ErrInvalidMissionExecutionState
		}
		execution.State = domain.MissionExecutionStateUploadedToVehicle
		execution.UpdatedAt = now
		execution.UploadedAt = now
		execution.LeaseUntil = time.Time{}
		execution.ResultMessage = resultMessage
		if err := updateMissionExecutionTx(context.Background(), tx, execution); err != nil {
			return err
		}
		return insertMissionExecutionEventTx(context.Background(), tx, s.nextIDTx(context.Background(), tx, "mission_execution_event_seq", "mev"), execution, "uploaded_to_vehicle", "backend", resultMessage, now)
	})
	return execution, err
}

func (s *PostgresStore) RequestMissionStart(input RequestMissionStartInput, now time.Time) (domain.MissionExecution, error) {
	var execution domain.MissionExecution
	err := withTx(context.Background(), s.db, func(tx *sql.Tx) error {
		mission, err := s.missionByIDTx(context.Background(), tx, input.MissionID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrMissionNotFound
		}
		if err != nil {
			return err
		}

		execution = s.latestMissionExecutionInStateTx(context.Background(), tx, input.MissionID, domain.MissionExecutionStateUploadedToVehicle)
		if execution.ID == "" {
			return ErrInvalidMissionExecutionState
		}
		active := s.operationalMissionExecutionForDroneTx(context.Background(), tx, mission.DroneID, execution.ID)
		if active.ID != "" {
			return ErrDroneMissionActive
		}
		if err := s.validateMissionStartPreconditionsTx(context.Background(), tx, mission, now); err != nil {
			return err
		}

		execution.State = domain.MissionExecutionStateStartRequested
		execution.RequestedBy = input.RequestedBy
		execution.StartRequestedBy = input.RequestedBy
		execution.UpdatedAt = now
		execution.StartRequestedAt = now
		if err := updateMissionExecutionTx(context.Background(), tx, execution); err != nil {
			return err
		}
		return insertMissionExecutionEventTx(context.Background(), tx, s.nextIDTx(context.Background(), tx, "mission_execution_event_seq", "mev"), execution, "start_requested", "backend", "mission start requested", now)
	})
	return execution, err
}

func (s *PostgresStore) RequestMissionAbort(input RequestMissionAbortInput, now time.Time) (domain.MissionExecution, error) {
	var execution domain.MissionExecution
	err := withTx(context.Background(), s.db, func(tx *sql.Tx) error {
		if !rowExists(tx, `SELECT 1 FROM missions WHERE id = $1`, input.MissionID) {
			return ErrMissionNotFound
		}
		execution = s.abortableMissionExecutionForMissionTx(context.Background(), tx, input.MissionID)
		if execution.ID == "" {
			return ErrInvalidMissionExecutionState
		}
		execution.State = domain.MissionExecutionStateRTLRequested
		execution.RequestedBy = input.RequestedBy
		execution.UpdatedAt = now
		execution.LeaseUntil = time.Time{}
		execution.ResultMessage = "abort requested; returning to launch"
		if err := updateMissionExecutionTx(context.Background(), tx, execution); err != nil {
			return err
		}
		return insertMissionExecutionEventTx(context.Background(), tx, s.nextIDTx(context.Background(), tx, "mission_execution_event_seq", "mev"), execution, "rtl_requested", "backend", execution.ResultMessage, now)
	})
	return execution, err
}

func (s *PostgresStore) ListMissionExecutions(missionID string) ([]domain.MissionExecution, error) {
	if !rowExists(s.db, `SELECT 1 FROM missions WHERE id = $1`, missionID) {
		return nil, ErrMissionNotFound
	}
	rows, err := s.db.QueryContext(context.Background(), missionExecutionSelectSQL+`
		WHERE mission_id = $1
		ORDER BY created_at DESC, id DESC
	`, missionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMissionExecutions(rows)
}

func (s *PostgresStore) ListMissionExecutionEvents(missionID string) ([]domain.MissionExecutionEvent, error) {
	if !rowExists(s.db, `SELECT 1 FROM missions WHERE id = $1`, missionID) {
		return nil, ErrMissionNotFound
	}
	rows, err := s.db.QueryContext(context.Background(), missionExecutionEventSelectSQL+`
		WHERE mission_id = $1
		ORDER BY created_at, id
	`, missionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMissionExecutionEvents(rows)
}

func (s *PostgresStore) NextMissionExecutionForAgent(agentID string, now time.Time) (domain.MissionExecution, bool, error) {
	var execution domain.MissionExecution
	ok := false
	err := withTx(context.Background(), s.db, func(tx *sql.Tx) error {
		if !rowExists(tx, `SELECT 1 FROM agents WHERE id = $1`, agentID) {
			return ErrAgentNotFound
		}
		rows, err := tx.QueryContext(context.Background(), missionExecutionSelectSQL+`
			WHERE agent_id = $1
			ORDER BY updated_at, id
		`+s.forUpdate(), agentID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			candidate, err := scanMissionExecutionFromRows(rows)
			if err != nil {
				return err
			}
			if isMissionExecutionDeliverable(candidate, now) {
				execution = candidate
				break
			}
		}
		if execution.ID == "" {
			return nil
		}
		execution = markMissionExecutionSent(execution, now)
		if err := updateMissionExecutionTx(context.Background(), tx, execution); err != nil {
			return err
		}
		if err := insertMissionExecutionEventTx(context.Background(), tx, s.nextIDTx(context.Background(), tx, "mission_execution_event_seq", "mev"), execution, "sent_to_agent", "backend", "mission execution sent to agent", now); err != nil {
			return err
		}
		ok = true
		return nil
	})
	return execution, ok, err
}

func (s *PostgresStore) ClaimMissionExecutionForAgent(agentID string, executionID string, now time.Time) (domain.MissionExecution, error) {
	var execution domain.MissionExecution
	err := withTx(context.Background(), s.db, func(tx *sql.Tx) error {
		if !rowExists(tx, `SELECT 1 FROM agents WHERE id = $1`, agentID) {
			return ErrAgentNotFound
		}
		var err error
		execution, err = s.missionExecutionByIDTx(context.Background(), tx, executionID, true)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrMissionExecutionNotFound
		}
		if err != nil {
			return err
		}
		if execution.AgentID != agentID {
			return ErrMissionExecutionNotAssigned
		}
		if !isMissionExecutionDeliverable(execution, now) {
			return ErrInvalidMissionExecutionState
		}
		execution = markMissionExecutionSent(execution, now)
		if err := updateMissionExecutionTx(context.Background(), tx, execution); err != nil {
			return err
		}
		return insertMissionExecutionEventTx(context.Background(), tx, s.nextIDTx(context.Background(), tx, "mission_execution_event_seq", "mev"), execution, "sent_to_agent", "backend", "mission execution sent to agent", now)
	})
	return execution, err
}

func (s *PostgresStore) UpdateMissionExecutionStatus(input UpdateMissionExecutionStatusInput, now time.Time) (domain.MissionExecution, error) {
	var execution domain.MissionExecution
	err := withTx(context.Background(), s.db, func(tx *sql.Tx) error {
		var err error
		execution, err = s.missionExecutionByIDTx(context.Background(), tx, input.ExecutionID, true)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrMissionExecutionNotFound
		}
		if err != nil {
			return err
		}
		if execution.AgentID != input.AgentID {
			return ErrMissionExecutionNotAssigned
		}
		if !canApplyAgentReportedMissionExecutionState(execution.State, input.State) {
			return ErrInvalidMissionExecutionState
		}
		execution.State = input.State
		execution.ResultMessage = input.ResultMessage
		execution.UpdatedAt = now
		execution.LeaseUntil = time.Time{}
		if input.CurrentMissionItem > 0 || input.TotalMissionItems > 0 {
			execution.CurrentMissionItem = input.CurrentMissionItem
			execution.TotalMissionItems = input.TotalMissionItems
			execution.ProgressUpdatedAt = now
		}
		switch input.State {
		case domain.MissionExecutionStateUploadedToVehicle:
			execution.UploadedAt = now
		case domain.MissionExecutionStateActive:
			if execution.StartedAt.IsZero() {
				execution.StartedAt = now
			}
		case domain.MissionExecutionStateCompleted:
			if execution.CompletedAt.IsZero() {
				execution.CompletedAt = now
			}
		case domain.MissionExecutionStateHold:
			if execution.CompletedAt.IsZero() {
				execution.CompletedAt = now
			}
			if execution.HoldAt.IsZero() {
				execution.HoldAt = now
			}
		case domain.MissionExecutionStateUploadFailed,
			domain.MissionExecutionStateFailed,
			domain.MissionExecutionStateAborted:
			execution.FailedAt = now
		}
		if err := updateMissionExecutionTx(context.Background(), tx, execution); err != nil {
			return err
		}
		return insertMissionExecutionEventTx(context.Background(), tx, s.nextIDTx(context.Background(), tx, "mission_execution_event_seq", "mev"), execution, missionExecutionEventType(input), "agent", input.ResultMessage, now)
	})
	return execution, err
}

const agentByIDSQL = `
	SELECT id, drone_id, version, registered_at, last_heartbeat_at,
	       command_channel_state, command_channel_connected_at, command_channel_last_disconnected_at
	FROM agents WHERE id = $1
`

const commandSelectSQL = `
	SELECT id, drone_id, agent_id, type, state, requested_by, requested_at, updated_at,
	       last_sent_at, lease_until, vehicle_acked_at, confirmation_baseline,
	       delivery_attempt, policy_reason, result_message, telemetry_state, agent_status
	FROM operator_commands
`

const missionSelectSQL = `
	SELECT id, drone_id, name, created_by, created_at, updated_at,
	       completion_action, validation_status, validation_errors
	FROM missions
`

const missionExecutionSelectSQL = `
	SELECT id, mission_id, drone_id, agent_id, requested_by, upload_requested_by,
	       start_requested_by, state, created_at, updated_at, last_sent_at, lease_until,
	       upload_requested_at, uploaded_at, start_requested_at, started_at, completed_at,
	       hold_at, failed_at, current_mission_item, total_mission_items, progress_updated_at,
	       delivery_attempt, result_message
	FROM mission_executions
`

const missionExecutionEventSelectSQL = `
	SELECT id, execution_id, mission_id, drone_id, agent_id, event_type, state, message,
	       current_mission_item, total_mission_items, source, created_at
	FROM mission_execution_events
`

type rowScanner interface {
	Scan(dest ...any) error
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type queryExecer interface {
	queryer
	execer
}

func withTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := fn(tx); err != nil {
		return err
	}

	return tx.Commit()
}

func rowExists(q queryer, query string, args ...any) bool {
	var one int
	return q.QueryRowContext(context.Background(), query, args...).Scan(&one) == nil
}

func (s *PostgresStore) nextIDTx(ctx context.Context, tx *sql.Tx, sequence string, prefix string) string {
	var value int64
	if s.dialect == storeDialectSQLite {
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO atlas_sequences (name, value) VALUES ($1, 0)
		`, sequence); err != nil {
			panic(err)
		}
		if err := tx.QueryRowContext(ctx, `
			UPDATE atlas_sequences SET value = value + 1 WHERE name = $1 RETURNING value
		`, sequence).Scan(&value); err != nil {
			panic(err)
		}
	} else {
		query := fmt.Sprintf("SELECT nextval('%s')", sequence)
		if err := tx.QueryRowContext(ctx, query).Scan(&value); err != nil {
			panic(err)
		}
	}
	return fmt.Sprintf("%s-%06d", prefix, value)
}

func (s *PostgresStore) forUpdate() string {
	if s.dialect == storeDialectPostgres {
		return " FOR UPDATE"
	}
	return ""
}

func scanAgent(row rowScanner) (domain.Agent, error) {
	var agent domain.Agent
	var lastHeartbeatAt, connectedAt, disconnectedAt sql.NullTime
	var channelState string
	err := row.Scan(
		&agent.ID,
		&agent.DroneID,
		&agent.Version,
		&agent.RegisteredAt,
		&lastHeartbeatAt,
		&channelState,
		&connectedAt,
		&disconnectedAt,
	)
	if err != nil {
		return domain.Agent{}, err
	}
	agent.LastHeartbeatAt = timeFromNull(lastHeartbeatAt)
	agent.CommandChannelState = domain.CommandChannelState(channelState)
	agent.CommandChannelConnectedAt = timeFromNull(connectedAt)
	agent.CommandChannelLastDisconnectedAt = timeFromNull(disconnectedAt)
	return agent, nil
}

func (s *PostgresStore) updateCommandChannel(agentID string, state domain.CommandChannelState, now time.Time, connected bool) (domain.Agent, error) {
	var agent domain.Agent
	err := withTx(context.Background(), s.db, func(tx *sql.Tx) error {
		var res sql.Result
		var err error
		if connected {
			res, err = tx.ExecContext(context.Background(), `
				UPDATE agents SET command_channel_state = $2, command_channel_connected_at = $3 WHERE id = $1
			`, agentID, string(state), now)
		} else {
			res, err = tx.ExecContext(context.Background(), `
				UPDATE agents SET command_channel_state = $2, command_channel_last_disconnected_at = $3 WHERE id = $1
			`, agentID, string(state), now)
		}
		if err != nil {
			return err
		}
		if rows, _ := res.RowsAffected(); rows == 0 {
			return ErrAgentNotFound
		}
		agent, err = scanAgent(tx.QueryRowContext(context.Background(), agentByIDSQL, agentID))
		return err
	})
	return agent, err
}

func (s *PostgresStore) agentForDrone(ctx context.Context, droneID string) domain.Agent {
	return s.agentForDroneWithQueryer(ctx, s.db, droneID)
}

func (s *PostgresStore) agentForDroneTx(ctx context.Context, tx *sql.Tx, droneID string) domain.Agent {
	return s.agentForDroneWithQueryer(ctx, tx, droneID)
}

func (s *PostgresStore) agentForDroneWithQueryer(ctx context.Context, q queryer, droneID string) domain.Agent {
	agent, err := scanAgent(q.QueryRowContext(ctx, `
		SELECT id, drone_id, version, registered_at, last_heartbeat_at,
		       command_channel_state, command_channel_connected_at, command_channel_last_disconnected_at
		FROM agents
		WHERE drone_id = $1
		ORDER BY registered_at DESC, id DESC
		LIMIT 1
	`, droneID))
	if err != nil {
		return domain.Agent{}
	}
	return agent
}

func (s *PostgresStore) telemetryForDrone(ctx context.Context, droneID string) (domain.TelemetrySnapshot, bool) {
	return s.telemetryForDroneWithQueryer(ctx, s.db, droneID)
}

func (s *PostgresStore) telemetryForDroneTx(ctx context.Context, tx *sql.Tx, droneID string) (domain.TelemetrySnapshot, bool) {
	return s.telemetryForDroneWithQueryer(ctx, tx, droneID)
}

func (s *PostgresStore) telemetryForDroneWithQueryer(ctx context.Context, q queryer, droneID string) (domain.TelemetrySnapshot, bool) {
	var snapshot domain.TelemetrySnapshot
	err := q.QueryRowContext(ctx, `
		SELECT drone_id, agent_id, observed_at, received_at, battery_percent, relative_altitude_m,
		       flight_mode, armed, in_air, latitude, longitude, heading_deg, ground_speed_mps,
		       gps_fix, satellites_visible, home_position_set, source
		FROM telemetry_latest WHERE drone_id = $1
	`, droneID).Scan(
		&snapshot.DroneID,
		&snapshot.AgentID,
		&snapshot.ObservedAt,
		&snapshot.ReceivedAt,
		&snapshot.BatteryPercent,
		&snapshot.RelativeAltitudeM,
		&snapshot.FlightMode,
		&snapshot.Armed,
		&snapshot.InAir,
		&snapshot.Latitude,
		&snapshot.Longitude,
		&snapshot.HeadingDeg,
		&snapshot.GroundSpeedMPS,
		&snapshot.GPSFix,
		&snapshot.SatellitesVisible,
		&snapshot.HomePositionSet,
		&snapshot.Source,
	)
	return snapshot, err == nil
}

func scanCommand(row rowScanner) (domain.OperatorCommand, error) {
	var command domain.OperatorCommand
	var commandType, state, telemetryState, agentStatus string
	var lastSentAt, leaseUntil, vehicleAckedAt sql.NullTime
	var baselineRaw []byte
	err := row.Scan(
		&command.ID,
		&command.DroneID,
		&command.AgentID,
		&commandType,
		&state,
		&command.RequestedBy,
		&command.RequestedAt,
		&command.UpdatedAt,
		&lastSentAt,
		&leaseUntil,
		&vehicleAckedAt,
		&baselineRaw,
		&command.DeliveryAttempt,
		&command.PolicyReason,
		&command.ResultMessage,
		&telemetryState,
		&agentStatus,
	)
	if err != nil {
		return domain.OperatorCommand{}, err
	}
	command.Type = domain.CommandType(commandType)
	command.State = domain.CommandState(state)
	command.LastSentAt = timeFromNull(lastSentAt)
	command.LeaseUntil = timeFromNull(leaseUntil)
	command.VehicleAckedAt = timeFromNull(vehicleAckedAt)
	command.TelemetryState = domain.TelemetryState(telemetryState)
	command.AgentStatus = domain.AgentStatus(agentStatus)
	if len(baselineRaw) > 0 {
		_ = json.Unmarshal(baselineRaw, &command.ConfirmationBaseline)
	}
	return command, nil
}

func scanCommands(rows *sql.Rows) ([]domain.OperatorCommand, error) {
	var commands []domain.OperatorCommand
	for rows.Next() {
		command, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		commands = append(commands, command)
	}
	return commands, rows.Err()
}

func insertCommandTx(ctx context.Context, tx *sql.Tx, command domain.OperatorCommand) error {
	return execCommandTx(ctx, tx, command, `
		INSERT INTO operator_commands (
		  id, drone_id, agent_id, type, state, requested_by, requested_at, updated_at,
		  last_sent_at, lease_until, vehicle_acked_at, confirmation_baseline, delivery_attempt,
		  policy_reason, result_message, telemetry_state, agent_status
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
	`)
}

func updateCommandTx(ctx context.Context, tx *sql.Tx, command domain.OperatorCommand) error {
	return execCommandTx(ctx, tx, command, `
		UPDATE operator_commands SET
		  drone_id = $2, agent_id = $3, type = $4, state = $5, requested_by = $6,
		  requested_at = $7, updated_at = $8, last_sent_at = $9, lease_until = $10,
		  vehicle_acked_at = $11, confirmation_baseline = $12, delivery_attempt = $13,
		  policy_reason = $14, result_message = $15, telemetry_state = $16, agent_status = $17
		WHERE id = $1
	`)
}

func execCommandTx(ctx context.Context, tx *sql.Tx, command domain.OperatorCommand, query string) error {
	baseline, err := json.Marshal(command.ConfirmationBaseline)
	if err != nil {
		return err
	}
	if command.ConfirmationBaseline.ReceivedAt.IsZero() && command.ConfirmationBaseline.ObservedAt.IsZero() {
		baseline = nil
	}
	_, err = tx.ExecContext(ctx, query,
		command.ID,
		command.DroneID,
		command.AgentID,
		string(command.Type),
		string(command.State),
		command.RequestedBy,
		command.RequestedAt,
		command.UpdatedAt,
		nullTime(command.LastSentAt),
		nullTime(command.LeaseUntil),
		nullTime(command.VehicleAckedAt),
		baseline,
		command.DeliveryAttempt,
		command.PolicyReason,
		command.ResultMessage,
		string(command.TelemetryState),
		string(command.AgentStatus),
	)
	return err
}

func insertCommandEventTx(ctx context.Context, tx *sql.Tx, eventID string, command domain.OperatorCommand, eventType string, source string, message string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO command_events (id, command_id, drone_id, agent_id, event_type, state, message, source, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, eventID, command.ID, command.DroneID, command.AgentID, eventType, string(command.State), message, source, now)
	return err
}

func (s *PostgresStore) confirmCommandsFromTelemetryTx(ctx context.Context, tx *sql.Tx, snapshot domain.TelemetrySnapshot, now time.Time) error {
	rows, err := tx.QueryContext(ctx, commandSelectSQL+`
		WHERE drone_id = $1 AND state = $2
	`+s.forUpdate(), snapshot.DroneID, string(domain.CommandStateVehicleAcked))
	if err != nil {
		return err
	}
	defer rows.Close()

	var commands []domain.OperatorCommand
	for rows.Next() {
		command, err := scanCommand(rows)
		if err != nil {
			return err
		}
		commands = append(commands, command)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, command := range commands {
		if !telemetryConfirmsCommand(command, snapshot) {
			continue
		}
		command.State = domain.CommandStateTelemetryConfirmed
		command.UpdatedAt = now
		command.ResultMessage = "confirmed by telemetry"
		if err := updateCommandTx(ctx, tx, command); err != nil {
			return err
		}
		if err := insertCommandEventTx(ctx, tx, s.nextIDTx(ctx, tx, "command_event_seq", "cev"), command, string(command.State), "backend", command.ResultMessage, now); err != nil {
			return err
		}
	}

	return nil
}

func (s *PostgresStore) supersedeOlderAckedCommandsTx(ctx context.Context, tx *sql.Tx, newer domain.OperatorCommand, now time.Time) error {
	rows, err := tx.QueryContext(ctx, commandSelectSQL+`
		WHERE id <> $1 AND drone_id = $2 AND type = $3 AND state = $4 AND requested_at < $5
	`+s.forUpdate(), newer.ID, newer.DroneID, string(newer.Type), string(domain.CommandStateVehicleAcked), newer.RequestedAt)
	if err != nil {
		return err
	}
	defer rows.Close()

	var commands []domain.OperatorCommand
	for rows.Next() {
		command, err := scanCommand(rows)
		if err != nil {
			return err
		}
		commands = append(commands, command)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, command := range commands {
		command.State = domain.CommandStateFailed
		command.UpdatedAt = now
		command.ResultMessage = fmt.Sprintf("superseded by newer %s command", newer.Type)
		if err := updateCommandTx(ctx, tx, command); err != nil {
			return err
		}
		if err := insertCommandEventTx(ctx, tx, s.nextIDTx(ctx, tx, "command_event_seq", "cev"), command, string(command.State), "backend", command.ResultMessage, now); err != nil {
			return err
		}
	}
	return nil
}

func insertMissionTx(ctx context.Context, tx *sql.Tx, mission domain.Mission) error {
	rawErrors, err := json.Marshal(mission.ValidationErrors)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO missions (
		  id, drone_id, name, created_by, created_at, updated_at,
		  completion_action, validation_status, validation_errors
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, mission.ID, mission.DroneID, mission.Name, mission.CreatedBy, mission.CreatedAt, mission.UpdatedAt,
		string(mission.CompletionAction), string(mission.ValidationStatus), rawErrors)
	return err
}

func insertMissionWaypointTx(ctx context.Context, tx *sql.Tx, missionID string, waypoint domain.MissionWaypoint) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO mission_waypoints (
		  mission_id, sequence, latitude, longitude, relative_altitude_m, speed_mps, loiter_time_s
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, missionID, waypoint.Sequence, waypoint.Latitude, waypoint.Longitude, waypoint.RelativeAltitudeM,
		floatPtrValue(waypoint.SpeedMPS), floatPtrValue(waypoint.LoiterTimeS))
	return err
}

func scanMission(row rowScanner) (domain.Mission, error) {
	var mission domain.Mission
	var completionAction, validationStatus string
	var rawErrors []byte
	err := row.Scan(
		&mission.ID,
		&mission.DroneID,
		&mission.Name,
		&mission.CreatedBy,
		&mission.CreatedAt,
		&mission.UpdatedAt,
		&completionAction,
		&validationStatus,
		&rawErrors,
	)
	if err != nil {
		return domain.Mission{}, err
	}
	mission.CompletionAction = domain.MissionCompletionAction(completionAction)
	mission.ValidationStatus = domain.MissionValidationStatus(validationStatus)
	if len(rawErrors) > 0 {
		_ = json.Unmarshal(rawErrors, &mission.ValidationErrors)
	}
	return mission, nil
}

func scanMissions(rows *sql.Rows) ([]domain.Mission, error) {
	var missions []domain.Mission
	for rows.Next() {
		mission, err := scanMission(rows)
		if err != nil {
			return nil, err
		}
		missions = append(missions, mission)
	}
	return missions, rows.Err()
}

func (s *PostgresStore) missionByIDTx(ctx context.Context, tx *sql.Tx, missionID string) (domain.Mission, error) {
	mission, err := scanMission(tx.QueryRowContext(ctx, missionSelectSQL+`WHERE id = $1`, missionID))
	if err != nil {
		return domain.Mission{}, err
	}
	mission.Waypoints, err = s.listMissionWaypointsTx(ctx, tx, missionID)
	return mission, err
}

func (s *PostgresStore) listMissionWaypoints(ctx context.Context, missionID string) ([]domain.MissionWaypoint, error) {
	return s.listMissionWaypointsWithQueryer(ctx, s.db, missionID)
}

func (s *PostgresStore) listMissionWaypointsTx(ctx context.Context, tx *sql.Tx, missionID string) ([]domain.MissionWaypoint, error) {
	return s.listMissionWaypointsWithQueryer(ctx, tx, missionID)
}

func (s *PostgresStore) listMissionWaypointsWithQueryer(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, missionID string) ([]domain.MissionWaypoint, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT sequence, latitude, longitude, relative_altitude_m, speed_mps, loiter_time_s
		FROM mission_waypoints
		WHERE mission_id = $1
		ORDER BY sequence
	`, missionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var waypoints []domain.MissionWaypoint
	for rows.Next() {
		var waypoint domain.MissionWaypoint
		var speed, loiter sql.NullFloat64
		if err := rows.Scan(
			&waypoint.Sequence,
			&waypoint.Latitude,
			&waypoint.Longitude,
			&waypoint.RelativeAltitudeM,
			&speed,
			&loiter,
		); err != nil {
			return nil, err
		}
		if speed.Valid {
			waypoint.SpeedMPS = &speed.Float64
		}
		if loiter.Valid {
			waypoint.LoiterTimeS = &loiter.Float64
		}
		waypoints = append(waypoints, waypoint)
	}
	return waypoints, rows.Err()
}

func insertMissionExecutionTx(ctx context.Context, tx *sql.Tx, execution domain.MissionExecution) error {
	return execMissionExecutionTx(ctx, tx, execution, `
		INSERT INTO mission_executions (
		  id, mission_id, drone_id, agent_id, requested_by, upload_requested_by, start_requested_by,
		  state, created_at, updated_at, last_sent_at, lease_until, upload_requested_at, uploaded_at,
		  start_requested_at, started_at, completed_at, hold_at, failed_at, current_mission_item,
		  total_mission_items, progress_updated_at, delivery_attempt, result_message
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24)
	`)
}

func updateMissionExecutionTx(ctx context.Context, tx *sql.Tx, execution domain.MissionExecution) error {
	return execMissionExecutionTx(ctx, tx, execution, `
		UPDATE mission_executions SET
		  mission_id = $2, drone_id = $3, agent_id = $4, requested_by = $5,
		  upload_requested_by = $6, start_requested_by = $7, state = $8,
		  created_at = $9, updated_at = $10, last_sent_at = $11, lease_until = $12,
		  upload_requested_at = $13, uploaded_at = $14, start_requested_at = $15,
		  started_at = $16, completed_at = $17, hold_at = $18, failed_at = $19,
		  current_mission_item = $20, total_mission_items = $21, progress_updated_at = $22,
		  delivery_attempt = $23, result_message = $24
		WHERE id = $1
	`)
}

func execMissionExecutionTx(ctx context.Context, tx *sql.Tx, execution domain.MissionExecution, query string) error {
	_, err := tx.ExecContext(ctx, query,
		execution.ID,
		execution.MissionID,
		execution.DroneID,
		execution.AgentID,
		execution.RequestedBy,
		execution.UploadRequestedBy,
		execution.StartRequestedBy,
		string(execution.State),
		execution.CreatedAt,
		execution.UpdatedAt,
		nullTime(execution.LastSentAt),
		nullTime(execution.LeaseUntil),
		nullTime(execution.UploadRequestedAt),
		nullTime(execution.UploadedAt),
		nullTime(execution.StartRequestedAt),
		nullTime(execution.StartedAt),
		nullTime(execution.CompletedAt),
		nullTime(execution.HoldAt),
		nullTime(execution.FailedAt),
		execution.CurrentMissionItem,
		execution.TotalMissionItems,
		nullTime(execution.ProgressUpdatedAt),
		execution.DeliveryAttempt,
		execution.ResultMessage,
	)
	return err
}

func (s *PostgresStore) missionExecutionByIDTx(ctx context.Context, tx *sql.Tx, executionID string, forUpdate bool) (domain.MissionExecution, error) {
	query := missionExecutionSelectSQL + `WHERE id = $1`
	if forUpdate {
		query += s.forUpdate()
	}
	return scanMissionExecution(tx.QueryRowContext(ctx, query, executionID))
}

func scanMissionExecution(row rowScanner) (domain.MissionExecution, error) {
	var execution domain.MissionExecution
	var state string
	var lastSentAt, leaseUntil, uploadRequestedAt, uploadedAt, startRequestedAt, startedAt, completedAt, holdAt, failedAt, progressUpdatedAt sql.NullTime
	err := row.Scan(
		&execution.ID,
		&execution.MissionID,
		&execution.DroneID,
		&execution.AgentID,
		&execution.RequestedBy,
		&execution.UploadRequestedBy,
		&execution.StartRequestedBy,
		&state,
		&execution.CreatedAt,
		&execution.UpdatedAt,
		&lastSentAt,
		&leaseUntil,
		&uploadRequestedAt,
		&uploadedAt,
		&startRequestedAt,
		&startedAt,
		&completedAt,
		&holdAt,
		&failedAt,
		&execution.CurrentMissionItem,
		&execution.TotalMissionItems,
		&progressUpdatedAt,
		&execution.DeliveryAttempt,
		&execution.ResultMessage,
	)
	if err != nil {
		return domain.MissionExecution{}, err
	}
	execution.State = domain.MissionExecutionState(state)
	execution.LastSentAt = timeFromNull(lastSentAt)
	execution.LeaseUntil = timeFromNull(leaseUntil)
	execution.UploadRequestedAt = timeFromNull(uploadRequestedAt)
	execution.UploadedAt = timeFromNull(uploadedAt)
	execution.StartRequestedAt = timeFromNull(startRequestedAt)
	execution.StartedAt = timeFromNull(startedAt)
	execution.CompletedAt = timeFromNull(completedAt)
	execution.HoldAt = timeFromNull(holdAt)
	execution.FailedAt = timeFromNull(failedAt)
	execution.ProgressUpdatedAt = timeFromNull(progressUpdatedAt)
	return execution, nil
}

func scanMissionExecutionFromRows(rows *sql.Rows) (domain.MissionExecution, error) {
	return scanMissionExecution(rows)
}

func scanMissionExecutions(rows *sql.Rows) ([]domain.MissionExecution, error) {
	var executions []domain.MissionExecution
	for rows.Next() {
		execution, err := scanMissionExecution(rows)
		if err != nil {
			return nil, err
		}
		executions = append(executions, execution)
	}
	return executions, rows.Err()
}

func insertMissionExecutionEventTx(ctx context.Context, tx *sql.Tx, eventID string, execution domain.MissionExecution, eventType string, source string, message string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO mission_execution_events (
		  id, execution_id, mission_id, drone_id, agent_id, event_type, state, message,
		  current_mission_item, total_mission_items, source, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`, eventID, execution.ID, execution.MissionID, execution.DroneID, execution.AgentID, eventType,
		string(execution.State), message, execution.CurrentMissionItem, execution.TotalMissionItems, source, now)
	return err
}

func scanMissionExecutionEvents(rows *sql.Rows) ([]domain.MissionExecutionEvent, error) {
	var events []domain.MissionExecutionEvent
	for rows.Next() {
		var event domain.MissionExecutionEvent
		var state string
		if err := rows.Scan(
			&event.ID,
			&event.ExecutionID,
			&event.MissionID,
			&event.DroneID,
			&event.AgentID,
			&event.Type,
			&state,
			&event.Message,
			&event.CurrentMissionItem,
			&event.TotalMissionItems,
			&event.Source,
			&event.CreatedAt,
		); err != nil {
			return nil, err
		}
		event.State = domain.MissionExecutionState(state)
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *PostgresStore) latestMissionExecutionForDrone(ctx context.Context, droneID string) domain.MissionExecution {
	rows, err := s.db.QueryContext(ctx, missionExecutionSelectSQL+`
		WHERE drone_id = $1
	`, droneID)
	if err != nil {
		return domain.MissionExecution{}
	}
	defer rows.Close()
	executions, err := scanMissionExecutions(rows)
	if err != nil {
		return domain.MissionExecution{}
	}
	var latest domain.MissionExecution
	for _, execution := range executions {
		if latest.ID == "" ||
			missionExecutionSnapshotRank(execution.State) > missionExecutionSnapshotRank(latest.State) ||
			(missionExecutionSnapshotRank(execution.State) == missionExecutionSnapshotRank(latest.State) && execution.UpdatedAt.After(latest.UpdatedAt)) ||
			(missionExecutionSnapshotRank(execution.State) == missionExecutionSnapshotRank(latest.State) && execution.UpdatedAt.Equal(latest.UpdatedAt) && execution.ID > latest.ID) {
			latest = execution
		}
	}
	return latest
}

func (s *PostgresStore) latestMissionExecutionInStateTx(ctx context.Context, tx *sql.Tx, missionID string, state domain.MissionExecutionState) domain.MissionExecution {
	execution, err := scanMissionExecution(tx.QueryRowContext(ctx, missionExecutionSelectSQL+`
		WHERE mission_id = $1 AND state = $2
		ORDER BY created_at DESC, id DESC
		LIMIT 1
	`+s.forUpdate(), missionID, string(state)))
	if err != nil {
		return domain.MissionExecution{}
	}
	return execution
}

func (s *PostgresStore) operationalMissionExecutionForDroneTx(ctx context.Context, tx *sql.Tx, droneID string, exceptExecutionID string) domain.MissionExecution {
	rows, err := tx.QueryContext(ctx, missionExecutionSelectSQL+`
		WHERE drone_id = $1 AND id <> $2
		ORDER BY updated_at DESC, id DESC
	`+s.forUpdate(), droneID, exceptExecutionID)
	if err != nil {
		return domain.MissionExecution{}
	}
	defer rows.Close()
	for rows.Next() {
		execution, err := scanMissionExecution(rows)
		if err == nil && isOperationalMissionExecutionState(execution.State) {
			return execution
		}
	}
	return domain.MissionExecution{}
}

func (s *PostgresStore) abortableMissionExecutionForMissionTx(ctx context.Context, tx *sql.Tx, missionID string) domain.MissionExecution {
	rows, err := tx.QueryContext(ctx, missionExecutionSelectSQL+`
		WHERE mission_id = $1
		ORDER BY updated_at DESC, id DESC
	`+s.forUpdate(), missionID)
	if err != nil {
		return domain.MissionExecution{}
	}
	defer rows.Close()
	for rows.Next() {
		execution, err := scanMissionExecution(rows)
		if err == nil && isAbortableMissionExecutionState(execution.State) {
			return execution
		}
	}
	return domain.MissionExecution{}
}

func (s *PostgresStore) validateMissionTx(ctx context.Context, tx *sql.Tx, mission domain.Mission, now time.Time) []domain.MissionValidationError {
	var validationErrors []domain.MissionValidationError
	agent := s.agentForDroneTx(ctx, tx, mission.DroneID)
	if agent.ID == "" {
		validationErrors = append(validationErrors, missionValidationError("agent", "drone must have a registered agent"))
	} else if domain.StatusFromHeartbeat(agent.LastHeartbeatAt, now) != domain.AgentStatusOnline {
		validationErrors = append(validationErrors, missionValidationError("agent", "agent must be online"))
	}

	telemetry, _ := s.telemetryForDroneTx(ctx, tx, mission.DroneID)
	if domain.TelemetryStateFromReceivedAt(telemetry.ReceivedAt, now) != domain.TelemetryStateFresh {
		validationErrors = append(validationErrors, missionValidationError("telemetry", "telemetry must be fresh"))
	}
	if !telemetry.HomePositionSet {
		validationErrors = append(validationErrors, missionValidationError("homePositionSet", "home position must be set"))
	}
	if !gpsFixUsableForMission(telemetry.GPSFix) {
		validationErrors = append(validationErrors, missionValidationError("gpsFix", "GPS fix must be usable"))
	}
	if telemetry.BatteryPercent < MinimumMissionBatteryPercent {
		validationErrors = append(validationErrors, missionValidationError("batteryPercent", fmt.Sprintf("battery must be at least %.0f%%", MinimumMissionBatteryPercent)))
	}
	if strings.TrimSpace(mission.Name) == "" {
		validationErrors = append(validationErrors, missionValidationError("name", "mission name is required"))
	}
	if len(mission.Waypoints) == 0 {
		validationErrors = append(validationErrors, missionValidationError("waypoints", "mission must include at least one waypoint"))
	}
	if len(mission.Waypoints) > MaximumMissionWaypoints {
		validationErrors = append(validationErrors, missionValidationError("waypoints", fmt.Sprintf("mission cannot include more than %d waypoints", MaximumMissionWaypoints)))
	}
	if !validMissionCompletionAction(mission.CompletionAction) {
		validationErrors = append(validationErrors, missionValidationError("completionAction", "completion action must be hold, return_to_launch, or land"))
	}
	for _, waypoint := range mission.Waypoints {
		fieldPrefix := fmt.Sprintf("waypoints[%d]", waypoint.Sequence-1)
		if !validLatitude(waypoint.Latitude) {
			validationErrors = append(validationErrors, missionValidationError(fieldPrefix+".latitude", "latitude must be between -90 and 90"))
		}
		if !validLongitude(waypoint.Longitude) {
			validationErrors = append(validationErrors, missionValidationError(fieldPrefix+".longitude", "longitude must be between -180 and 180"))
		}
		if waypoint.RelativeAltitudeM < MinimumMissionAltitudeM || waypoint.RelativeAltitudeM > MaximumMissionAltitudeM {
			validationErrors = append(validationErrors, missionValidationError(fieldPrefix+".relativeAltitudeM", fmt.Sprintf("relative altitude must be between %.0f and %.0f meters", MinimumMissionAltitudeM, MaximumMissionAltitudeM)))
		}
		if waypoint.SpeedMPS != nil && *waypoint.SpeedMPS <= 0 {
			validationErrors = append(validationErrors, missionValidationError(fieldPrefix+".speedMPS", "speed must be greater than 0 when provided"))
		}
		if waypoint.LoiterTimeS != nil && *waypoint.LoiterTimeS < 0 {
			validationErrors = append(validationErrors, missionValidationError(fieldPrefix+".loiterTimeS", "loiter time cannot be negative when provided"))
		}
	}
	return validationErrors
}

func (s *PostgresStore) validateMissionStartPreconditionsTx(ctx context.Context, tx *sql.Tx, mission domain.Mission, now time.Time) error {
	agent := s.agentForDroneTx(ctx, tx, mission.DroneID)
	if agent.ID == "" {
		return missionStartPreconditionError("drone has no registered agent")
	}
	if domain.StatusFromHeartbeat(agent.LastHeartbeatAt, now) != domain.AgentStatusOnline {
		return missionStartPreconditionError("agent must be online before mission start")
	}
	telemetry, _ := s.telemetryForDroneTx(ctx, tx, mission.DroneID)
	if domain.TelemetryStateFromReceivedAt(telemetry.ReceivedAt, now) != domain.TelemetryStateFresh {
		return missionStartPreconditionError("fresh telemetry is required before mission start")
	}
	return nil
}

func nullTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

func timeFromNull(value sql.NullTime) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time
}

func floatPtrValue(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

var _ Store = (*PostgresStore)(nil)
