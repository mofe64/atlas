package postgres

import (
	"context"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/data/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/database"
	"github.com/sunnyside/atlas/atlas-backend/internal/repositories"
)

type EnrollmentTokenRepository struct {
	tx database.TxExecutor
}

var _ repositories.EnrollmentTokenRepository = (*EnrollmentTokenRepository)(nil)

func NewEnrollmentTokenRepository(tx database.TxExecutor) *EnrollmentTokenRepository {
	return &EnrollmentTokenRepository{tx: tx}
}

func (r *EnrollmentTokenRepository) Create(ctx context.Context, input models.NewEnrollmentToken) (models.EnrollmentToken, error) {
	var token models.EnrollmentToken
	err := scanEnrollmentToken(r.tx.QueryRow(ctx, `
		INSERT INTO vehicle_agent_enrollment_tokens (
			organization_id, created_by_user_id, token_hash, scoped_drone_id,
			created_at, expires_at
		)
		VALUES ($1, $2, $3, NULLIF($4, '')::uuid, $5, $6)
		RETURNING id, organization_id, created_by_user_id, token_hash,
		          COALESCE(scoped_drone_id::text, ''), created_at, expires_at,
		          used_at, COALESCE(enrollment_request_id, ''),
		          COALESCE(enrolled_vehicle_agent_id::text, ''),
		          COALESCE(enrolled_vehicle_agent_binding_id::text, '')
	`, input.OrganizationID, input.CreatedByUserID, input.TokenHash, input.ScopedDroneID, input.Now, input.ExpiresAt), &token)
	if err != nil {
		return models.EnrollmentToken{}, mapDatabaseError("insert vehicle-agent enrollment token", err)
	}
	return token, nil
}

func (r *EnrollmentTokenRepository) FindByHashForUpdate(ctx context.Context, tokenHash []byte) (models.EnrollmentToken, error) {
	var token models.EnrollmentToken
	err := scanEnrollmentToken(r.tx.QueryRow(ctx, enrollmentTokenSelect+` WHERE token_hash = $1 FOR UPDATE`, tokenHash), &token)
	if err != nil {
		return models.EnrollmentToken{}, mapDatabaseError("find vehicle-agent enrollment token", err)
	}
	return token, nil
}

func (r *EnrollmentTokenRepository) MarkUsed(ctx context.Context, tokenID, requestID, agentID, bindingID string, now time.Time) error {
	tag, err := r.tx.Exec(ctx, `
		UPDATE vehicle_agent_enrollment_tokens
		SET used_at = $5, enrollment_request_id = $2, enrolled_vehicle_agent_id = $3,
		    enrolled_vehicle_agent_binding_id = $4
		WHERE id = $1 AND used_at IS NULL
	`, tokenID, requestID, agentID, bindingID, now)
	if err != nil {
		return mapDatabaseError("consume vehicle-agent enrollment token", err)
	}
	if tag.RowsAffected() != 1 {
		return repositories.ErrConflict
	}
	return nil
}

const enrollmentTokenSelect = `
	SELECT id, organization_id, created_by_user_id, token_hash,
	       COALESCE(scoped_drone_id::text, ''), created_at, expires_at,
	       used_at, COALESCE(enrollment_request_id, ''),
	       COALESCE(enrolled_vehicle_agent_id::text, ''),
	       COALESCE(enrolled_vehicle_agent_binding_id::text, '')
	FROM vehicle_agent_enrollment_tokens
`

func scanEnrollmentToken(row rowScanner, token *models.EnrollmentToken) error {
	return row.Scan(
		&token.ID,
		&token.OrganizationID,
		&token.CreatedByUserID,
		&token.TokenHash,
		&token.ScopedDroneID,
		&token.CreatedAt,
		&token.ExpiresAt,
		&token.UsedAt,
		&token.EnrollmentRequestID,
		&token.EnrolledVehicleAgentID,
		&token.EnrolledBindingID,
	)
}
