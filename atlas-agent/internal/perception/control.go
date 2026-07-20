package perception

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	minimumActivationLease = 3 * time.Second
	maximumActivationLease = 30 * time.Second
)

// Claim declares one independent consumer of onboard inference. Mission claims
// are durable and have no lease; interactive claims must be renewed so a
// crashed Native view cannot leave inference running indefinitely.
type Claim struct {
	ID               string
	Owner            string
	SourceID         string
	DetectionClasses []string
	LeaseDuration    time.Duration
}

type ActivationEvidence struct {
	ClaimID            string        `json:"claimId"`
	Owner              string        `json:"owner"`
	State              string        `json:"state"`
	SourceID           string        `json:"sourceId,omitempty"`
	StreamEpoch        string        `json:"streamEpoch,omitempty"`
	LastFrameID        string        `json:"lastFrameId,omitempty"`
	Model              ModelIdentity `json:"model,omitempty"`
	DetectionClasses   []string      `json:"detectionClasses,omitempty"`
	ObservedAt         time.Time     `json:"observedAt"`
	RuntimeStillActive bool          `json:"runtimeStillActive,omitempty"`
	RemainingClaims    int           `json:"remainingClaims,omitempty"`
}

// Control is the accelerator-neutral activation boundary used by missions and
// leased Native views. Acquire and Release acknowledge observed adapter state,
// not merely successful writes to the local socket.
type Control interface {
	Acquire(context.Context, Claim) (ActivationEvidence, error)
	Release(context.Context, string) (ActivationEvidence, error)
}

type runtimeActivationRequest struct {
	ProtocolVersion string   `json:"protocolVersion"`
	Type            string   `json:"type"`
	RequestID       string   `json:"requestId"`
	DesiredState    string   `json:"desiredState"`
	ClaimIDs        []string `json:"claimIds,omitempty"`
}

type runtimeActivationResult struct {
	RequestID  string    `json:"requestId"`
	State      string    `json:"state"`
	SourceID   string    `json:"sourceId,omitempty"`
	ObservedAt time.Time `json:"observedAt"`
	Error      string    `json:"error,omitempty"`
}

type activeClaim struct {
	claim     Claim
	expiresAt time.Time
}

type runtimeController struct {
	ctx context.Context

	operationMu   sync.Mutex
	mu            sync.Mutex
	writeMu       sync.Mutex
	connection    net.Conn
	claims        map[string]activeClaim
	pending       map[string]chan runtimeActivationResult
	state         string
	result        runtimeActivationResult
	latestFrame   Frame
	frameCount    uint64
	requestID     atomic.Uint64
	tracker       *TrackingStage
	everConnected bool
}

func newRuntimeController(ctx context.Context) *runtimeController {
	controller := &runtimeController{
		ctx:     ctx,
		claims:  make(map[string]activeClaim),
		pending: make(map[string]chan runtimeActivationResult),
		state:   "INACTIVE",
	}
	go controller.expireLeases()
	return controller
}

func (c *runtimeController) setTrackingStage(stage *TrackingStage) {
	c.mu.Lock()
	c.tracker = stage
	c.mu.Unlock()
}

func (c *runtimeController) Acquire(ctx context.Context, claim Claim) (ActivationEvidence, error) {
	if err := validateClaim(claim); err != nil {
		return ActivationEvidence{}, err
	}
	claim.DetectionClasses = normalizedClasses(claim.DetectionClasses)
	c.operationMu.Lock()
	defer c.operationMu.Unlock()
	c.mu.Lock()
	hadClaims := len(c.claims) > 0
	_, existed := c.claims[claim.ID]
	expiresAt := time.Time{}
	if claim.LeaseDuration > 0 {
		expiresAt = time.Now().Add(claim.LeaseDuration)
	}
	c.claims[claim.ID] = activeClaim{claim: claim, expiresAt: expiresAt}
	claimCount := len(c.claims)
	state := c.state
	tracker := c.tracker
	c.mu.Unlock()
	if !hadClaims && !existed && tracker != nil {
		tracker.Reset(TrackingResetActivated)
	}
	baseline := c.observedFrameCount()

	if claimCount > 1 && state == "ACTIVE" {
		if !existed {
			if err := c.waitForFreshFrame(ctx, baseline, claim.SourceID); err != nil {
				c.rollbackClaimAfterFailedAcquire(claim.ID)
				return ActivationEvidence{}, err
			}
		}
		return c.evidence(claim, true), nil
	}
	result, err := c.requestState(ctx, "ACTIVE")
	if err != nil {
		if !existed {
			c.rollbackClaimAfterFailedAcquire(claim.ID)
		}
		return ActivationEvidence{}, err
	}
	if result.State != "ACTIVE" {
		if !existed {
			c.rollbackClaimAfterFailedAcquire(claim.ID)
		}
		return ActivationEvidence{}, fmt.Errorf("perception runtime acknowledged unexpected state %q", result.State)
	}
	if err := c.waitForFreshFrame(ctx, baseline, claim.SourceID); err != nil {
		if !existed {
			c.rollbackClaimAfterFailedAcquire(claim.ID)
		}
		return ActivationEvidence{}, err
	}
	return c.evidence(claim, claimCount > 1), nil
}

func (c *runtimeController) rollbackClaimAfterFailedAcquire(claimID string) {
	c.mu.Lock()
	delete(c.claims, claimID)
	shouldDeactivate := len(c.claims) == 0
	c.mu.Unlock()
	if !shouldDeactivate {
		return
	}
	cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), 5*time.Second)
	defer cancel()
	result, err := c.requestState(cleanupContext, "INACTIVE")
	if err == nil && result.State == "INACTIVE" {
		c.mu.Lock()
		tracker := c.tracker
		c.mu.Unlock()
		if tracker != nil {
			tracker.Reset(TrackingResetDeactivated)
		}
	}
}

func (c *runtimeController) Release(ctx context.Context, claimID string) (ActivationEvidence, error) {
	claimID = strings.TrimSpace(claimID)
	if claimID == "" {
		return ActivationEvidence{}, errors.New("perception claim id is required")
	}
	c.operationMu.Lock()
	defer c.operationMu.Unlock()

	c.mu.Lock()
	entry, existed := c.claims[claimID]
	if existed {
		delete(c.claims, claimID)
	}
	remaining := len(c.claims)
	c.mu.Unlock()
	if !existed {
		return ActivationEvidence{ClaimID: claimID, State: "INACTIVE", ObservedAt: time.Now().UTC(), RemainingClaims: remaining, RuntimeStillActive: remaining > 0}, nil
	}
	if remaining > 0 {
		evidence := c.evidence(entry.claim, true)
		evidence.State = "INACTIVE"
		evidence.RemainingClaims = remaining
		return evidence, nil
	}
	result, err := c.requestState(ctx, "INACTIVE")
	if err != nil {
		return ActivationEvidence{}, err
	}
	if result.State != "INACTIVE" {
		return ActivationEvidence{}, fmt.Errorf("perception runtime acknowledged unexpected state %q", result.State)
	}
	c.mu.Lock()
	tracker := c.tracker
	c.mu.Unlock()
	if tracker != nil {
		tracker.Reset(TrackingResetDeactivated)
	}
	evidence := c.evidence(entry.claim, false)
	evidence.State = "INACTIVE"
	evidence.RemainingClaims = 0
	return evidence, nil
}

func validateClaim(claim Claim) error {
	if strings.TrimSpace(claim.ID) == "" || strings.TrimSpace(claim.Owner) == "" {
		return errors.New("perception claim id and owner are required")
	}
	if claim.LeaseDuration != 0 && (claim.LeaseDuration < minimumActivationLease || claim.LeaseDuration > maximumActivationLease) {
		return errors.New("perception activation lease must be between 3 and 30 seconds")
	}
	return nil
}

func normalizedClasses(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func (c *runtimeController) requestState(ctx context.Context, desiredState string) (runtimeActivationResult, error) {
	requestID := fmt.Sprintf("activation-%d", c.requestID.Add(1))
	response := make(chan runtimeActivationResult, 1)
	c.mu.Lock()
	c.pending[requestID] = response
	claimIDs := make([]string, 0, len(c.claims))
	for id := range c.claims {
		claimIDs = append(claimIDs, id)
	}
	c.mu.Unlock()
	sort.Strings(claimIDs)
	defer func() {
		c.mu.Lock()
		delete(c.pending, requestID)
		c.mu.Unlock()
	}()
	request := runtimeActivationRequest{
		ProtocolVersion: AdapterProtocolVersion,
		Type:            "activation_request",
		RequestID:       requestID,
		DesiredState:    desiredState,
		ClaimIDs:        claimIDs,
	}
	if err := c.writeWhenConnected(ctx, request); err != nil {
		return runtimeActivationResult{}, err
	}
	select {
	case <-ctx.Done():
		return runtimeActivationResult{}, fmt.Errorf("wait for perception runtime %s acknowledgement: %w", strings.ToLower(desiredState), ctx.Err())
	case result := <-response:
		if result.Error != "" {
			return result, errors.New(result.Error)
		}
		return result, nil
	}
}

func (c *runtimeController) writeWhenConnected(ctx context.Context, value any) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		c.mu.Lock()
		connection := c.connection
		c.mu.Unlock()
		if connection != nil {
			payload, err := json.Marshal(value)
			if err != nil {
				return fmt.Errorf("encode perception runtime control: %w", err)
			}
			payload = append(payload, '\n')
			c.writeMu.Lock()
			_, err = connection.Write(payload)
			c.writeMu.Unlock()
			if err == nil {
				return nil
			}
			c.clearConnection(connection)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("perception runtime is not connected: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (c *runtimeController) waitForFreshFrame(ctx context.Context, baseline uint64, sourceID string) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		c.mu.Lock()
		count := c.frameCount
		frame := c.latestFrame
		c.mu.Unlock()
		if count > baseline && (strings.TrimSpace(sourceID) == "" || frame.SourceID == sourceID) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for fresh perception frame after activation: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (c *runtimeController) evidence(claim Claim, stillActive bool) ActivationEvidence {
	c.mu.Lock()
	defer c.mu.Unlock()
	return ActivationEvidence{
		ClaimID:            claim.ID,
		Owner:              claim.Owner,
		State:              c.state,
		SourceID:           c.latestFrame.SourceID,
		StreamEpoch:        c.latestFrame.StreamEpoch,
		LastFrameID:        c.latestFrame.FrameID,
		Model:              c.latestFrame.Model,
		DetectionClasses:   append([]string(nil), claim.DetectionClasses...),
		ObservedAt:         time.Now().UTC(),
		RuntimeStillActive: stillActive,
		RemainingClaims:    len(c.claims),
	}
}

func (c *runtimeController) setConnection(connection net.Conn) {
	c.mu.Lock()
	previous := c.connection
	c.connection = connection
	isReconnect := c.everConnected
	c.everConnected = true
	tracker := c.tracker
	shouldRestore := len(c.claims) > 0 && len(c.pending) == 0
	c.mu.Unlock()
	if isReconnect && tracker != nil {
		tracker.Reset(TrackingResetRuntimeReconnected)
	}
	if previous != nil && previous != connection {
		_ = previous.Close()
	}
	if shouldRestore {
		go func() {
			c.operationMu.Lock()
			defer c.operationMu.Unlock()
			c.mu.Lock()
			claimsRemain := len(c.claims) > 0
			c.mu.Unlock()
			if !claimsRemain {
				return
			}
			controlContext, cancel := context.WithTimeout(c.ctx, 15*time.Second)
			defer cancel()
			_, _ = c.requestState(controlContext, "ACTIVE")
		}()
	}
}

func (c *runtimeController) clearConnection(connection net.Conn) {
	c.mu.Lock()
	if c.connection == connection {
		c.connection = nil
		c.state = "UNAVAILABLE"
	}
	c.mu.Unlock()
}

func (c *runtimeController) observeFrame(frame Frame) {
	c.mu.Lock()
	c.latestFrame = frame
	c.frameCount++
	c.mu.Unlock()
}

func (c *runtimeController) filterFrame(frame Frame) Frame {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.claims) == 0 {
		frame.Detections = nil
		return frame
	}
	allowed := make(map[string]struct{})
	for _, entry := range c.claims {
		if len(entry.claim.DetectionClasses) == 0 {
			return frame
		}
		for _, className := range entry.claim.DetectionClasses {
			allowed[className] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		frame.Detections = nil
		return frame
	}
	filtered := make([]Detection, 0, len(frame.Detections))
	for _, detection := range frame.Detections {
		if _, included := allowed[detection.ClassLabel]; included {
			filtered = append(filtered, detection)
		}
	}
	frame.Detections = filtered
	return frame
}

func (c *runtimeController) observedFrameCount() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.frameCount
}

func (c *runtimeController) handleActivationResult(result runtimeActivationResult) {
	c.mu.Lock()
	c.state = result.State
	c.result = result
	response := c.pending[result.RequestID]
	c.mu.Unlock()
	if response != nil {
		select {
		case response <- result:
		default:
		}
	}
}

func (c *runtimeController) expireLeases() {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case now := <-ticker.C:
			c.mu.Lock()
			expired := make([]string, 0)
			for id, entry := range c.claims {
				if !entry.expiresAt.IsZero() && !now.Before(entry.expiresAt) {
					expired = append(expired, id)
				}
			}
			c.mu.Unlock()
			for _, id := range expired {
				releaseContext, cancel := context.WithTimeout(c.ctx, 5*time.Second)
				_, _ = c.Release(releaseContext, id)
				cancel()
			}
		}
	}
}
