package work

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript" // CORRECTED: Use txscript for opcodes
	"github.com/btcsuite/btcd/wire"
)

// CreateCoinbaseTx constructs the special coinbase transaction for a block.
// It now includes the witness commitment hash for SegWit compliance.
func CreateCoinbaseTx(tmpl *BlockTemplate, payoutAddress string, extraNonce1, extraNonce2 string, witnessCommitment []byte) ([]byte, error) {
	extraNonce, err := hex.DecodeString(extraNonce1 + extraNonce2)
	if err != nil {
		return nil, fmt.Errorf("could not decode extranonce: %v", err)
	}

	heightBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(heightBytes, uint32(tmpl.Height))

	coinbaseScript := NewScriptBuilder().AddData(heightBytes).AddData(extraNonce).Script()

	hrp, decoded, err := bech32.Decode(payoutAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to decode bech32 address '%s': %v", payoutAddress, err)
	}
	if hrp != "vtc" {
		return nil, fmt.Errorf("address is not a valid vertcoin bech32 address (hrp: %s)", hrp)
	}

	witnessVersion := decoded[0]
	witnessProgram := decoded[1:]

	payoutScriptPubKey := NewScriptBuilder().AddOp(witnessVersion).AddData(witnessProgram).Script()

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
		// Create the witness commitment output script as per BIP141
		commitmentHeader := []byte{0xaa, 0x21, 0xa9, 0xed}
		commitmentScriptPubKey := NewScriptBuilder().AddOp(txscript.OP_RETURN).AddData(append(commitmentHeader, witnessCommitment...)).Script() // CORRECTED
		txOutCommitment := wire.NewTxOut(0, commitmentScriptPubKey)
		tx.AddTxOut(txOutCommitment)
	}

	// Add witness data
	// The witness nonce is the 32-byte hash of the extranonce
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
	if lenData < txscript.OP_PUSHDATA1 { // CORRECTED
		b.script = append(b.script, byte(lenData))
	} else if lenData <= 0xff {
		b.script = append(b.script, txscript.OP_PUSHDATA1, byte(lenData)) // CORRECTED
	} else if lenData <= 0xffff {
		b.script = append(b.script, txscript.OP_PUSHDATA2) // CORRECTED
		buf := make([]byte, 2)
		binary.LittleEndian.PutUint16(buf, uint16(lenData))
		b.script = append(b.script, buf...)
	} else {
		b.script = append(b.script, txscript.OP_PUSHDATA4) // CORRECTED
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
