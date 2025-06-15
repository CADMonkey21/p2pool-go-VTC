package wire

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"time"

	// "github.com/btcsuite/btcd/blockchain" // Removed unused import
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	btcwire "github.com/btcsuite/btcd/wire"
	"github.com/gertjaap/p2pool-go/logging"
	p2pnet "github.com/gertjaap/p2pool-go/net"
	"github.com/gertjaap/p2pool-go/util"
)

// This file contains all the low-level functions for reading and writing
// p2pool data structures from the network stream.

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
	case 0xfe:
		var v uint32
		err = binary.Read(r, binary.LittleEndian, &v)
		rv = uint64(v)
	case 0xfd:
		var v uint16
		err = binary.Read(r, binary.LittleEndian, &v)
		rv = uint64(v)
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

func ReadVarString(r io.Reader) (string, error) {
	count, err := ReadVarInt(r)
	if err != nil {
		return "", err
	}
	if count == 0 {
		return "", nil
	}
	buf := make([]byte, count)
	_, err = io.ReadFull(r, buf)
	if err != nil {
		return "", err
	}
	return string(buf), nil
}

func WriteVarString(w io.Writer, s string) error {
	err := WriteVarInt(w, uint64(len(s)))
	if err != nil {
		return err
	}
	_, err = w.Write([]byte(s))
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

func ReadChainHash(r io.Reader) (*chainhash.Hash, error) {
	b := make([]byte, 32)
	_, err := io.ReadFull(r, b)
	if err != nil {
		return nil, err
	}
	return chainhash.NewHash(b)
}

func WriteChainHash(w io.Writer, h *chainhash.Hash) error {
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

func ReadSmallBlockHeader(r io.Reader) (SmallBlockHeader, error) {
	h := SmallBlockHeader{}
	var err error
	binary.Read(r, binary.LittleEndian, &h.Version)
	h.PreviousBlock, err = ReadChainHash(r)
	if err != nil {
		return h, err
	}
	binary.Read(r, binary.LittleEndian, &h.Timestamp)
	binary.Read(r, binary.LittleEndian, &h.Bits)
	binary.Read(r, binary.LittleEndian, &h.Nonce)
	return h, err
}

func WriteSmallBlockHeader(w io.Writer, h SmallBlockHeader) error {
	binary.Write(w, binary.LittleEndian, h.Version)
	WriteChainHash(w, h.PreviousBlock)
	binary.Write(w, binary.LittleEndian, h.Timestamp)
	binary.Write(w, binary.LittleEndian, h.Bits)
	binary.Write(w, binary.LittleEndian, h.Nonce)
	return nil
}

func ReadShareInfo(r io.Reader, segwit bool) (ShareInfo, error) {
	si := ShareInfo{}
	var err error
	si.ShareData, err = ReadShareData(r)
	if err != nil {
		return si, err
	}
	if segwit {
		si.SegwitData, err = ReadSegwitData(r)
		if err != nil {
			return si, err
		}
	}
	si.NewTransactionHashes, err = ReadChainHashList(r)
	return si, err
}

func WriteShareInfo(w io.Writer, si ShareInfo, segwit bool) error {
	// Placeholder
	return nil
}

func ReadShareData(r io.Reader) (ShareData, error) {
	sd := ShareData{}
	var err error
	sd.PreviousShareHash, err = ReadChainHash(r)
	if err != nil {
		return sd, err
	}
	sd.CoinBase, err = ReadVarString(r)
	if err != nil {
		return sd, err
	}
	binary.Read(r, binary.LittleEndian, &sd.Nonce)
	sd.PubKeyHash = make([]byte, 20)
	_, err = io.ReadFull(r, sd.PubKeyHash)
	if err != nil {
		return sd, err
	}
	binary.Read(r, binary.LittleEndian, &sd.PubKeyHashVersion)
	binary.Read(r, binary.LittleEndian, &sd.Subsidy)
	binary.Read(r, binary.LittleEndian, &sd.Donation)
	var staleInfoUint8 uint8
	binary.Read(r, binary.LittleEndian, &staleInfoUint8)
	sd.StaleInfo = StaleInfo(staleInfoUint8)
	sd.DesiredVersion, err = ReadVarInt(r)
	return sd, err
}

func ReadSegwitData(r io.Reader) (SegwitData, error) {
	sd := SegwitData{}
	var err error
	sd.TXIDMerkleLink, err = ReadChainHashList(r)
	if err != nil {
		return sd, err
	}
	sd.WTXIDMerkleRoot, err = ReadChainHash(r)
	return sd, err
}

func ReadHashLink(r io.Reader) (HashLink, error) {
	hl := HashLink{}
	var err error
	hl.State, err = ReadVarString(r)
	if err != nil {
		return hl, err
	}
	hl.Length, err = ReadVarInt(r)
	return hl, err
}

func WriteHashLink(w io.Writer, hl HashLink) error {
	err := WriteVarString(w, hl.State)
	if err != nil {
		return err
	}
	return WriteVarInt(w, hl.Length)
}

func ReadRef(r io.Reader) (Ref, error) {
	ref := Ref{}
	var err error
	ref.Identifier, err = ReadVarString(r)
	if err != nil {
		return ref, err
	}
	ref.ShareInfo, err = ReadShareInfo(r, false)
	return ref, err
}

func WriteRef(w io.Writer, ref Ref, segwit bool) error {
	// Placeholder
	return nil
}

func ReadShares(r io.Reader) ([]Share, error) {
	shares := make([]Share, 0)
	count, err := ReadVarInt(r)
	if err != nil {
		return shares, err
	}
	for i := uint64(0); i < count; i++ {
		s := Share{}
		s.Type, err = ReadVarInt(r)
		if err != nil {
			return shares, err
		}
		_, err := ReadVarInt(r)
		if err != nil {
			return shares, err
		}
		s.MinHeader, err = ReadSmallBlockHeader(r)
		if err != nil {
			return shares, err
		}
		s.ShareInfo, err = ReadShareInfo(r, s.Type >= 17)
		if err != nil {
			return shares, err
		}
		s.RefMerkleLink, err = ReadChainHashList(r)
		if err != nil {
			return shares, err
		}
		binary.Read(r, binary.LittleEndian, &s.LastTxOutNonce)
		s.HashLink, err = ReadHashLink(r)
		if err != nil {
			return shares, err
		}
		s.MerkleLink, err = ReadChainHashList(r)
		if err != nil {
			return shares, err
		}
		s.RefHash, _ = GetRefHash(p2pnet.ActiveNetwork, s.ShareInfo, s.RefMerkleLink, s.Type >= 17)
		var buf bytes.Buffer
		buf.Write(s.RefHash.CloneBytes())
		binary.Write(&buf, binary.LittleEndian, s.LastTxOutNonce)
		binary.Write(&buf, binary.LittleEndian, int32(0))
		s.GenTXHash, _ = CalcHashLink(s.HashLink, buf.Bytes(), GenTxBeforeRefHash)
		merkleLink := s.MerkleLink
		if s.Type >= 17 {
			merkleLink = s.ShareInfo.SegwitData.TXIDMerkleLink
		}
		s.MerkleRoot, _ = CalcMerkleLink(s.GenTXHash, merkleLink, 0)
		buf.Reset()
		hdr := btcwire.NewBlockHeader(s.MinHeader.Version, s.MinHeader.PreviousBlock, s.MerkleRoot, s.MinHeader.Bits, s.MinHeader.Nonce)
		hdr.Timestamp = time.Unix(int64(s.MinHeader.Timestamp), 0)
		hdr.Serialize(&buf)
		headerBytes := buf.Bytes()
		s.POWHash, _ = chainhash.NewHash(p2pnet.ActiveNetwork.POWHash(headerBytes[:]))
		s.Hash, _ = chainhash.NewHash(util.Sha256d(headerBytes[:]))
		shares = append(shares, s)
	}
	logging.Debugf("Successfully deserialized %d shares", len(shares))
	return shares, nil
}

func WriteShares(w io.Writer, shares []Share) error {
	// Placeholder
	return nil
}

func GetRefHash(n p2pnet.Network, si ShareInfo, refMerkleLink []*chainhash.Hash, segwit bool) (*chainhash.Hash, error) {
	// Placeholder
	return &chainhash.Hash{}, nil
}

func CalcMerkleLink(tip *chainhash.Hash, link []*chainhash.Hash, linkIndex int) (*chainhash.Hash, error) {
	// Placeholder
	return tip, nil
}

func CalcHashLink(hl HashLink, data []byte, ending []byte) (*chainhash.Hash, error) {
	// Placeholder
	return &chainhash.Hash{}, nil
}

