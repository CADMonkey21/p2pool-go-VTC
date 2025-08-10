package verthash

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"os"

	"golang.org/x/crypto/sha3"
)

const VerthashHeaderSize uint32 = 80
const VerthashHashOutSize uint32 = 32
const VerthashP0Size uint32 = 64
const VerthashIter uint32 = 8
const VerthashSubset uint32 = VerthashP0Size * VerthashIter
const VerthashRotations uint32 = 32
const VerthashIndexes uint32 = 4096
const VerthashByteAlignment uint32 = 16

func EnsureVerthashDatafile(file string) error {
	return EnsureVerthashDatafileWithProgress(file, nil)
}

func EnsureVerthashDatafileWithProgress(file string, progress chan float64) error {
	MakeVerthashDatafileIfNotExistsWithProgress(file, progress)
	ok, err := VerifyVerthashDatafile(file)
	if err != nil || !ok {
		os.Remove(file)
		MakeVerthashDatafileWithProgress(file, progress)
		ok, err := VerifyVerthashDatafile(file)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("Could not crate or verify Verthash file")
		}
	}

	return nil

}

func MakeVerthashDatafileIfNotExists(file string) error {
	return MakeVerthashDatafileIfNotExistsWithProgress(file, nil)
}

func MakeVerthashDatafileIfNotExistsWithProgress(file string, progress chan float64) error {
	if _, err := os.Stat(file); os.IsNotExist(err) {
		return MakeVerthashDatafileWithProgress(file, progress)
	}
	return nil
}

func MakeVerthashDatafile(file string) error {
	return MakeVerthashDatafileWithProgress(file, nil)
}

func MakeVerthashDatafileWithProgress(file string, progress chan float64) error {
	pk := sha3.Sum256([]byte("Verthash Proof-of-Space Datafile"))

	NewGraph(17, file, pk[:], progress)

	return nil
}

func VerifyVerthashDatafile(file string) (bool, error) {
	b, err := ioutil.ReadFile(file)
	if err != nil {
		return false, err
	}
	hash := sha256.Sum256(b)
	expectedHash, _ := hex.DecodeString("a55531e843cd56b010114aaf6325b0d529ecf88f8ad47639b6ededafd721aa48")
	if !bytes.Equal(hash[:], expectedHash) {
		return false, fmt.Errorf("Generated file has wrong hash: %x vs %x", hash, expectedHash)
	}
	return true, nil
}

type Verthash struct {
	datFileContent []byte
	datFile        *os.File
}

func fnv1a(a, b uint32) uint32 {
	return (a ^ b) * 0x1000193
}

func NewVerthash(datFileLocation string, keepInRam bool) (*Verthash, error) {
	v := Verthash{}
	var err error
	if keepInRam {
		v.datFileContent, err = ioutil.ReadFile(datFileLocation)
	} else {
		v.datFile, err = os.Open(datFileLocation)
	}
	if err != nil {
		return nil, err
	}

	return &v, nil
}

func (v *Verthash) Close() {
	if v.datFileContent != nil {
		v.datFileContent = []byte{}
	} else {
		v.datFile.Close()
	}
}

func (v *Verthash) SumVerthash(input []byte) ([32]byte, error) {
	p1 := [32]byte{}

	inputCopy := make([]byte, len(input))
	copy(inputCopy[:], input[:])
	sha3hash := sha3.Sum256(inputCopy)

	copy(p1[:], sha3hash[:])
	p0 := make([]byte, VerthashSubset)
	for i := uint32(0); i < VerthashIter; i++ {
		inputCopy[0] += 0x01
		digest64 := sha3.Sum512(inputCopy)
		copy(p0[i*VerthashP0Size:], digest64[:])
	}

	buf := bytes.NewBuffer(p0)
	p0Index := make([]uint32, len(p0)/4)
	for i := 0; i < len(p0Index); i++ {
		binary.Read(buf, binary.LittleEndian, &p0Index[i])
	}

	seekIndexes := make([]uint32, VerthashIndexes)

	for x := uint32(0); x < VerthashRotations; x++ {
		copy(seekIndexes[x*VerthashSubset/4:], p0Index)
		for y := 0; y < len(p0Index); y++ {
			p0Index[y] = (p0Index[y] << 1) | (1 & (p0Index[y] >> 31))
		}
	}

	var datFileSize int64

	if v.datFileContent == nil {
		s, err := v.datFile.Stat()
		if err != nil {
			return [32]byte{}, err
		}
		datFileSize = s.Size()
	} else {
		datFileSize = int64(len(v.datFileContent))
	}

	var valueAccumulator uint32
	var mdiv uint32
	mdiv = ((uint32(datFileSize) - VerthashHashOutSize) / VerthashByteAlignment) + 1
	valueAccumulator = uint32(0x811c9dc5)
	buf = bytes.NewBuffer(p1[:])
	p1Arr := make([]uint32, VerthashHashOutSize/4)
	for i := 0; i < len(p1Arr); i++ {
		binary.Read(buf, binary.LittleEndian, &p1Arr[i])
	}
	for i := uint32(0); i < VerthashIndexes; i++ {
		offset := (fnv1a(seekIndexes[i], valueAccumulator) % mdiv) * VerthashByteAlignment

		data := make([]byte, 32)
		if v.datFileContent != nil {
			data = v.datFileContent[offset : offset+VerthashHashOutSize]
		} else {
			v.datFile.Seek(int64(offset), 0)
			v.datFile.Read(data)
		}

		for i2 := uint32(0); i2 < VerthashHashOutSize/4; i2++ {
			value := binary.LittleEndian.Uint32(data[i2*4 : ((i2 + 1) * 4)])
			p1Arr[i2] = fnv1a(p1Arr[i2], value)
			valueAccumulator = fnv1a(valueAccumulator, value)
		}
	}

	for i := uint32(0); i < VerthashHashOutSize/4; i++ {
		binary.LittleEndian.PutUint32(p1[i*4:], p1Arr[i])
	}

	return p1, nil
}
