package communicationlinks

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/data/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repositories"
)

type fakeTxManager struct{ repos repositories.Repositories }

func (m fakeTxManager) WithinTransaction(_ context.Context, work func(repositories.Repositories) error) error {
	return work(m.repos)
}

type fakeRepositories struct {
	binding models.VehicleAgentBinding
	links   *fakeLinkRepository
}

func (fakeRepositories) Auth() repositories.AuthRepository                  { return nil }
func (fakeRepositories) Drones() repositories.DroneRepository               { return nil }
func (fakeRepositories) VehicleAgents() repositories.VehicleAgentRepository { return nil }
func (r fakeRepositories) VehicleAgentBindings() repositories.VehicleAgentBindingRepository {
	return fakeBindingRepository{binding: r.binding}
}
func (r fakeRepositories) CommunicationLinks() repositories.CommunicationLinkRepository {
	return r.links
}
func (fakeRepositories) EnrollmentTokens() repositories.EnrollmentTokenRepository { return nil }

type fakeBindingRepository struct{ binding models.VehicleAgentBinding }

func (fakeBindingRepository) Create(context.Context, models.NewVehicleAgentBinding) (models.VehicleAgentBinding, error) {
	panic("unexpected Create call")
}
func (r fakeBindingRepository) FindByID(_ context.Context, organizationID, bindingID string) (models.VehicleAgentBinding, error) {
	if r.binding.OrganizationID != organizationID || r.binding.ID != bindingID {
		return models.VehicleAgentBinding{}, repositories.ErrNotFound
	}
	return r.binding, nil
}
func (fakeBindingRepository) FindActiveByAgent(context.Context, string, string) (models.VehicleAgentBinding, error) {
	panic("unexpected FindActiveByAgent call")
}
func (fakeBindingRepository) FindActiveByDrone(context.Context, string, string) (models.VehicleAgentBinding, error) {
	panic("unexpected FindActiveByDrone call")
}
func (fakeBindingRepository) End(context.Context, string, string, string, time.Time) (models.VehicleAgentBinding, error) {
	panic("unexpected End call")
}

type fakeLinkRepository struct {
	link        models.CommunicationLink
	createCount int
}

func (r *fakeLinkRepository) Create(_ context.Context, input models.NewCommunicationLink) (models.CommunicationLink, error) {
	r.createCount++
	r.link = models.CommunicationLink{
		ID: "link-1", OrganizationID: input.OrganizationID,
		VehicleAgentBindingID: input.VehicleAgentBindingID, SessionInstanceID: input.SessionInstanceID,
		Transport: input.Transport, Roles: input.Roles, Status: models.CommunicationLinkConnecting,
		RemoteAddress: input.RemoteAddress, CommandEligible: input.CommandEligible, OpenedAt: input.Now,
	}
	return r.link, nil
}

func (r *fakeLinkRepository) FindByID(_ context.Context, organizationID, linkID string) (models.CommunicationLink, error) {
	if r.link.ID != linkID || r.link.OrganizationID != organizationID {
		return models.CommunicationLink{}, repositories.ErrNotFound
	}
	return r.link, nil
}

func (r *fakeLinkRepository) FindBySessionInstanceID(_ context.Context, organizationID, bindingID, sessionID string) (models.CommunicationLink, error) {
	if r.link.OrganizationID == organizationID && r.link.VehicleAgentBindingID == bindingID && r.link.SessionInstanceID == sessionID {
		return r.link, nil
	}
	return models.CommunicationLink{}, repositories.ErrNotFound
}

func (r *fakeLinkRepository) RecordHeartbeat(_ context.Context, organizationID, linkID string, health models.CommunicationLinkHealth, now time.Time) (models.CommunicationLink, error) {
	if _, err := r.FindByID(context.Background(), organizationID, linkID); err != nil || r.link.ClosedAt != nil {
		return models.CommunicationLink{}, repositories.ErrNotFound
	}
	r.link.Status = models.CommunicationLinkHealthy
	if r.link.FirstHeartbeatAt == nil {
		first := now
		r.link.FirstHeartbeatAt = &first
	}
	last := now
	r.link.LastHeartbeatAt = &last
	r.link.LatencyMS = health.LatencyMS
	return r.link, nil
}

func (r *fakeLinkRepository) Close(_ context.Context, organizationID, linkID, reason string, now time.Time) (models.CommunicationLink, error) {
	if _, err := r.FindByID(context.Background(), organizationID, linkID); err != nil {
		return models.CommunicationLink{}, err
	}
	if r.link.ClosedAt == nil {
		closed := now
		r.link.ClosedAt = &closed
		r.link.Status = models.CommunicationLinkClosed
		r.link.CloseReason = reason
	}
	return r.link, nil
}

func (r *fakeLinkRepository) CloseOpenForBinding(_ context.Context, organizationID, bindingID, reason string, now time.Time) error {
	if r.link.OrganizationID == organizationID && r.link.VehicleAgentBindingID == bindingID && r.link.ClosedAt == nil {
		closed := now
		r.link.ClosedAt = &closed
		r.link.Status = models.CommunicationLinkClosed
		r.link.CloseReason = reason
	}
	return nil
}

func (r *fakeLinkRepository) FindCurrentCommandLink(context.Context, string, string) (models.CommunicationLink, error) {
	return models.CommunicationLink{}, repositories.ErrNotFound
}

func TestLinkLifecycleUsesOneRuntimeRecord(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	links := &fakeLinkRepository{}
	repos := fakeRepositories{
		binding: models.VehicleAgentBinding{ID: "binding-1", OrganizationID: "org-1", Status: models.VehicleAgentBindingActive},
		links:   links,
	}
	service, err := NewService(fakeTxManager{repos: repos}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	opened, err := service.Open(context.Background(), OpenInput{
		OrganizationID: "org-1", VehicleAgentBindingID: "binding-1", SessionInstanceID: "process-1",
		Transport: "GRPC", Roles: []string{"command", "telemetry"}, CommandEligible: true,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if opened.Status != models.CommunicationLinkConnecting || opened.FirstHeartbeatAt != nil {
		t.Fatalf("opened link = %#v", opened)
	}
	if _, err := service.Open(context.Background(), OpenInput{
		OrganizationID: "org-1", VehicleAgentBindingID: "binding-1", SessionInstanceID: "process-1", Transport: "grpc",
	}); err != nil {
		t.Fatalf("idempotent Open() error = %v", err)
	}
	if links.createCount != 1 {
		t.Fatalf("Open() create count = %d, want 1", links.createCount)
	}

	now = now.Add(5 * time.Second)
	latency := 42.5
	healthy, err := service.RecordHeartbeat(context.Background(), "org-1", opened.ID, models.CommunicationLinkHealth{LatencyMS: &latency})
	if err != nil {
		t.Fatalf("RecordHeartbeat() error = %v", err)
	}
	if healthy.Status != models.CommunicationLinkHealthy || healthy.FirstHeartbeatAt == nil || healthy.LastHeartbeatAt == nil {
		t.Fatalf("healthy link = %#v", healthy)
	}

	now = now.Add(time.Second)
	closed, err := service.Close(context.Background(), "org-1", opened.ID, "stream ended")
	if err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if closed.Status != models.CommunicationLinkClosed || closed.CloseReason != "stream ended" {
		t.Fatalf("closed link = %#v", closed)
	}
}

func TestOpenRejectsEndedBinding(t *testing.T) {
	repos := fakeRepositories{
		binding: models.VehicleAgentBinding{ID: "binding-1", OrganizationID: "org-1", Status: models.VehicleAgentBindingEnded},
		links:   &fakeLinkRepository{},
	}
	service, err := NewService(fakeTxManager{repos: repos}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Open(context.Background(), OpenInput{
		OrganizationID: "org-1", VehicleAgentBindingID: "binding-1", SessionInstanceID: "process-1", Transport: "grpc",
	})
	if !errors.Is(err, ErrBindingUnavailable) {
		t.Fatalf("Open() error = %v, want ErrBindingUnavailable", err)
	}
}
