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

// buildCoinbaseMsgTx constructs the wire.MsgTx for the coinbase transaction.
func buildCoinbaseMsgTx(tmpl *BlockTemplate, payoutAddress string, extraNonce []byte, witnessCommitment []byte) (*wire.MsgTx, error) {
	// BIP34 requires minimal CScriptNum encoding for the block height.
	// btcd's txscript.NewScriptBuilder().AddInt64() handles this automatically.
	cbBuilder := txscript.NewScriptBuilder().AddInt64(tmpl.Height).AddData(extraNonce)
	if tmpl.CoinbaseAux.Flags != "" {
		if auxBytes, err := hex.DecodeString(tmpl.CoinbaseAux.Flags); err == nil && len(auxBytes) > 0 {
			cbBuilder.AddData(auxBytes)
		}
	}
	coinbaseScript, err := cbBuilder.Script()
	if err != nil {
		return nil, fmt.Errorf("failed to build coinbase scriptsig: %v", err)
	}

	var payoutScriptPubKey []byte
	if strings.HasPrefix(strings.ToLower(payoutAddress), "vtc1") || strings.HasPrefix(strings.ToLower(payoutAddress), "tvtc1") {
		hrp, decoded, err := bech32.Decode(payoutAddress)
		if err != nil {
			return nil, fmt.Errorf("failed to decode bech32 address '%s': %v", payoutAddress, err)
		}
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

	tx := wire.NewMsgTx(2)

	// Transaction Input
	txIn := wire.NewTxIn(&wire.OutPoint{Hash: chainhash.Hash{}, Index: 0xffffffff}, coinbaseScript, nil)
	tx.AddTxIn(txIn)

	// Transaction Outputs
	txOutPayout := wire.NewTxOut(tmpl.CoinbaseValue, payoutScriptPubKey)
	tx.AddTxOut(txOutPayout)

	// Add the SegWit witness commitment output if present
	if len(witnessCommitment) > 0 {
		commitmentHeader := []byte{0xaa, 0x21, 0xa9, 0xed}
		commitmentScriptPubKey := NewScriptBuilder().AddOp(txscript.OP_RETURN).AddData(append(commitmentHeader, witnessCommitment...)).Script()
		txOutCommitment := wire.NewTxOut(0, commitmentScriptPubKey)
		tx.AddTxOut(txOutCommitment)
	}

	// CRITICAL FIX: The witness reserved value MUST be 32 zero-bytes to match CalculateWitnessCommitment in segwit.go
	witnessNonce := make([]byte, 32)
	tx.TxIn[0].Witness = wire.TxWitness{witnessNonce}

	return tx, nil
}

// CreateCoinbaseTx constructs the full witness-serialized coinbase transaction for block submission.
func CreateCoinbaseTx(tmpl *BlockTemplate, payoutAddress string, extraNonce1, extraNonce2 string, witnessCommitment []byte) ([]byte, error) {
	extraNonce, err := hex.DecodeString(extraNonce1 + extraNonce2)
	if err != nil {
		return nil, fmt.Errorf("could not decode extranonce: %v", err)
	}

	tx, err := buildCoinbaseMsgTx(tmpl, payoutAddress, extraNonce, witnessCommitment)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := tx.Serialize(&buf); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// SplitCoinbaseForStratum builds the non-witness coinbase transaction and splits it around the
// 8-byte extranonce placeholder so Stratum miners receive the exact coinb1 and coinb2 needed
// to compute the real block Merkle root.
func SplitCoinbaseForStratum(tmpl *BlockTemplate, payoutAddress string, witnessCommitment []byte) (string, string, error) {
	// Use a unique 8-byte marker representing extraNonce1 (4 bytes) + extraNonce2 (4 bytes)
	marker := []byte{0xde, 0xad, 0xbe, 0xef, 0xca, 0xfe, 0xba, 0xbe}

	tx, err := buildCoinbaseMsgTx(tmpl, payoutAddress, marker, witnessCommitment)
	if err != nil {
		return "", "", err
	}

	// Stratum miners compute the coinbase TXID using the stripped (non-witness) transaction
	var buf bytes.Buffer
	if err := tx.SerializeNoWitness(&buf); err != nil {
		return "", "", err
	}

	rawBytes := buf.Bytes()
	idx := bytes.Index(rawBytes, marker)
	if idx == -1 {
		return "", "", fmt.Errorf("extranonce marker not found in serialized coinbase")
	}

	coinb1 := hex.EncodeToString(rawBytes[:idx])
	coinb2 := hex.EncodeToString(rawBytes[idx+len(marker):])
	return coinb1, coinb2, nil
}

// Simple script builder for output scripts
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
