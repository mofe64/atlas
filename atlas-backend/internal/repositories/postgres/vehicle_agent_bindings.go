package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/data/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/database"
	"github.com/sunnyside/atlas/atlas-backend/internal/repositories"
)

type VehicleAgentBindingRepository struct {
	tx database.TxExecutor
}

var _ repositories.VehicleAgentBindingRepository = (*VehicleAgentBindingRepository)(nil)

func NewVehicleAgentBindingRepository(tx database.TxExecutor) *VehicleAgentBindingRepository {
	return &VehicleAgentBindingRepository{tx: tx}
}

func (r *VehicleAgentBindingRepository) Create(ctx context.Context, input models.NewVehicleAgentBinding) (models.VehicleAgentBinding, error) {
	var binding models.VehicleAgentBinding
	err := scanVehicleAgentBinding(r.tx.QueryRow(ctx, `
		INSERT INTO vehicle_agent_bindings (
			organization_id, vehicle_agent_id, drone_id, status,
			flight_controller_transport, endpoint_description, baud_rate,
			mavlink_system_id, mavlink_component_id,
			observed_flight_controller_uid, bound_at, verified_at
		)
		VALUES ($1, $2, $3, $4::varchar, $5, $6, NULLIF($7, 0), NULLIF($8, 0),
		        NULLIF($9, 0), $10, $11::timestamptz,
		        CASE WHEN $4::varchar = 'active' THEN $11::timestamptz ELSE NULL END)
		RETURNING id, organization_id, vehicle_agent_id, drone_id, status,
		          flight_controller_transport, endpoint_description, baud_rate,
		          mavlink_system_id, mavlink_component_id,
		          observed_flight_controller_uid, bound_at, verified_at, ended_at, end_reason
	`, input.OrganizationID, input.VehicleAgentID, input.DroneID, input.Status,
		input.Attachment.Transport, input.Attachment.EndpointDescription, input.Attachment.BaudRate,
		input.Attachment.SystemID, input.Attachment.ComponentID, input.Attachment.ObservedUID, input.Now), &binding)
	if err != nil {
		return models.VehicleAgentBinding{}, mapDatabaseError("insert vehicle-agent binding", err)
	}
	return binding, nil
}

func (r *VehicleAgentBindingRepository) FindByID(ctx context.Context, organizationID, bindingID string) (models.VehicleAgentBinding, error) {
	return r.find(ctx, "organization_id = $1 AND id = $2", organizationID, bindingID)
}

func (r *VehicleAgentBindingRepository) FindActiveByAgent(ctx context.Context, organizationID, agentID string) (models.VehicleAgentBinding, error) {
	return r.find(ctx, "organization_id = $1 AND vehicle_agent_id = $2 AND status = 'active'", organizationID, agentID)
}

func (r *VehicleAgentBindingRepository) FindActiveByDrone(ctx context.Context, organizationID, droneID string) (models.VehicleAgentBinding, error) {
	return r.find(ctx, "organization_id = $1 AND drone_id = $2 AND status = 'active'", organizationID, droneID)
}

func (r *VehicleAgentBindingRepository) End(ctx context.Context, organizationID, bindingID, reason string, now time.Time) (models.VehicleAgentBinding, error) {
	var binding models.VehicleAgentBinding
	err := scanVehicleAgentBinding(r.tx.QueryRow(ctx, vehicleAgentBindingSelect+`
		WHERE organization_id = $1 AND id = $2
		FOR UPDATE
	`, organizationID, bindingID), &binding)
	if err != nil {
		return models.VehicleAgentBinding{}, mapDatabaseError("find vehicle-agent binding to end", err)
	}
	if binding.Status == models.VehicleAgentBindingEnded {
		return binding, nil
	}
	err = scanVehicleAgentBinding(r.tx.QueryRow(ctx, `
		UPDATE vehicle_agent_bindings
		SET status = 'ended', ended_at = $3, end_reason = $4
		WHERE organization_id = $1 AND id = $2
		RETURNING id, organization_id, vehicle_agent_id, drone_id, status,
		          flight_controller_transport, endpoint_description, baud_rate,
		          mavlink_system_id, mavlink_component_id,
		          observed_flight_controller_uid, bound_at, verified_at, ended_at, end_reason
	`, organizationID, bindingID, now, reason), &binding)
	if err != nil {
		return models.VehicleAgentBinding{}, mapDatabaseError("end vehicle-agent binding", err)
	}
	return binding, nil
}

func (r *VehicleAgentBindingRepository) find(ctx context.Context, predicate string, args ...any) (models.VehicleAgentBinding, error) {
	var binding models.VehicleAgentBinding
	err := scanVehicleAgentBinding(r.tx.QueryRow(ctx, vehicleAgentBindingSelect+` WHERE `+predicate, args...), &binding)
	if err != nil {
		return models.VehicleAgentBinding{}, mapDatabaseError("find vehicle-agent binding", err)
	}
	return binding, nil
}

const vehicleAgentBindingSelect = `
	SELECT id, organization_id, vehicle_agent_id, drone_id, status,
	       flight_controller_transport, endpoint_description, baud_rate,
	       mavlink_system_id, mavlink_component_id,
	       observed_flight_controller_uid, bound_at, verified_at, ended_at, end_reason
	FROM vehicle_agent_bindings
`

func scanVehicleAgentBinding(row rowScanner, binding *models.VehicleAgentBinding) error {
	var baudRate, systemID, componentID sql.NullInt32
	if err := row.Scan(
		&binding.ID,
		&binding.OrganizationID,
		&binding.VehicleAgentID,
		&binding.DroneID,
		&binding.Status,
		&binding.Attachment.Transport,
		&binding.Attachment.EndpointDescription,
		&baudRate,
		&systemID,
		&componentID,
		&binding.Attachment.ObservedUID,
		&binding.BoundAt,
		&binding.VerifiedAt,
		&binding.EndedAt,
		&binding.EndReason,
	); err != nil {
		return err
	}
	binding.Attachment.BaudRate = int(baudRate.Int32)
	binding.Attachment.SystemID = int(systemID.Int32)
	binding.Attachment.ComponentID = int(componentID.Int32)
	return nil
}
