package work

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

// CreateCoinbaseTx constructs the special coinbase transaction for a block.
// It now correctly handles both Bech32 and legacy Base58 addresses.
func CreateCoinbaseTx(tmpl *BlockTemplate, payoutAddress string, extraNonce1, extraNonce2 string, witnessCommitment []byte) ([]byte, error) {
	extraNonce, err := hex.DecodeString(extraNonce1 + extraNonce2)
	if err != nil {
		return nil, fmt.Errorf("could not decode extranonce: %v", err)
	}

	heightBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(heightBytes, uint32(tmpl.Height))

	coinbaseScript := NewScriptBuilder().AddData(heightBytes).AddData(extraNonce).Script()

	var payoutScriptPubKey []byte
	// CORRECTED: Added check for "tvtc1" prefix
	if strings.HasPrefix(strings.ToLower(payoutAddress), "vtc1") || strings.HasPrefix(strings.ToLower(payoutAddress), "tvtc1") {
		hrp, decoded, err := bech32.Decode(payoutAddress)
		if err != nil {
			return nil, fmt.Errorf("failed to decode bech32 address '%s': %v", payoutAddress, err)
		}
		// CORRECTED: Added check for "tvtc" hrp
		if hrp != "vtc" && hrp != "tvtc" {
			return nil, fmt.Errorf("address is not a valid vertcoin bech32 address (hrp: %s)", hrp)
		}
		witnessVersion := decoded[0]
		witnessProgram, err := bech32.ConvertBits(decoded[1:], 5, 8, false)
		if err != nil {
			return nil, fmt.Errorf("failed to convert witness program for address '%s': %v", payoutAddress, err)
		}
		payoutScriptPubKey = NewScriptBuilder().AddOp(witnessVersion).AddData(witnessProgram).Script()
	} else {
		decoded := base58.Decode(payoutAddress)
		if len(decoded) < 5 {
			return nil, fmt.Errorf("invalid base58 address length for '%s'", payoutAddress)
		}
		pkh := decoded[1 : len(decoded)-4]
		payoutScriptPubKey = NewScriptBuilder().AddOp(txscript.OP_DUP).AddOp(txscript.OP_HASH160).AddData(pkh).AddOp(txscript.OP_EQUALVERIFY).AddOp(txscript.OP_CHECKSIG).Script()
	}

	// Build the transaction
	tx := wire.NewMsgTx(2) // Version 2 transaction

	// Transaction Input
	txIn := wire.NewTxIn(&wire.OutPoint{Hash: chainhash.Hash{}, Index: 0xffffffff}, coinbaseScript, nil)
	tx.AddTxIn(txIn)

	// Transaction Outputs
	txOutPayout := wire.NewTxOut(tmpl.CoinbaseValue, payoutScriptPubKey)
	tx.AddTxOut(txOutPayout)

	// Add the witness commitment output if there's a commitment to add
	if witnessCommitment != nil && len(witnessCommitment) > 0 {
		commitmentHeader := []byte{0xaa, 0x21, 0xa9, 0xed}
		commitmentScriptPubKey := NewScriptBuilder().AddOp(txscript.OP_RETURN).AddData(append(commitmentHeader, witnessCommitment...)).Script()
		txOutCommitment := wire.NewTxOut(0, commitmentScriptPubKey)
		tx.AddTxOut(txOutCommitment)
	}

	// Add witness data
	witnessNonceHash := chainhash.HashH(extraNonce)
	tx.TxIn[0].Witness = wire.TxWitness{witnessNonceHash[:]}

	var buf bytes.Buffer
	err = tx.Serialize(&buf)
	if err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// Simple script builder
type ScriptBuilder struct {
	script []byte
}

func NewScriptBuilder() *ScriptBuilder {
	return &ScriptBuilder{}
}
func (b *ScriptBuilder) AddData(data []byte) *ScriptBuilder {
	lenData := len(data)
	if lenData < txscript.OP_PUSHDATA1 {
		b.script = append(b.script, byte(lenData))
	} else if lenData <= 0xff {
		b.script = append(b.script, txscript.OP_PUSHDATA1, byte(lenData))
	} else if lenData <= 0xffff {
		b.script = append(b.script, txscript.OP_PUSHDATA2)
		buf := make([]byte, 2)
		binary.LittleEndian.PutUint16(buf, uint16(lenData))
		b.script = append(b.script, buf...)
	} else {
		b.script = append(b.script, txscript.OP_PUSHDATA4)
		buf := make([]byte, 4)
		binary.LittleEndian.PutUint32(buf, uint32(lenData))
		b.script = append(b.script, buf...)
	}
	b.script = append(b.script, data...)
	return b
}
func (b *ScriptBuilder) AddOp(op byte) *ScriptBuilder {
	b.script = append(b.script, op)
	return b
}
func (b *ScriptBuilder) Script() []byte {
	return b.script
}
