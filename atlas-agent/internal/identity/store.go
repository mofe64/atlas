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

// Identity represents the stable local identifiers for one agent installation.
// we send this identity to our ground station during registration
type Identity struct {
	InstallationID string `json:"installationId"` // identifies this agent installation
	DroneID        string `json:"droneId"`        // identifies the physical drone
}

// Loads the identity json from the state directory, or creates a new one if it doesn't exist
func LoadOrCreate(stateDirectory string) (Identity, error) {
	if stateDirectory == "" {
		return Identity{}, errors.New("state directory is required")
	}
	// ensure state directory exists
	// mkdirAll creates the directory and all parent directories if they don't exist
	// 0o700 means the directory is readable and writable by the user only
	// Owner: read, write, enter
	// Group: no access
	// Other: no access
	if err := os.MkdirAll(stateDirectory, 0o700); err != nil {
		return Identity{}, fmt.Errorf("create agent state directory: %w", err)
	}

	// construct the path to the identity file
	// filepath.Join builds an operating-system-appropriate path
	path := filepath.Join(stateDirectory, identityFileName)
	// read the identity file
	raw, err := os.ReadFile(path)
	if err == nil {
		// if the file exists, decode the json into an Identity struct
		var identity Identity
		if err := json.Unmarshal(raw, &identity); err != nil {
			return Identity{}, fmt.Errorf("decode agent identity: %w", err)
		}
		// validate the identity and confirm installation and drone IDs are set
		if identity.InstallationID == "" || identity.DroneID == "" {
			return Identity{}, errors.New("stored agent identity is incomplete or invalid")
		}
		// return the identity if it is valid
		return identity, nil
	}
	// if our read failed for some reason other than the file not existing, return an error
	if !errors.Is(err, os.ErrNotExist) {
		return Identity{}, fmt.Errorf("read agent identity: %w", err)
	}

	// if we got here, means that our identity file does not exist, so we need to create a new one
	identity := Identity{InstallationID: NewID(), DroneID: NewID()}
	// json.MarshalIndent converts the struct into JSON using two-space indentation.
	raw, err = json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return Identity{}, err
	}
	// create a temporary file to write the identity to
	// 0o600 gives the owner read/write access and gives nobody else access
	// Writing to a temporary file avoids exposing a partially written identity.json.
	// If the process fails halfway through writing, the final identity path has not yet been published.
	temporaryPath := path + ".tmp"
	if err := os.WriteFile(temporaryPath, raw, 0o600); err != nil {
		return Identity{}, fmt.Errorf("write temporary agent identity: %w", err)
	}
	// rename the temporary file to the final path
	// this is atomic and ensures that the identity file is always in a consistent state
	if err := os.Rename(temporaryPath, path); err != nil {
		return Identity{}, fmt.Errorf("commit agent identity: %w", err)
	}
	return identity, nil
}

func NewID() string {
	// allocate 16 bytes
	value := make([]byte, 16)
	// read 16 bytes of random data into the value slice
	if _, err := rand.Read(value); err != nil {
		// if the read fails, panic with a descriptive error message
		panic(fmt.Sprintf("generate random identifier: %v", err))
	}

	// set the uuid version and variant bits

	// sets the upper four bits of byte 6 to 0100, which marks the uuid as version 4
	value[6] = (value[6] & 0x0f) | 0x40
	// sets the first two bits of byte 8 to 10
	value[8] = (value[8] & 0x3f) | 0x80
	// encode the value slice as a hex string
	encoded := hex.EncodeToString(value)
	// return the uuid as a string in the format of 8-4-4-4-12
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}
