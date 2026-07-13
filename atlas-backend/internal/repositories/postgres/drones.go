package postgres

import (
	"context"

	"github.com/sunnyside/atlas/atlas-backend/internal/data/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/database"
	"github.com/sunnyside/atlas/atlas-backend/internal/repositories"
)

type DroneRepository struct {
	tx database.TxExecutor
}

var _ repositories.DroneRepository = (*DroneRepository)(nil)

func NewDroneRepository(tx database.TxExecutor) *DroneRepository {
	return &DroneRepository{tx: tx}
}

func (r *DroneRepository) Create(ctx context.Context, input models.NewDrone) (models.Drone, error) {
	var drone models.Drone
	err := scanDrone(r.tx.QueryRow(ctx, `
		INSERT INTO drones (
			organization_id, name, flight_controller_uid, serial_number,
			vehicle_type, created_at, updated_at
		)
		VALUES ($1, $2, NULLIF($3, ''), $4, $5, $6, $6)
		RETURNING id, organization_id, name, COALESCE(flight_controller_uid, ''),
		          serial_number, vehicle_type, status, created_at, updated_at, archived_at
	`, input.OrganizationID, input.Name, input.FlightControllerUID, input.SerialNumber, input.VehicleType, input.Now), &drone)
	if err != nil {
		return models.Drone{}, mapDatabaseError("insert drone", err)
	}
	return drone, nil
}

func (r *DroneRepository) FindByID(ctx context.Context, organizationID, droneID string) (models.Drone, error) {
	var drone models.Drone
	err := scanDrone(r.tx.QueryRow(ctx, droneSelect+` WHERE organization_id = $1 AND id = $2`, organizationID, droneID), &drone)
	if err != nil {
		return models.Drone{}, mapDatabaseError("find drone", err)
	}
	return drone, nil
}

func (r *DroneRepository) FindByFlightControllerUID(ctx context.Context, organizationID, uid string) (models.Drone, error) {
	var drone models.Drone
	err := scanDrone(r.tx.QueryRow(ctx, droneSelect+` WHERE organization_id = $1 AND flight_controller_uid = $2`, organizationID, uid), &drone)
	if err != nil {
		return models.Drone{}, mapDatabaseError("find drone by flight-controller uid", err)
	}
	return drone, nil
}

func (r *DroneRepository) AssignIdentityIfUnset(ctx context.Context, organizationID, droneID string, identity models.DroneIdentity) (models.Drone, error) {
	var drone models.Drone
	err := scanDrone(r.tx.QueryRow(ctx, `
		UPDATE drones
		SET flight_controller_uid = COALESCE(flight_controller_uid, NULLIF($3, '')),
		    serial_number = CASE WHEN serial_number = '' THEN $4 ELSE serial_number END,
		    vehicle_type = CASE WHEN vehicle_type = 'unknown' THEN $5 ELSE vehicle_type END,
		    updated_at = $6
		WHERE organization_id = $1 AND id = $2
		  AND (flight_controller_uid IS NULL OR flight_controller_uid = NULLIF($3, ''))
		RETURNING id, organization_id, name, COALESCE(flight_controller_uid, ''),
		          serial_number, vehicle_type, status, created_at, updated_at, archived_at
	`, organizationID, droneID, identity.FlightControllerUID, identity.SerialNumber, identity.VehicleType, identity.Now), &drone)
	if err != nil {
		return models.Drone{}, mapDatabaseError("assign drone flight-controller identity", err)
	}
	return drone, nil
}

const droneSelect = `
	SELECT id, organization_id, name, COALESCE(flight_controller_uid, ''),
	       serial_number, vehicle_type, status, created_at, updated_at, archived_at
	FROM drones
`

func scanDrone(row rowScanner, drone *models.Drone) error {
	return row.Scan(
		&drone.ID,
		&drone.OrganizationID,
		&drone.Name,
		&drone.FlightControllerUID,
		&drone.SerialNumber,
		&drone.VehicleType,
		&drone.Status,
		&drone.CreatedAt,
		&drone.UpdatedAt,
		&drone.ArchivedAt,
	)
}
