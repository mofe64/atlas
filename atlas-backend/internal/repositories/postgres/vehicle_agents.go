package postgres

import (
	"context"
	"encoding/json"

	"github.com/sunnyside/atlas/atlas-backend/internal/data/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/database"
	"github.com/sunnyside/atlas/atlas-backend/internal/repositories"
)

type VehicleAgentRepository struct {
	tx database.TxExecutor
}

var _ repositories.VehicleAgentRepository = (*VehicleAgentRepository)(nil)

func NewVehicleAgentRepository(tx database.TxExecutor) *VehicleAgentRepository {
	return &VehicleAgentRepository{tx: tx}
}

func (r *VehicleAgentRepository) Create(ctx context.Context, input models.NewVehicleAgent) (models.VehicleAgent, error) {
	profile, err := json.Marshal(input.DeviceProfile)
	if err != nil {
		return models.VehicleAgent{}, err
	}
	capabilities, err := json.Marshal(input.Capabilities)
	if err != nil {
		return models.VehicleAgent{}, err
	}

	var agent models.VehicleAgent
	err = scanVehicleAgent(r.tx.QueryRow(ctx, `
		INSERT INTO vehicle_agents (
			organization_id, installation_id, public_key, agent_version,
			protocol_version, device_profile, capabilities,
			enrollment_request_id, enrolled_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
		RETURNING id, organization_id, installation_id, public_key, status,
		          agent_version, protocol_version, device_profile, capabilities,
		          enrollment_request_id, enrolled_at, updated_at, revoked_at
	`, input.OrganizationID, input.InstallationID, input.PublicKey, input.AgentVersion,
		input.ProtocolVersion, profile, capabilities, input.EnrollmentRequestID, input.Now), &agent)
	if err != nil {
		return models.VehicleAgent{}, mapDatabaseError("insert vehicle agent", err)
	}
	return agent, nil
}

func (r *VehicleAgentRepository) FindByID(ctx context.Context, organizationID, agentID string) (models.VehicleAgent, error) {
	var agent models.VehicleAgent
	err := scanVehicleAgent(r.tx.QueryRow(ctx, vehicleAgentSelect+` WHERE organization_id = $1 AND id = $2`, organizationID, agentID), &agent)
	if err != nil {
		return models.VehicleAgent{}, mapDatabaseError("find vehicle agent", err)
	}
	return agent, nil
}

func (r *VehicleAgentRepository) FindByInstallationID(ctx context.Context, organizationID, installationID string) (models.VehicleAgent, error) {
	var agent models.VehicleAgent
	err := scanVehicleAgent(r.tx.QueryRow(ctx, vehicleAgentSelect+` WHERE organization_id = $1 AND installation_id = $2`, organizationID, installationID), &agent)
	if err != nil {
		return models.VehicleAgent{}, mapDatabaseError("find vehicle agent by installation id", err)
	}
	return agent, nil
}

const vehicleAgentSelect = `
	SELECT id, organization_id, installation_id, public_key, status,
	       agent_version, protocol_version, device_profile, capabilities,
	       enrollment_request_id, enrolled_at, updated_at, revoked_at
	FROM vehicle_agents
`

func scanVehicleAgent(row rowScanner, agent *models.VehicleAgent) error {
	var profile, capabilities []byte
	if err := row.Scan(
		&agent.ID,
		&agent.OrganizationID,
		&agent.InstallationID,
		&agent.PublicKey,
		&agent.Status,
		&agent.AgentVersion,
		&agent.ProtocolVersion,
		&profile,
		&capabilities,
		&agent.EnrollmentRequestID,
		&agent.EnrolledAt,
		&agent.UpdatedAt,
		&agent.RevokedAt,
	); err != nil {
		return err
	}
	if err := json.Unmarshal(profile, &agent.DeviceProfile); err != nil {
		return err
	}
	if err := json.Unmarshal(capabilities, &agent.Capabilities); err != nil {
		return err
	}
	return nil
}
