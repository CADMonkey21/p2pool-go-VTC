package verthash

import (
	"crypto/rand"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/CADMonkey21/p2pool-go-VTC/logging"
	// Vendored code (verthash.go, graph.go) is in this package directly
)

// Verthasher is an interface for a Verthash implementation.
type Verthasher interface {
	Hash(headerData []byte) ([]byte, error)
	Close()
}

// implementation is the primary Verthasher that uses the vendored functions.
type implementation struct {
	hasherInstance *Verthash
}

// New creates a Verthasher using the local (vendored) Verthash code.
func New(configuredDatFilePath string) (Verthasher, error) {
	logging.Infof("Verthash: Initializing Verthasher...")

	finalPathToUse := configuredDatFilePath
	found := false

	if configuredDatFilePath != "" {
		if _, err := os.Stat(configuredDatFilePath); err == nil {
			logging.Debugf("Verthash: Found verthash.dat at configured path: %s", configuredDatFilePath)
			found = true
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("error accessing configured verthash.dat at %s: %w", configuredDatFilePath, err)
		} else {
			logging.Debugf("Verthash: Configured path '%s' for verthash.dat not found.", configuredDatFilePath)
		}
	} else {
		logging.Debugf("Verthash: verthash_dat_file not specified in configuration.")
	}

	if !found {
		homeDir, err := os.UserHomeDir()
		if err == nil {
			defaultUserPath := filepath.Join(homeDir, ".vertcoin", "verthash.dat")
			logging.Debugf("Verthash: Trying default user path: %s", defaultUserPath)
			if _, err := os.Stat(defaultUserPath); err == nil {
				logging.Infof("Verthash: Found verthash.dat at default user path: %s", defaultUserPath)
				finalPathToUse = defaultUserPath
				found = true
			} else if !os.IsNotExist(err) {
				logging.Warnf("Verthash: Error accessing default verthash.dat at %s: %v", defaultUserPath, err)
			}
		} else {
			logging.Warnf("Verthash: Could not determine user home directory to check default path.")
		}
	}

	if !found {
		logging.Fatalf("Verthash: CRITICAL ERROR - verthash.dat NOT FOUND. Please ensure it is present at the configured path, or in the default location (~/.vertcoin/verthash.dat).")
		return nil, fmt.Errorf("verthash.dat not found")
	}

	keepInRam := false
	hasher, err := NewVerthash(finalPathToUse, keepInRam)
	if err != nil {
		return nil, fmt.Errorf("verthasher: failed to initialize with verthash.dat '%s': %w", finalPathToUse, err)
	}

	logging.Infof("Verthash: Verthasher initialized successfully (verthash.dat: %s, keepInRam: %t)", finalPathToUse, keepInRam)
	return &implementation{
		hasherInstance: hasher,
	}, nil
}

// Hash performs Verthash using the local (vendored) Verthash instance.
func (v *implementation) Hash(headerData []byte) ([]byte, error) {
	logging.Debugf("Verthash: Hashing share header.")
	if v.hasherInstance == nil {
		return nil, fmt.Errorf("verthasher not properly initialized (hasher instance is nil)")
	}
	if len(headerData) != 80 {
		return nil, fmt.Errorf("verthash: headerData must be 80 bytes, got %d", len(headerData))
	}

	hashArray, err := v.hasherInstance.SumVerthash(headerData)
	if err != nil {
		return nil, fmt.Errorf("verthasher: SumVerthash failed: %w", err)
	}

	hashSlice := make([]byte, 32)
	copy(hashSlice, hashArray[:])
	return hashSlice, nil
}

func (v *implementation) Close() {
	if v.hasherInstance != nil {
		v.hasherInstance.Close()
	}
}

// --- DummyVerthasher ---
type DummyVerthasher struct {
	datFilePath string
	initialized bool
}

func NewDummyVerthasher(configuredDatFilePath string) (Verthasher, error) {
	log.Println("Verthash: Initializing DUMMY Verthasher...")
	finalPathToUse := configuredDatFilePath
	if configuredDatFilePath == "" { /* ... path checking ... */
	} else { /* ... path checking ... */
	}
	return &DummyVerthasher{datFilePath: finalPathToUse, initialized: true}, nil
}
func (dv *DummyVerthasher) Hash(headerData []byte) ([]byte, error) {
	if !dv.initialized {
		return nil, fmt.Errorf("dummy verthasher not initialized")
	}
	if len(headerData) != 80 {
		return nil, fmt.Errorf("dummy verthash: headerData must be 80 bytes, got %d", len(headerData))
	}
	log.Printf("Verthash: DUMMY Hash called for header (first 8 bytes): %x...", headerData[:8])
	dummyHash := make([]byte, 32)
	_, randErr := rand.Read(dummyHash)
	if randErr != nil {
		return nil, fmt.Errorf("failed to generate dummy hash: %w", randErr)
	}
	log.Printf("Verthash: DUMMY Hash produced (first 8 bytes): %x...", dummyHash[:8])
	return dummyHash, nil
}
func (dv *DummyVerthasher) Close() { /* Nothing to close */ }
