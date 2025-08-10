package verthash 

import (
	"crypto/rand" 
	"fmt"
	"log"
	"os"
	"path/filepath"
	// Vendored code (verthash.go, graph.go) is in this package directly
)

// Verthasher is an interface for a Verthash implementation.
type Verthasher interface {
	Hash(headerData []byte) ([]byte, error)
	Close() 
}

// RealVerthasher now uses the Verthash struct and functions
// from the vendored files (verthash.go, graph.go) in this same package.
type RealVerthasher struct {
	hasherInstance *Verthash 
}

// NewRealVerthasher creates a Verthasher using the local (vendored) Verthash code.
func NewRealVerthasher(configuredDatFilePath string) (Verthasher, error) {
	log.Println("Verthash: Initializing REAL Verthasher (using local/vendored code)...")
	
	finalPathToUse := configuredDatFilePath
	found := false

	if configuredDatFilePath != "" {
		if _, err := os.Stat(configuredDatFilePath); err == nil {
			log.Printf("Verthash: Found verthash.dat at configured path: %s", configuredDatFilePath)
			found = true
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("error accessing configured verthash.dat at %s: %w", configuredDatFilePath, err)
		} else {
			log.Printf("Verthash: Configured path '%s' for verthash.dat not found.", configuredDatFilePath)
		}
	} else {
		log.Println("Verthash: verthash_dat_file not specified in configuration.")
	}

	if !found {
		homeDir, err := os.UserHomeDir()
		if err == nil { 
			defaultUserPath := filepath.Join(homeDir, ".vertcoin", "verthash.dat")
			log.Printf("Verthash: Trying default user path: %s", defaultUserPath)
			if _, err := os.Stat(defaultUserPath); err == nil {
				log.Printf("Verthash: Found verthash.dat at default user path: %s", defaultUserPath)
				finalPathToUse = defaultUserPath 
				found = true
			} else if !os.IsNotExist(err) {
				log.Printf("Verthash: Error accessing default verthash.dat at %s: %v", defaultUserPath, err)
			}
		} else {
			log.Printf("Verthash: Could not determine user home directory to check default path.")
		}
	}
	
	if !found {
		log.Printf("Verthash: CRITICAL ERROR - verthash.dat NOT FOUND. Last path checked: %s", finalPathToUse)
		return nil, fmt.Errorf("verthash.dat not found. RealVerthasher cannot operate without it. Last path checked: %s", finalPathToUse)
	}
	
	keepInRam := false 
	hasher, err := NewVerthash(finalPathToUse, keepInRam) 
	if err != nil {
		return nil, fmt.Errorf("RealVerthasher: failed to initialize with verthash.dat '%s': %w", finalPathToUse, err)
	}

	log.Printf("Verthash: REAL Verthasher (vendored) initialized, verthash.dat: %s, keepInRam: %t", finalPathToUse, keepInRam)
	return &RealVerthasher{
		hasherInstance: hasher,
	}, nil
}

// Hash performs Verthash using the local (vendored) Verthash instance.
func (rv *RealVerthasher) Hash(headerData []byte) ([]byte, error) {
	// NEW Log line for clarity
	log.Println("Verthash: RealVerthasher.Hash() CALLED (using vendored code).")
	if rv.hasherInstance == nil {
		return nil, fmt.Errorf("RealVerthasher not properly initialized (hasher instance is nil)")
	}
	if len(headerData) != 80 {
		return nil, fmt.Errorf("real verthash: headerData must be 80 bytes, got %d", len(headerData))
	}
	
	hashArray, err := rv.hasherInstance.SumVerthash(headerData) 
	if err != nil {
		return nil, fmt.Errorf("RealVerthasher: SumVerthash failed: %w", err)
	}
	
	hashSlice := make([]byte, 32)
	copy(hashSlice, hashArray[:]) 
	return hashSlice, nil
}

func (rv *RealVerthasher) Close() {
    if rv.hasherInstance != nil {
        rv.hasherInstance.Close() 
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
	if configuredDatFilePath == "" { /* ... path checking ... */ } else { /* ... path checking ... */ }
	return &DummyVerthasher{datFilePath: finalPathToUse, initialized: true}, nil
}
func (dv *DummyVerthasher) Hash(headerData []byte) ([]byte, error) {
	if !dv.initialized { return nil, fmt.Errorf("dummy verthasher not initialized") }
	if len(headerData) != 80 { return nil, fmt.Errorf("dummy verthash: headerData must be 80 bytes, got %d", len(headerData)) }
	log.Printf("Verthash: DUMMY Hash called for header (first 8 bytes): %x...", headerData[:8])
	dummyHash := make([]byte, 32); _, randErr := rand.Read(dummyHash) 
	if randErr != nil { return nil, fmt.Errorf("failed to generate dummy hash: %w", randErr) }
	log.Printf("Verthash: DUMMY Hash produced (first 8 bytes): %x...", dummyHash[:8])
	return dummyHash, nil 
}
func (dv *DummyVerthasher) Close() { /* Nothing to close */ }
