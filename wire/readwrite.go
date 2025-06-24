package wire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/gertjaap/p2pool-go/logging"
)

var nullHash chainhash.Hash

func ReadVarInt(r io.Reader) (uint64, error) {
	var d uint8
	err := binary.Read(r, binary.LittleEndian, &d)
	if err != nil {
		return 0, err
	}
	var rv uint64
	switch d {
	case 0xff:
		err = binary.Read(r, binary.LittleEndian, &rv)
		if rv < 0x100000000 {
			return 0, errors.New("varint not canonically packed (0xff prefix with value < 2^32)")
		}
	case 0xfe:
		var v uint32
		err = binary.Read(r, binary.LittleEndian, &v)
		rv = uint64(v)
		if rv < 0x10000 {
			return 0, errors.New("varint not canonically packed (0xfe prefix with value < 2^16)")
		}
	case 0xfd:
		var v uint16
		err = binary.Read(r, binary.LittleEndian, &v)
		rv = uint64(v)
		if rv < 0xfd {
			return 0, errors.New("varint not canonically packed (0xfd prefix with value < 0xfd)")
		}
	default:
		rv = uint64(d)
	}
	return rv, err
}

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

func WriteVarString(w io.Writer, s []byte) error {
	err := WriteVarInt(w, uint64(len(s)))
	if err != nil {
		return err
	}
	_, err = w.Write(s)
	return err
}

func ReadIPAddr(r io.Reader) (net.IP, error) {
	b := make([]byte, 16)
	_, err := io.ReadFull(r, b)
	return net.IP(b), err
}

func WriteIPAddr(w io.Writer, ip net.IP) error {
	_, err := w.Write(ip.To16())
	return err
}

func ReadP2PoolAddress(r io.Reader) (P2PoolAddress, error) {
	addr := P2PoolAddress{}
	var err error
	err = binary.Read(r, binary.LittleEndian, &addr.Services)
	if err != nil {
		return addr, err
	}
	addr.Address, err = ReadIPAddr(r)
	if err != nil {
		return addr, err
	}
	err = binary.Read(r, binary.BigEndian, &addr.Port)
	if err != nil {
		return addr, err
	}
	return addr, nil
}

func WriteP2PoolAddress(w io.Writer, addr P2PoolAddress) error {
	var err error
	err = binary.Write(w, binary.LittleEndian, addr.Services)
	if err != nil {
		return err
	}
	err = WriteIPAddr(w, addr.Address)
	if err != nil {
		return err
	}
	err = binary.Write(w, binary.BigEndian, addr.Port)
	return err
}

func ReadChainHash(r io.Reader) (*chainhash.Hash, error) {
	b := make([]byte, chainhash.HashSize)
	_, err := io.ReadFull(r, b)
	if err != nil {
		return nil, err
	}
	return chainhash.NewHash(b)
}

func WriteChainHash(w io.Writer, h *chainhash.Hash) error {
	if h == nil {
		_, err := w.Write(make([]byte, chainhash.HashSize))
		return err
	}
	_, err := w.Write(h.CloneBytes())
	return err
}

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

func ReadFixedBytes(r io.Reader, length int) ([]byte, error) {
	b := make([]byte, length)
	_, err := io.ReadFull(r, b)
	return b, err
}

func WriteFixedBytes(w io.Writer, b []byte, length int) error {
	if len(b) != length {
		return errors.New("byte slice length mismatch for fixed bytes write")
	}
	_, err := w.Write(b)
	return err
}

func ReadInt(r io.Reader, bits int, endian binary.ByteOrder) (*big.Int, error) {
	byteLen := bits / 8
	if bits%8 != 0 {
		return nil, errors.New("bit length must be a multiple of 8")
	}
	rawBytes := make([]byte, byteLen)
	if _, err := io.ReadFull(r, rawBytes); err != nil {
		return nil, err
	}
	if endian == binary.LittleEndian {
		for i, j := 0, len(rawBytes)-1; i < j; i, j = i+1, j-1 {
			rawBytes[i], rawBytes[j] = rawBytes[j], rawBytes[i]
		}
	}
	val := new(big.Int).SetBytes(rawBytes)
	return val, nil
}

func WriteInt(w io.Writer, val *big.Int, bits int, endian binary.ByteOrder) error {
	byteLen := bits / 8
	if bits%8 != 0 {
		return errors.New("bit length must be a multiple of 8")
	}
	rawBytes := val.Bytes()
	if len(rawBytes) < byteLen {
		padding := make([]byte, byteLen-len(rawBytes))
		rawBytes = append(padding, rawBytes...)
	} else if len(rawBytes) > byteLen {
		return errors.New("value too large for specified bit length")
	}
	if endian == binary.LittleEndian {
		for i, j := 0, len(rawBytes)-1; i < j; i, j = i+1, j-1 {
			rawBytes[i], rawBytes[j] = rawBytes[j], rawBytes[i]
		}
	}
	_, err := w.Write(rawBytes)
	return err
}

func ReadFloatingInteger(r io.Reader) (uint32, error) {
	var bits uint32
	err := binary.Read(r, binary.LittleEndian, &bits)
	return bits, err
}

func WriteFloatingInteger(w io.Writer, bits uint32) error {
	return binary.Write(w, binary.LittleEndian, bits)
}

func ReadPossiblyNoneHash(r io.Reader) (*chainhash.Hash, error) {
	hash, err := ReadChainHash(r)
	if err != nil {
		return nil, err
	}
	if hash.IsEqual(&nullHash) {
		return nil, nil
	}
	return hash, nil
}

func WritePossiblyNoneHash(w io.Writer, h *chainhash.Hash, noneValue *chainhash.Hash) error {
	if h == nil {
		return WriteChainHash(w, noneValue)
	}
	return WriteChainHash(w, h)
}

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
		return StaleInfo(val), nil
	}
}

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
		val = uint8(si)
	}
	return binary.Write(w, binary.LittleEndian, val)
}

func ReadTransactionHashRefs(r io.Reader) ([]TransactionHashRef, error) {
	// The VarInt here is the count of PAIRS, not the total number of items.
	pairCount, err := ReadVarInt(r)
	if err != nil {
		return nil, err
	}

	refs := make([]TransactionHashRef, pairCount)
	for i := uint64(0); i < pairCount; i++ {
		// For each pair, read the two VarInts.
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

func WriteTransactionHashRefs(w io.Writer, refs []TransactionHashRef) error {
	// Write the number of PAIRS as the length prefix.
	err := WriteVarInt(w, uint64(len(refs)))
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

// New, resilient function
func ReadShares(r io.Reader) ([]Share, error) {
	shares := make([]Share, 0)
	count, err := ReadVarInt(r)
	if err != nil {
		return shares, fmt.Errorf("failed to read shares count: %v", err)
	}

	for i := uint64(0); i < count; i++ {
		var share Share
		// We pass the reader 'r' to FromBytes. FromBytes will now log the
		// raw hex on failure, which is what we want.
		err = share.FromBytes(r)

		if err != nil {
			// Instead of stopping all processing, we log the error for the
			// problematic share and continue to the next one.
			// The original error (including the hex dump) is already logged inside FromBytes.
			logging.Warnf("Skipping one malformed share from peer (share %d of %d). The error was: %v", i+1, count, err)
			// Since each share is its own self-contained VarString, the reader 'r' is not
			// hopelessly corrupted. When share.FromBytes is called in the next iteration,
			// it will start by reading the next VarInt for Type, and a new VarString for contents,
			// effectively resynchronizing the stream for the next share. We just need to
			// make sure we don't return the error and stop the whole message processing.
			continue
		} else {
			// Only add the share if it was successfully deserialized.
			shares = append(shares, share)
		}
	}

	// We return nil error because we successfully processed the message,
	// even if we had to skip some bad data within it.
	return shares, nil
}

func WriteShares(w io.Writer, shares []Share) error {
	err := WriteVarInt(w, uint64(len(shares)))
	if err != nil {
		return err
	}
	for _, share := range shares {
		shareBytes, err := share.ToBytes()
		if err != nil {
			return err
		}
		_, err = w.Write(shareBytes)
		if err != nil {
			return err
		}
	}
	return nil
}
