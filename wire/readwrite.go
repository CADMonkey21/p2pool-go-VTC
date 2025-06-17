package wire

import (
	"encoding/binary"
	"errors"
	"io"
	"math/big"
	"net" // Added net import for net.IP type

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/gertjaap/p2pool-go/logging"
)

var nullHash chainhash.Hash // Represents a zero hash for P2Pool's 'PossiblyNoneType(0, pack.IntType(256))'

// ReadVarInt reads a variable-length integer from the given reader.
// This matches the Bitcoin protocol's compact size format.
func ReadVarInt(r io.Reader) (uint64, error) {
	var d uint8
	err := binary.Read(r, binary.LittleEndian, &d)
	if err != nil {
		return 0, err
	}
	var rv uint64
	switch d {
	case 0xff: // 8 bytes
		err = binary.Read(r, binary.LittleEndian, &rv)
		if rv < 0x100000000 { // Check for canonical encoding (should be 0xff for values >= 2^32)
			return 0, errors.New("varint not canonically packed (0xff prefix with value < 2^32)")
		}
	case 0xfe: // 4 bytes
		var v uint32
		err = binary.Read(r, binary.LittleEndian, &v)
		rv = uint64(v)
		if rv < 0x10000 { // Check for canonical encoding (should be 0xfe for values >= 2^16)
			return 0, errors.New("varint not canonically packed (0xfe prefix with value < 2^16)")
		}
	case 0xfd: // 2 bytes
		var v uint16
		err = binary.Read(r, binary.LittleEndian, &v)
		rv = uint64(v)
		if rv < 0xfd { // Check for canonical encoding (should be 0xfd for values >= 0xfd)
			return 0, errors.New("varint not canonically packed (0xfd prefix with value < 0xfd)")
		}
	default: // 1 byte
		rv = uint64(d)
	}
	return rv, err
}

// WriteVarInt writes a variable-length integer to the given writer.
// This matches the Bitcoin protocol's compact size format.
func WriteVarInt(w io.Writer, val uint64) error {
	if val < 0xfd {
		return binary.Write(w, binary.LittleEndian, uint8(val))
	} else if val <= 0xffff {
		binary.Write(w, binary.LittleEndian, uint8(0xfd))
		return binary.Write(w, binary.LittleEndian, uint16(val))
	} else if val <= 0xffffffff {
		binary.Write(w, binary.LittleEndian, uint8(0xfe))
		return binary.Write(w, binary.LittleEndian, uint32(val))
	} else {
		binary.Write(w, binary.LittleEndian, uint8(0xff))
		return binary.Write(w, binary.LittleEndian, val)
	}
}

// ReadVarString reads a variable-length string (as a byte slice) from the reader.
// It reads a varint length prefix, then that many bytes.
func ReadVarString(r io.Reader) ([]byte, error) {
	count, err := ReadVarInt(r)
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return []byte{}, nil
	}
	buf := make([]byte, count)
	_, err = io.ReadFull(r, buf)
	if err != nil {
		return nil, err
	}
	return buf, nil
}

// WriteVarString writes a variable-length string (as a byte slice) to the writer.
// It writes a varint length prefix, then the bytes.
func WriteVarString(w io.Writer, s []byte) error {
	err := WriteVarInt(w, uint64(len(s)))
	if err != nil {
		return err
	}
	_, err = w.Write(s)
	return err
}

// ReadIPAddr reads a 16-byte IP address into a net.IP type.
func ReadIPAddr(r io.Reader) (net.IP, error) {
	b := make([]byte, 16)
	_, err := io.ReadFull(r, b)
	return net.IP(b), err
}

// WriteIPAddr writes a 16-byte IP address from a net.IP type.
func WriteIPAddr(w io.Writer, ip net.IP) error {
	_, err := w.Write(ip.To16()) // Ensure it's a 16-byte representation
	return err
}

// ReadP2PoolAddress reads a P2PoolAddress structure (services, IP, port).
func ReadP2PoolAddress(r io.Reader) (P2PoolAddress, error) {
	addr := P2PoolAddress{}
	var err error
	err = binary.Read(r, binary.LittleEndian, &addr.Services)
	if err != nil {
		return addr, err
	}
	addr.Address, err = ReadIPAddr(r) // Now correctly returns net.IP
	if err != nil {
		return addr, err
	}
	err = binary.Read(r, binary.BigEndian, &addr.Port) // Port is big-endian
	if err != nil {
		return addr, err
	}
	return addr, nil
}

// WriteP2PoolAddress writes a P2PoolAddress structure (services, IP, port).
func WriteP2PoolAddress(w io.Writer, addr P2PoolAddress) error {
	var err error
	err = binary.Write(w, binary.LittleEndian, addr.Services)
	if err != nil {
		return err
	}
	err = WriteIPAddr(w, addr.Address) // Now correctly passes net.IP
	if err != nil {
		return err
	}
	err = binary.Write(w, binary.BigEndian, addr.Port) // Port is big-endian
	return err
}


// ReadChainHash reads a 32-byte hash (chainhash.Hash) from the reader.
func ReadChainHash(r io.Reader) (*chainhash.Hash, error) {
	b := make([]byte, chainhash.HashSize)
	_, err := io.ReadFull(r, b)
	if err != nil {
		return nil, err
	}
	return chainhash.NewHash(b)
}

// WriteChainHash writes a 32-byte hash (chainhash.Hash) to the writer.
func WriteChainHash(w io.Writer, h *chainhash.Hash) error {
	if h == nil {
		// P2Pool Python code uses 0 for None. Need to write 32 zero bytes.
		_, err := w.Write(make([]byte, chainhash.HashSize))
		return err
	}
	_, err := w.Write(h.CloneBytes())
	return err
}

// ReadChainHashList reads a list of chainhash.Hash.
func ReadChainHashList(r io.Reader) ([]*chainhash.Hash, error) {
	count, err := ReadVarInt(r)
	if err != nil {
		return nil, err
	}
	hashes := make([]*chainhash.Hash, count)
	for i := uint64(0); i < count; i++ {
		hashes[i], err = ReadChainHash(r)
		if err != nil {
			return nil, err
		}
	}
	return hashes, nil
}

// WriteChainHashList writes a list of chainhash.Hash.
func WriteChainHashList(w io.Writer, l []*chainhash.Hash) error {
	err := WriteVarInt(w, uint64(len(l)))
	if err != nil {
		return err
	}
	for _, h := range l {
		err = WriteChainHash(w, h)
		if err != nil {
			return err
		}
	}
	return nil
}

// ReadFixedBytes reads a fixed number of bytes from the reader.
func ReadFixedBytes(r io.Reader, length int) ([]byte, error) {
	b := make([]byte, length)
	_, err := io.ReadFull(r, b)
	return b, err
}

// WriteFixedBytes writes a fixed number of bytes to the writer.
func WriteFixedBytes(w io.Writer, b []byte, length int) error { // Added 'length' parameter
	if len(b) != length { // Ensure byte slice matches expected fixed length
		return errors.New("byte slice length mismatch for fixed bytes write")
	}
	_, err := w.Write(b)
	return err
}

// ReadInt reads an integer of a specified bit length and endianness into a big.Int.
// It handles sizes beyond standard Go int types by reading raw bytes.
func ReadInt(r io.Reader, bits int, endian binary.ByteOrder) (*big.Int, error) {
	byteLen := bits / 8
	if bits%8 != 0 {
		return nil, errors.New("bit length must be a multiple of 8")
	}

	rawBytes := make([]byte, byteLen)
	if _, err := io.ReadFull(r, rawBytes); err != nil {
		return nil, err
	}

	// Python's pack.IntType(bits) uses hex conversion with reversal for little-endian.
	// We need to reverse bytes for little-endian to get the correct big-endian representation for big.Int.
	if endian == binary.LittleEndian {
		for i, j := 0, len(rawBytes)-1; i < j; i, j = i+1, j-1 {
			rawBytes[i], rawBytes[j] = rawBytes[j], rawBytes[i]
		}
	}
	
	val := new(big.Int).SetBytes(rawBytes)
	return val, nil
}

// WriteInt writes a big.Int to a specified bit length and endianness.
// It handles sizes beyond standard Go int types by converting to raw bytes.
func WriteInt(w io.Writer, val *big.Int, bits int, endian binary.ByteOrder) error {
	byteLen := bits / 8
	if bits%8 != 0 {
		return errors.New("bit length must be a multiple of 8")
	}

	// Convert big.Int to byte slice. big.Int.Bytes() returns big-endian.
	rawBytes := val.Bytes()
	
	// Pad with leading zeros if necessary (e.g., for fixed-size types like hashes).
	if len(rawBytes) < byteLen {
		padding := make([]byte, byteLen-len(rawBytes))
		rawBytes = append(padding, rawBytes...)
	} else if len(rawBytes) > byteLen {
		// If the value is too large for the specified bit length.
		// This might indicate an error or an overflow.
		return errors.New("value too large for specified bit length")
	}

	// Python's pack.IntType(bits) with little-endian writes reversed bytes.
	// We need to reverse back if the original was little-endian.
	if endian == binary.LittleEndian {
		for i, j := 0, len(rawBytes)-1; i < j; i, j = i+1, j-1 {
			rawBytes[i], rawBytes[j] = rawBytes[j], rawBytes[i]
		}
	}

	_, err := w.Write(rawBytes)
	return err
}

// ReadFloatingInteger reads a Bitcoin compact "bits" format (uint32).
func ReadFloatingInteger(r io.Reader) (uint32, error) {
	var bits uint32
	err := binary.Read(r, binary.LittleEndian, &bits)
	return bits, err
}

// WriteFloatingInteger writes a Bitcoin compact "bits" format (uint32).
func WriteFloatingInteger(w io.Writer, bits uint32) error {
	return binary.Write(w, binary.LittleEndian, bits)
}

// ReadPossiblyNoneHash reads a hash that could be represented as all zeros for None.
// noneValue is the specific hash value that represents None (usually all zeros).
func ReadPossiblyNoneHash(r io.Reader, noneValue *chainhash.Hash) (*chainhash.Hash, error) {
	hash, err := ReadChainHash(r)
	if err != nil {
		return nil, err
	}
	if hash.IsEqual(noneValue) {
		return nil, nil // Interpret the 'noneValue' hash as nil (Python's None)
	}
	return hash, nil
}

// WritePossiblyNoneHash writes a hash, writing 'noneValue' if the hash is nil.
func WritePossiblyNoneHash(w io.Writer, h *chainhash.Hash, noneValue *chainhash.Hash) error {
	if h == nil {
		return WriteChainHash(w, noneValue) // Write the specific 'noneValue' hash for nil
	}
	return WriteChainHash(w, h)
}

// StaleInfo enum for Python compatibility
func ReadStaleInfo(r io.Reader) (StaleInfo, error) {
	var val uint8
	err := binary.Read(r, binary.LittleEndian, &val)
	if err != nil {
		return StaleInfoNone, err
	}
	switch val {
	case 0:
		return StaleInfoNone, nil
	case 253:
		return StaleInfoOrphan, nil
	case 254:
		return StaleInfoDOA, nil
	default:
		// For unkX values, Python's code uses "unk%i" % (k,)
		// We'll just store the raw byte value and assume it's valid.
		return StaleInfo(val), nil
	}
}

// WriteStaleInfo writes a StaleInfo enum to a single byte.
func WriteStaleInfo(w io.Writer, si StaleInfo) error {
	var val uint8
	switch si {
	case StaleInfoNone:
		val = 0
	case StaleInfoOrphan:
		val = 253
	case StaleInfoDOA:
		val = 254
	default:
		val = uint8(si) // For "unkX" values, write the raw byte
	}
	return binary.Write(w, binary.LittleEndian, val)
}


// ReadTransactionHashRefs reads a list of TransactionHashRef pairs.
// Python: pack.ListType(pack.VarIntType(), 2) -> pairs of share_count, tx_count
func ReadTransactionHashRefs(r io.Reader) ([]TransactionHashRef, error) {
	count, err := ReadVarInt(r) // This count is for pairs, so it's len(list)//2
	if err != nil {
		return nil, err
	}

	refs := make([]TransactionHashRef, count)
	for i := uint64(0); i < count; i++ {
		shareCount, err := ReadVarInt(r)
		if err != nil {
			return nil, err
		}
		txCount, err := ReadVarInt(r)
		if err != nil {
			return nil, err
		}
		refs[i] = TransactionHashRef{
			ShareCount: shareCount,
			TxCount:    txCount,
		}
	}
	return refs, nil
}

// WriteTransactionHashRefs writes a list of TransactionHashRef pairs.
func WriteTransactionHashRefs(w io.Writer, refs []TransactionHashRef) error {
	err := WriteVarInt(w, uint64(len(refs))) // Count is for pairs
	if err != nil {
		return err
	}
	for _, ref := range refs {
		err := WriteVarInt(w, ref.ShareCount)
		if err != nil {
			return err
		}
		err = WriteVarInt(w, ref.TxCount)
		if err != nil {
			return err
		}
	}
	return nil
}


// ReadShares now fully deserializes incoming shares.
// It uses Share.FromBytes to parse each individual share.
func ReadShares(r io.Reader) ([]Share, error) {
	shares := make([]Share, 0)
	count, err := ReadVarInt(r)
	if err != nil {
		return shares, err
	}

	for i := uint64(0); i < count; i++ {
		// Each share is wrapped in a type (VarInt) and contents (VarStr)
		// Python: share_type: ('type', pack.VarIntType()),
		var shareType uint64 // Use uint64 to read VarInt
		shareType, err = ReadVarInt(r) // Corrected: Read Type as VarInt
		if err != nil {
			return shares, err
		}

		contents, err := ReadVarString(r)
		if err != nil {
			return shares, err
		}

		share := Share{Type: shareType}
		err = share.FromBytes(contents)
		if err != nil {
			logging.Errorf("Failed to deserialize individual share contents: %v", err)
			return shares, err
		}
		shares = append(shares, share)
	}
	return shares, nil
}

// WriteShares now fully serializes outgoing shares.
// It uses Share.ToBytes to serialize each individual share.
func WriteShares(w io.Writer, shares []Share) error {
	err := WriteVarInt(w, uint64(len(shares)))
	if err != nil {
		return err
	}

	for _, share := range shares {
		// Write the share type
		// Python: share_type: ('type', pack.VarIntType()),
		err = WriteVarInt(w, share.Type) // Corrected: Write Type as VarInt
		if err != nil {
			return err
		}

		// Get the serialized contents of the share
		contents, err := share.ToBytes()
		if err != nil {
			return err
		}

		// Write the contents as a VarString (length prefix + data)
		err = WriteVarString(w, contents)
		if err != nil {
			return err
		}
	}
	return nil
}
