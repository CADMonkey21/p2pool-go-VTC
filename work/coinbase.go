package work

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/btcutil/bech32"
)

// CreateCoinbaseTx constructs the special coinbase transaction for a block.
func CreateCoinbaseTx(tmpl *BlockTemplate, payoutAddress string, extraNonce1, extraNonce2 string) ([]byte, error) {
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

	scriptPubKey := NewScriptBuilder().AddOp(witnessVersion).AddData(witnessProgram).Script()

	var txBuf bytes.Buffer
	txBuf.Write([]byte{1, 0, 0, 0})
	txBuf.WriteByte(1)
	txBuf.Write(make([]byte, 36))
	txBuf.WriteByte(byte(len(coinbaseScript)))
	txBuf.Write(coinbaseScript)
	txBuf.Write([]byte{0xff, 0xff, 0xff, 0xff})
	txBuf.WriteByte(1)
	binary.Write(&txBuf, binary.LittleEndian, tmpl.CoinbaseValue)
	txBuf.WriteByte(byte(len(scriptPubKey)))
	txBuf.Write(scriptPubKey)
	txBuf.Write([]byte{0, 0, 0, 0})

	return txBuf.Bytes(), nil
}

// Simple script builder
type ScriptBuilder struct {
	script []byte
}

func NewScriptBuilder() *ScriptBuilder {
	return &ScriptBuilder{}
}
func (b *ScriptBuilder) AddData(data []byte) *ScriptBuilder {
	b.script = append(b.script, byte(len(data)))
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

