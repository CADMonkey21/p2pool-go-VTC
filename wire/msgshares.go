package wire

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"math/big"

	"github.com/btcsuite/btcd/blockchain"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/gertjaap/p2pool-go/logging"
)

var _ P2PoolMessage = &MsgShares{}

// Share represents a P2Pool share object, mapping directly to Python's p2pool.data.Share.
type Share struct {
	Type uint64 // Corresponds to BaseShare.VERSION or NewShare.VERSION

	MinHeader      SmallBlockHeader
	ShareInfo      ShareInfo
	RefMerkleLink  MerkleLink // Renamed from [] *chainhash.Hash for clarity in Go
	LastTxOutNonce uint64
	HashLink       HashLink // State and Length
	MerkleLink     MerkleLink // Renamed from [] *chainhash.Hash for clarity in Go
	// Fields below are derived from MinHeader/ShareInfo after deserialization
	// or are for internal use, not directly serialized in the top-level Share contents
	Hash        *chainhash.Hash // Share hash (derived from header_hash)
	POWHash     *chainhash.Hash // Proof-of-Work hash (derived from header)
	GenTXHash   *chainhash.Hash // Generated transaction hash (derived from hash_link)
	MerkleRoot  *chainhash.Hash // Merkle root from header (derived from ref_merkle_link/merkle_link)
	RefHash     *chainhash.Hash // The hash of the ref_type (share_info + identifier)

	// Additional internal fields from Python's Share object that are not part of the serialized payload directly
	// These would need to be computed or set separately if necessary for internal logic.
	// E.g., time_seen, new_script, previous_hash (from ShareInfo.PreviousShareHash)
}

// MerkleLink represents the Merkle branch and index for a Merkle tree.
// Python: pack.ComposedType([('branch', pack.ListType(pack.IntType(256))), ('index', pack.IntType(0))])
type MerkleLink struct {
	Branch []*chainhash.Hash
	Index  uint64 // Always 0 in Python's implementation, but kept for strict mapping
}

// HashLink represents the hash_link structure used in shares.
// Python: hash_link_type = pack.ComposedType([('state', pack.FixedStrType(32)), ('extra_data', pack.FixedStrType(0)), ('length', pack.VarIntType())])
type HashLink struct {
	State     []byte // 32 bytes
	ExtraData []byte // 0 bytes in current Python impl (bit of a hack)
	Length    uint64 // Length in bits
}

// SmallBlockHeader represents the minimal block header information included in a share.
// Python: small_block_header_type
type SmallBlockHeader struct {
	Version       int32 // pack.VarIntType(), but often fixed size in CGo/Python
	PreviousBlock *chainhash.Hash
	Timestamp     uint32 // pack.IntType(32)
	Bits          uint32 // bitcoin_data.FloatingIntegerType()
	Nonce         uint32 // pack.IntType(32)
}

// ShareInfo contains detailed information about the share.
// Python: share_info_type
type ShareInfo struct {
	ShareData            ShareData
	SegwitData           *SegwitData // Optional, only if segwit_activated
	NewTransactionHashes []*chainhash.Hash
	TransactionHashRefs  []TransactionHashRef
	FarShareHash         *chainhash.Hash // pack.PossiblyNoneType(0, pack.IntType(256))
	MaxBits              uint32          // bitcoin_data.FloatingIntegerType()
	Bits                 uint32          // bitcoin_data.FloatingIntegerType()
	Timestamp            int32           // pack.IntType(32)
	AbsHeight            int32           // pack.IntType(32)
	AbsWork              *big.Int        // pack.IntType(128)
}

// SegwitData contains segwit-specific information for a share.
// Python: segwit_data (conditional part of share_info_type)
type SegwitData struct {
	TXIDMerkleLink  MerkleLink // pack.ComposedType for MerkleLink
	WTXIDMerkleRoot *chainhash.Hash
}

// TransactionHashRef is a pair of share_count and tx_count used for transaction references.
// Python: pairs in pack.ListType(pack.VarIntType(), 2)
type TransactionHashRef struct {
	ShareCount uint64
	TxCount    uint64
}

// ShareData contains core data for the share.
// Python: share_data (nested within share_info_type)
type ShareData struct {
	PreviousShareHash *chainhash.Hash // pack.PossiblyNoneType(0, pack.IntType(256))
	CoinBase          []byte          // pack.VarStrType() - raw bytes, not hex string
	Nonce             uint32          // pack.IntType(32)
	PubKeyHash        []byte          // pack.IntType(160) - 20 bytes
	PubKeyHashVersion uint8           // pack.IntType(8)
	Subsidy           uint64          // pack.IntType(64)
	Donation          uint16          // pack.IntType(16)
	StaleInfo         StaleInfo       // pack.StaleInfoEnumType()
	DesiredVersion    uint64          // pack.VarIntType()
}

// StaleInfo represents the staleness status of a share.
// Python: stale_pack_to_unpack = {0: None, 253: 'orphan', 254: 'doa'}
type StaleInfo uint8

const (
	StaleInfoNone   = StaleInfo(0)
	StaleInfoOrphan = StaleInfo(253)
	StaleInfoDOA    = StaleInfo(254)
	// Add other 'unkX' values as needed if they are explicitly sent by peers
)

// MsgShares is the P2Pool message for sending shares.
type MsgShares struct {
	Shares []Share
}

// IsValid checks if the share's PoW hash meets its target.
func (s *Share) IsValid() bool {
	if s.POWHash == nil || s.ShareInfo.Bits == 0 {
		return false
	}
	target := blockchain.CompactToBig(s.ShareInfo.Bits)
	bnHash := blockchain.HashToBig(s.POWHash)
	return bnHash.Cmp(target) <= 0
}

// ToBytes serializes the Share object into a byte slice.
// This implements the `contents` portion of the top-level Share message.
func (s *Share) ToBytes() ([]byte, error) {
	var buf bytes.Buffer
	var err error

	// min_header (SmallBlockHeader)
	// Python: ('version', pack.VarIntType()),
	err = WriteVarInt(&buf, uint64(s.MinHeader.Version))
	if err != nil { return nil, err }
	// Python: ('previous_block', pack.PossiblyNoneType(0, pack.IntType(256))),
	// Python: (0 means None, IntType(256) is 32 bytes)
	var zeroHash chainhash.Hash // All zeros for None representation
	err = WritePossiblyNoneHash(&buf, s.MinHeader.PreviousBlock, &zeroHash)
	if err != nil { return nil, err }
	// Python: ('timestamp', pack.IntType(32)),
	err = binary.Write(&buf, binary.LittleEndian, s.MinHeader.Timestamp)
	if err != nil { return nil, err }
	// Python: ('bits', bitcoin_data.FloatingIntegerType()),
	err = WriteFloatingInteger(&buf, s.MinHeader.Bits)
	if err != nil { return nil, err }
	// Python: ('nonce', pack.IntType(32)),
	err = binary.Write(&buf, binary.LittleEndian, s.MinHeader.Nonce)
	if err != nil { return nil, err }

	// share_info (ShareInfo)
	// share_data (ShareData)
	// Python: ('previous_share_hash', pack.PossiblyNoneType(0, pack.IntType(256))),
	err = WritePossiblyNoneHash(&buf, s.ShareInfo.ShareData.PreviousShareHash, &zeroHash)
	if err != nil { return nil, err }
	// Python: ('coinbase', pack.VarStrType()),
	err = WriteVarString(&buf, s.ShareInfo.ShareData.CoinBase)
	if err != nil { return nil, err }
	// Python: ('nonce', pack.IntType(32)),
	err = binary.Write(&buf, binary.LittleEndian, s.ShareInfo.ShareData.Nonce)
	if err != nil { return nil, err }
	// Python: ('pubkey_hash', pack.IntType(160)), (20 bytes)
	err = WriteFixedBytes(&buf, s.ShareInfo.ShareData.PubKeyHash, 20) // Added length 20
	if err != nil { return nil, err }
	// Python: ('pubkey_hash_version', pack.IntType(8)), (1 byte)
	err = binary.Write(&buf, binary.LittleEndian, s.ShareInfo.ShareData.PubKeyHashVersion)
	if err != nil { return nil, err }
	// Python: ('subsidy', pack.IntType(64)),
	err = binary.Write(&buf, binary.LittleEndian, s.ShareInfo.ShareData.Subsidy)
	if err != nil { return nil, err }
	// Python: ('donation', pack.IntType(16)),
	err = binary.Write(&buf, binary.LittleEndian, s.ShareInfo.ShareData.Donation)
	if err != nil { return nil, err }
	// Python: ('stale_info', pack.StaleInfoEnumType()),
	err = WriteStaleInfo(&buf, s.ShareInfo.ShareData.StaleInfo)
	if err != nil { return nil, err }
	// Python: ('desired_version', pack.VarIntType()),
	err = WriteVarInt(&buf, s.ShareInfo.ShareData.DesiredVersion)
	if err != nil { return nil, err }

	// SegwitData (conditional)
	// Python: ([segwit_data] if is_segwit_activated(cls.VERSION, net) else [])
	// For now, assuming Version 16 and above implies segwit active based on Python's logic
	if s.Type >= 16 { // Share Type 16 is SEGWIT_ACTIVATION_VERSION in Python
		// Python's 'PossiblyNoneType' for segwit_data is complex:
		// dict(txid_merkle_link=dict(branch=[], index=0), wtxid_merkle_root=2**256-1)
		// We'll write the "None equivalent" values if SegwitData is nil.
		if s.ShareInfo.SegwitData != nil {
			// txid_merkle_link
			err = WriteChainHashList(&buf, s.ShareInfo.SegwitData.TXIDMerkleLink.Branch)
			if err != nil { return nil, err }
			// Python: Index is IntType(0) - writes ZERO bytes!
			// (Removed: err = binary.Write(&buf, binary.LittleEndian, uint64(s.ShareInfo.SegwitData.TXIDMerkleLink.Index)))
			// wtxid_merkle_root
			err = WriteChainHash(&buf, s.ShareInfo.SegwitData.WTXIDMerkleRoot)
			if err != nil { return nil, err }
		} else {
			// Write the specific "None equivalent" values as per Python
			err = WriteVarInt(&buf, 0) // Empty branch for TXIDMerkleLink (VarInt for length 0)
			if err != nil { return nil, err }
			// Python: Index is IntType(0) - writes ZERO bytes!
			// (Removed: err = binary.Write(&buf, binary.LittleEndian, uint64(0)))
			
			noneWTXIDMerkleRoot, _ := chainhash.NewHashFromStr("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
			err = WriteChainHash(&buf, noneWTXIDMerkleRoot) // 2^256-1 for WTXIDMerkleRoot
			if err != nil { return nil, err }
		}
	}


	// new_transaction_hashes
	// Python: ('new_transaction_hashes', pack.ListType(pack.IntType(256))),
	err = WriteChainHashList(&buf, s.ShareInfo.NewTransactionHashes)
	if err != nil { return nil, err }
	// transaction_hash_refs
	// Python: ('transaction_hash_refs', pack.ListType(pack.VarIntType(), 2)), # pairs of share_count, tx_count
	err = WriteTransactionHashRefs(&buf, s.ShareInfo.TransactionHashRefs)
	if err != nil { return nil, err }
	// far_share_hash
	// Python: ('far_share_hash', pack.PossiblyNoneType(0, pack.IntType(256))),
	err = WritePossiblyNoneHash(&buf, s.ShareInfo.FarShareHash, &zeroHash)
	if err != nil { return nil, err }
	// max_bits
	// Python: ('max_bits', bitcoin_data.FloatingIntegerType()),
	err = WriteFloatingInteger(&buf, s.ShareInfo.MaxBits)
	if err != nil { return nil, err }
	// bits
	// Python: ('bits', bitcoin_data.FloatingIntegerType()),
	err = WriteFloatingInteger(&buf, s.ShareInfo.Bits)
	if err != nil { return nil, err }
	// timestamp
	// Python: ('timestamp', pack.IntType(32)),
	err = binary.Write(&buf, binary.LittleEndian, s.ShareInfo.Timestamp)
	if err != nil { return nil, err }
	// absheight
	// Python: ('absheight', pack.IntType(32)),
	err = binary.Write(&buf, binary.LittleEndian, s.ShareInfo.AbsHeight)
	if err != nil { return nil, err }
	// abswork
	// Python: ('abswork', pack.IntType(128)),
	err = WriteInt(&buf, s.ShareInfo.AbsWork, 128, binary.LittleEndian)
	if err != nil { return nil, err }

	// ref_merkle_link
	// Python: ('ref_merkle_link', pack.ComposedType([('branch', pack.ListType(pack.IntType(256))), ('index', pack.IntType(0))])),
	err = WriteChainHashList(&buf, s.RefMerkleLink.Branch)
	if err != nil { return nil, err }
	// Python: Index is IntType(0) - writes ZERO bytes!
	// (Removed: err = binary.Write(&buf, binary.LittleEndian, uint64(s.RefMerkleLink.Index)))

	// last_txout_nonce
	// Python: ('last_txout_nonce', pack.IntType(64)),
	err = binary.Write(&buf, binary.LittleEndian, s.LastTxOutNonce)
	if err != nil { return nil, err }

	// hash_link
	// Python: ('hash_link', hash_link_type),
	// hash_link_type = pack.ComposedType([('state', pack.FixedStrType(32)), ('extra_data', pack.FixedStrType(0)), ('length', pack.VarIntType())])
	err = WriteFixedBytes(&buf, s.HashLink.State, 32) // Added length 32
	if err != nil { return nil, err }
	err = WriteFixedBytes(&buf, s.HashLink.ExtraData, 0) // Added length 0
	if err != nil { return nil, err }
	err = WriteVarInt(&buf, s.HashLink.Length)
	if err != nil { return nil, err }

	// merkle_link
	// Python: ('merkle_link', pack.ComposedType([('branch', pack.ListType(pack.IntType(256))), ('index', pack.IntType(0))])),
	err = WriteChainHashList(&buf, s.MerkleLink.Branch)
	if err != nil { return nil, err }
	// Python: Index is IntType(0) - writes ZERO bytes!
	// (Removed: err = binary.Write(&buf, binary.LittleEndian, uint64(s.MerkleLink.Index)))

	return buf.Bytes(), nil
}

// FromBytes deserializes the Share object from a byte slice.
// This implements the `contents` portion of the top-level Share message.
func (s *Share) FromBytes(b []byte) error {
	r := bytes.NewReader(b)
	var err error

	// min_header (SmallBlockHeader)
	// Python: ('version', pack.VarIntType()),
	version, err := ReadVarInt(r)
	if err != nil { return err }
	s.MinHeader.Version = int32(version)
	// Python: ('previous_block', pack.PossiblyNoneType(0, pack.IntType(256))),
	// Python: (0 means None, IntType(256) is 32 bytes)
	var zeroHash chainhash.Hash // All zeros for None representation
	s.MinHeader.PreviousBlock, err = ReadPossiblyNoneHash(r, &zeroHash)
	if err != nil { return err }
	// Python: ('timestamp', pack.IntType(32)),
	err = binary.Read(r, binary.LittleEndian, &s.MinHeader.Timestamp)
	if err != nil { return err }
	// Python: ('bits', bitcoin_data.FloatingIntegerType()),
	s.MinHeader.Bits, err = ReadFloatingInteger(r)
	if err != nil { return err }
	// Python: ('nonce', pack.IntType(32)),
	err = binary.Read(r, binary.LittleEndian, &s.MinHeader.Nonce)
	if err != nil { return err }

	// share_info (ShareInfo)
	// share_data (ShareData)
	// Python: ('previous_share_hash', pack.PossiblyNoneType(0, pack.IntType(256))),
	s.ShareInfo.ShareData.PreviousShareHash, err = ReadPossiblyNoneHash(r, &zeroHash)
	if err != nil { return err }
	// Python: ('coinbase', pack.VarStrType()),
	s.ShareInfo.ShareData.CoinBase, err = ReadVarString(r)
	if err != nil { return err }
	// Python: ('nonce', pack.IntType(32)),
	err = binary.Read(r, binary.LittleEndian, &s.ShareInfo.ShareData.Nonce)
	if err != nil { return err }
	// Python: ('pubkey_hash', pack.IntType(160)), (20 bytes)
	s.ShareInfo.ShareData.PubKeyHash, err = ReadFixedBytes(r, 20)
	if err != nil { return err }
	// Python: ('pubkey_hash_version', pack.IntType(8)), (1 byte)
	err = binary.Read(r, binary.LittleEndian, &s.ShareInfo.ShareData.PubKeyHashVersion)
	if err != nil { return err }
	// Python: ('subsidy', pack.IntType(64)),
	err = binary.Read(r, binary.LittleEndian, &s.ShareInfo.ShareData.Subsidy)
	if err != nil { return err }
	// Python: ('donation', pack.IntType(16)),
	err = binary.Read(r, binary.LittleEndian, &s.ShareInfo.ShareData.Donation)
	if err != nil { return err }
	// Python: ('stale_info', pack.StaleInfoEnumType()),
	s.ShareInfo.ShareData.StaleInfo, err = ReadStaleInfo(r)
	if err != nil { return err }
	// Python: ('desired_version', pack.VarIntType()),
	s.ShareInfo.ShareData.DesiredVersion, err = ReadVarInt(r)
	if err != nil { return err }

	// SegwitData (conditional)
	// Python: ([segwit_data] if is_segwit_activated(cls.VERSION, net) else [])
	// For now, assuming Version 16 and above implies segwit active based on Python's logic
	if s.Type >= 16 { // Share Type 16 is SEGWIT_ACTIVATION_VERSION in Python
		// Python's 'PossiblyNoneType' for segwit_data is complex, it's not a simple None-or-value.
		// It has a specific dict value for "None'.
		var currentSegwitData SegwitData
		currentSegwitData.TXIDMerkleLink.Branch, err = ReadChainHashList(r)
		if err != nil { return err }
		
		// Python: Index is IntType(0) - reads ZERO bytes!
		currentSegwitData.TXIDMerkleLink.Index = 0 // Assign 0, do not read bytes
		
		currentSegwitData.WTXIDMerkleRoot, err = ReadChainHash(r)
		if err != nil { return err }

		noneWTXIDMerkleRoot, _ := chainhash.NewHashFromStr("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")

		// Check if the read data matches the Python's "None" equivalent for SegwitData
		if len(currentSegwitData.TXIDMerkleLink.Branch) == 0 &&
		   currentSegwitData.TXIDMerkleLink.Index == 0 &&
		   currentSegwitData.WTXIDMerkleRoot.IsEqual(noneWTXIDMerkleRoot) {
			s.ShareInfo.SegwitData = nil // Interpret as Python's None
		} else {
			s.ShareInfo.SegwitData = &currentSegwitData
		}
	}

	// new_transaction_hashes
	// Python: ('new_transaction_hashes', pack.ListType(pack.IntType(256))),
	s.ShareInfo.NewTransactionHashes, err = ReadChainHashList(r)
	if err != nil { return err }
	// transaction_hash_refs
	// Python: ('transaction_hash_refs', pack.ListType(pack.VarIntType(), 2)), # pairs of share_count, tx_count
	s.ShareInfo.TransactionHashRefs, err = ReadTransactionHashRefs(r)
	if err != nil { return err }
	// far_share_hash
	// Python: ('far_share_hash', pack.PossiblyNoneType(0, pack.IntType(256))),
	s.ShareInfo.FarShareHash, err = ReadPossiblyNoneHash(r, &zeroHash)
	if err != nil { return err }
	// max_bits
	// Python: ('max_bits', bitcoin_data.FloatingIntegerType()),
	s.ShareInfo.MaxBits, err = ReadFloatingInteger(r)
	if err != nil { return err }
	// bits
	// Python: ('bits', bitcoin_data.FloatingIntegerType()),
	s.ShareInfo.Bits, err = ReadFloatingInteger(r)
	if err != nil { return err }
	// timestamp
	// Python: ('timestamp', pack.IntType(32)),
	err = binary.Read(r, binary.LittleEndian, &s.ShareInfo.Timestamp)
	if err != nil { return err }
	// absheight
	// Python: ('absheight', pack.IntType(32)),
	err = binary.Read(r, binary.LittleEndian, &s.ShareInfo.AbsHeight)
	if err != nil { return err }
	// abswork
	// Python: ('abswork', pack.IntType(128)),
	s.ShareInfo.AbsWork, err = ReadInt(r, 128, binary.LittleEndian)
	if err != nil { return err }

	// ref_merkle_link
	// Python: ('ref_merkle_link', pack.ComposedType([('branch', pack.ListType(pack.IntType(256))), ('index', pack.IntType(0))])),
	s.RefMerkleLink.Branch, err = ReadChainHashList(r)
	if err != nil { return err }
	// Python: Index is IntType(0) - reads ZERO bytes!
	s.RefMerkleLink.Index = 0 // Assign 0, do not read bytes

	// last_txout_nonce
	// Python: ('last_txout_nonce', pack.IntType(64)),
	err = binary.Read(r, binary.LittleEndian, &s.LastTxOutNonce)
	if err != nil { return err }

	// hash_link
	// Python: ('hash_link', hash_link_type),
	// hash_link_type = pack.ComposedType([('state', pack.FixedStrType(32)), ('extra_data', pack.FixedStrType(0)), ('length', pack.VarIntType())])
	s.HashLink.State, err = ReadFixedBytes(r, 32)
	if err != nil { return err }
	s.HashLink.ExtraData, err = ReadFixedBytes(r, 0) // Will be empty
	if err != nil { return err }
	s.HashLink.Length, err = ReadVarInt(r)
	if err != nil { return err }

	// merkle_link
	// Python: ('merkle_link', pack.ComposedType([('branch', pack.ListType(pack.IntType(256))), ('index', pack.IntType(0))])),
	s.MerkleLink.Branch, err = ReadChainHashList(r)
	if err != nil { return err }
	// Python: Index is IntType(0) - reads ZERO bytes!
	s.MerkleLink.Index = 0 // Assign 0, do not read bytes

	return nil
}


// MsgShares (The P2PoolMessage implementation for shares)
func (m *MsgShares) FromBytes(b []byte) error {
	r := bytes.NewReader(b)
	var err error
	m.Shares, err = ReadShares(r) // Uses the new ReadShares helper
	if err != nil {
		logging.Errorf("Failed to deserialize shares message: %v", err)
	}
	return err
}

func (m *MsgShares) ToBytes() ([]byte, error) {
	var buf bytes.Buffer
	err := WriteShares(&buf, m.Shares) // Uses the new WriteShares helper
	return buf.Bytes(), err
}

func (m *MsgShares) Command() string {
	return "shares"
}

// init function sets up global constants (like DonationScript and GenTxBeforeRefHash)
// as defined in Python's p2pool/data.py
func init() {
	// Corresponds to DONATION_SCRIPT in p2pool/data.py
	// '410418a74130b2f4fad899d8ed2bff272bc43a03c8ca72897ae3da584d7a770b5a9ea8dd1b37a620d27c6cf6d5a7a9bbd6872f5981e95816d701d94f201c5d093be6ac'.decode('hex')
	var err error
	DonationScript, err = hex.DecodeString("410418a74130b2f4fad899d8ed2bff272bc43a03c8ca72897ae3da584d7a770b5a9ea8dd1b37a620d27c6cf6d5a7a9bbd6872f5981e95816d701d94f201c5d093be6ac")
	if err != nil {
		panic(err) // Should not happen with a valid hex string
	}

	// Corresponds to gentx_before_refhash in p2pool/data.py
	// pack.VarStrType().pack(DONATION_SCRIPT) + pack.IntType(64).pack(0) + pack.VarStrType().pack('\x6a\x28' + pack.IntType(256).pack(0) + pack.IntType(64).pack(0))[:3]
	// This is essentially DONATION_SCRIPT (varint length + data) + 8 zero bytes + 0x6a 0x28 0x00
	genTxBeforeRefHashBuf := new(bytes.Buffer)
	WriteVarString(genTxBeforeRefHashBuf, DonationScript)
	binary.Write(genTxBeforeRefHashBuf, binary.LittleEndian, uint64(0)) // pack.IntType(64).pack(0)
	genTxBeforeRefHashBuf.Write([]byte{0x6a, 0x28, 0x00}) // First 3 bytes of '\x6a\x28' + pack.IntType(256).pack(0) + pack.IntType(64).pack(0)
	GenTxBeforeRefHash = genTxBeforeRefHashBuf.Bytes()

	// Ensure DonationScript and GenTxBeforeRefHash are not nil for consistency
	if DonationScript == nil {
		DonationScript = []byte{}
	}
	if GenTxBeforeRefHash == nil {
		GenTxBeforeRefHash = []byte{}
	}
}

// Globals used by the init function
var DonationScript []byte
var GenTxBeforeRefHash []byte
