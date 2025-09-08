package util

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
)

func GetMyPublicIP() (net.IP, error) {
	url := "https://api.ipify.org?format=text" // we are using a pulib IP API, we're using ipify here, below are some others
	// https://www.ipify.org
	// http://myexternalip.com
	// http://api.ident.me
	// http://whatismyipaddress.com/api
	fmt.Printf("Getting IP address from  ipify ...\n")
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	ip, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return net.ParseIP(string(ip)), nil
}

func Sha256d(b []byte) []byte {
	h := sha256.Sum256(b)
	h2 := sha256.Sum256(h[:])
	return h2[:]
}

func GetRandomId() *chainhash.Hash {
	idBytes := make([]byte, 32)
	rand.Read(idBytes)
	id, _ := chainhash.NewHash(idBytes)
	return id
}

// ReverseBytes is a helper for handling endianness differences.
func ReverseBytes(b []byte) []byte {
	r := make([]byte, len(b))
	for i := 0; i < len(b); i++ {
		r[i] = b[len(b)-1-i]
	}
	return r
}

// ComputeMerkleRootFromLink calculates the merkle root by applying a branch to a leaf hash.
func ComputeMerkleRootFromLink(leaf *chainhash.Hash, branch []*chainhash.Hash, index uint64) *chainhash.Hash {
	currentHash := leaf
	for _, branchHash := range branch {
		var combined []byte
		if (index & 1) == 0 {
			combined = append(currentHash[:], branchHash[:]...)
		} else {
			combined = append(branchHash[:], currentHash[:]...)
		}
		newHashBytes := Sha256d(combined)
		currentHash, _ = chainhash.NewHash(newHashBytes)
		index >>= 1
	}
	return currentHash
}
