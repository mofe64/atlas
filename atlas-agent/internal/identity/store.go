// Package identity owns the stable local identifiers for one agent installation.
package identity

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const identityFileName = "identity.json"

type Identity struct {
	InstallationID string `json:"installationId"`
	DroneID        string `json:"droneId"`
}

func LoadOrCreate(stateDirectory string) (Identity, error) {
	if stateDirectory == "" {
		return Identity{}, errors.New("state directory is required")
	}
	if err := os.MkdirAll(stateDirectory, 0o700); err != nil {
		return Identity{}, fmt.Errorf("create agent state directory: %w", err)
	}
	path := filepath.Join(stateDirectory, identityFileName)
	raw, err := os.ReadFile(path)
	if err == nil {
		var identity Identity
		if err := json.Unmarshal(raw, &identity); err != nil {
			return Identity{}, fmt.Errorf("decode agent identity: %w", err)
		}
		if identity.InstallationID == "" || identity.DroneID == "" {
			return Identity{}, errors.New("stored agent identity is incomplete")
		}
		return identity, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Identity{}, fmt.Errorf("read agent identity: %w", err)
	}

	identity := Identity{InstallationID: NewID(), DroneID: NewID()}
	raw, err = json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return Identity{}, err
	}
	temporaryPath := path + ".tmp"
	if err := os.WriteFile(temporaryPath, raw, 0o600); err != nil {
		return Identity{}, fmt.Errorf("write temporary agent identity: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return Identity{}, fmt.Errorf("commit agent identity: %w", err)
	}
	return identity, nil
}

func NewID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		panic(fmt.Sprintf("generate random identifier: %v", err))
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}
