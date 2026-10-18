package work

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/CADMonkey21/p2pool-go-VTC/config"
	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

type PayoutOutput struct {
	Address string
	Amount  int64
}

// AddressToScriptPubKey converts a Vertcoin Bech32 or Base58 address into a locking scriptPubKey.
func AddressToScriptPubKey(payoutAddress string) ([]byte, error) {
	payoutAddress = strings.TrimSpace(payoutAddress)
	if strings.HasPrefix(strings.ToLower(payoutAddress), "vtc1") || strings.HasPrefix(strings.ToLower(payoutAddress), "tvtc1") {
		hrp, decoded, err := bech32.Decode(strings.ToLower(payoutAddress))
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
		return NewScriptBuilder().AddOp(witnessVersion).AddData(witnessProgram).Script(), nil
	}

	decoded := base58.Decode(payoutAddress)
	if len(decoded) < 5 {
		return nil, fmt.Errorf("invalid base58 address length for '%s'", payoutAddress)
	}
	version := decoded[0]
	pkh := decoded[1 : len(decoded)-4]
	if version == 0x05 || version == 0xc4 {
		// P2SH scriptPubKey: OP_HASH160 <20-byte scriptHash> OP_EQUAL
		return NewScriptBuilder().AddOp(txscript.OP_HASH160).AddData(pkh).AddOp(txscript.OP_EQUAL).Script(), nil
	}
	// P2PKH scriptPubKey: OP_DUP OP_HASH160 <20-byte PubKeyHash> OP_EQUALVERIFY OP_CHECKSIG
	return NewScriptBuilder().AddOp(txscript.OP_DUP).AddOp(txscript.OP_HASH160).AddData(pkh).AddOp(txscript.OP_EQUALVERIFY).AddOp(txscript.OP_CHECKSIG).Script(), nil
}

// CalculatePPLNSOutputs computes the deterministic coinbase outputs for the current PPLNS sharechain window,
// matching legacy p2pool-vtc's direct generation transaction payout model.
func CalculatePPLNSOutputs(totalReward int64, defaultMinerAddr string, sc *ShareChain) []PayoutOutput {
	if totalReward <= 0 {
		return nil
	}

	feePct := config.Active.Fee / 100.0
	if feePct < 0 {
		feePct = 0
	} else if feePct > 1.0 {
		feePct = 1.0
	}

	poolAddr := strings.TrimSpace(config.Active.PoolAddress)
	if _, err := AddressToScriptPubKey(poolAddr); err != nil {
		poolAddr = ""
	}

	var poolFee int64
	if poolAddr != "" && feePct > 0 {
		poolFee = int64(float64(totalReward) * feePct)
	}
	distributable := totalReward - poolFee

	weights := make(map[string]*big.Int)
	totalWeight := new(big.Int)

	if sc != nil {
		tipHash := sc.GetTipHash()
		windowSize := config.Active.PPLNSWindow
		if windowSize <= 0 {
			windowSize = 2160
		}
		if tipHash != nil {
			shares, err := sc.GetSharesForPayout(tipHash, windowSize)
			if err == nil && len(shares) > 0 {
				baseDiff1, _ := new(big.Int).SetString("000000FFFF000000000000000000000000000000000000000000000000000000", 16)
				scaledMaxTarget := new(big.Int).Mul(baseDiff1, big.NewInt(1000000))

				for _, s := range shares {
					if s == nil || len(s.ShareInfo.ShareData.PubKeyHash) == 0 || s.Target == nil || s.Target.Sign() <= 0 {
						continue
					}
					var addr string
					if s.ShareInfo.ShareData.PubKeyHashVersion == 0x00 {
						converted, err := bech32.ConvertBits(s.ShareInfo.ShareData.PubKeyHash, 8, 5, true)
						if err != nil {
							continue
						}
						hrp := "vtc"
						if config.Active.Testnet {
							hrp = "tvtc"
						}
						addr, _ = bech32.Encode(hrp, append([]byte{s.ShareInfo.ShareData.PubKeyHashVersion}, converted...))
					} else {
						addr = base58.CheckEncode(s.ShareInfo.ShareData.PubKeyHash, s.ShareInfo.ShareData.PubKeyHashVersion)
					}
					if addr == "" {
						continue
					}

					w := new(big.Int).Div(new(big.Int).Set(scaledMaxTarget), s.Target)
					if w.Sign() <= 0 {
						w = big.NewInt(1)
					}
					if _, exists := weights[addr]; !exists {
						weights[addr] = new(big.Int)
					}
					weights[addr].Add(weights[addr], w)
					totalWeight.Add(totalWeight, w)
				}
			}
		}
	}

	amounts := make(map[string]int64)
	if totalWeight.Sign() > 0 && len(weights) > 0 {
		addrs := make([]string, 0, len(weights))
		for addr := range weights {
			addrs = append(addrs, addr)
		}
		sort.Strings(addrs)

		var allocated int64
		for i, addr := range addrs {
			if i == len(addrs)-1 {
				rem := distributable - allocated
				if rem > 0 {
					amounts[addr] += rem
				}
			} else {
				amtBig := new(big.Int).Mul(big.NewInt(distributable), weights[addr])
				amtBig.Div(amtBig, totalWeight)
				amt := amtBig.Int64()
				if amt > 0 {
					amounts[addr] += amt
					allocated += amt
				}
			}
		}
	} else {
		amounts[defaultMinerAddr] = distributable
	}

	if poolAddr != "" && poolFee > 0 {
		amounts[poolAddr] += poolFee
	}

	sortedAddrs := make([]string, 0, len(amounts))
	for addr := range amounts {
		sortedAddrs = append(sortedAddrs, addr)
	}
	sort.Strings(sortedAddrs)

	outputs := make([]PayoutOutput, 0, len(sortedAddrs))
	for _, addr := range sortedAddrs {
		if amounts[addr] > 0 {
			outputs = append(outputs, PayoutOutput{
				Address: addr,
				Amount:  amounts[addr],
			})
		}
	}
	return outputs
}

// buildCoinbaseMsgTx constructs the wire.MsgTx for the coinbase transaction.
func buildCoinbaseMsgTx(tmpl *BlockTemplate, payoutAddress string, extraNonce []byte, witnessCommitment []byte, sc *ShareChain) (*wire.MsgTx, error) {
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

	tx := wire.NewMsgTx(2)

	txIn := wire.NewTxIn(&wire.OutPoint{Hash: chainhash.Hash{}, Index: 0xffffffff}, coinbaseScript, nil)
	tx.AddTxIn(txIn)

	payoutOutputs := CalculatePPLNSOutputs(tmpl.CoinbaseValue, payoutAddress, sc)
	for _, out := range payoutOutputs {
		scriptPubKey, err := AddressToScriptPubKey(out.Address)
		if err != nil {
			return nil, err
		}
		tx.AddTxOut(wire.NewTxOut(out.Amount, scriptPubKey))
	}

	if len(witnessCommitment) > 0 {
		commitmentHeader := []byte{0xaa, 0x21, 0xa9, 0xed}
		commitmentScriptPubKey := NewScriptBuilder().AddOp(txscript.OP_RETURN).AddData(append(commitmentHeader, witnessCommitment...)).Script()
		txOutCommitment := wire.NewTxOut(0, commitmentScriptPubKey)
		tx.AddTxOut(txOutCommitment)
	}

	witnessNonce := make([]byte, 32)
	tx.TxIn[0].Witness = wire.TxWitness{witnessNonce}

	return tx, nil
}

// RebuildCoinbaseFromStratumParts reconstructs the exact witness-serialized coinbase transaction
// from the job's coinb1, extraNonce1, extraNonce2, and coinb2 so that it is 100% identical
// to what the miner hashed even if new shares arrived in the ShareChain while the job was active.
func RebuildCoinbaseFromStratumParts(coinb1, extraNonce1, extraNonce2, coinb2 string) ([]byte, error) {
	strippedBytes, err := hex.DecodeString(coinb1 + extraNonce1 + extraNonce2 + coinb2)
	if err != nil {
		return nil, fmt.Errorf("failed to decode stratum coinbase parts: %v", err)
	}

	var tx wire.MsgTx
	if err := tx.DeserializeNoWitness(bytes.NewReader(strippedBytes)); err != nil {
		return nil, fmt.Errorf("failed to deserialize stripped coinbase: %v", err)
	}

	if len(tx.TxIn) > 0 {
		tx.TxIn[0].Witness = wire.TxWitness{make([]byte, 32)}
	}

	var buf bytes.Buffer
	if err := tx.Serialize(&buf); err != nil {
		return nil, fmt.Errorf("failed to serialize witness coinbase: %v", err)
	}

	return buf.Bytes(), nil
}

// CreateCoinbaseTx constructs the full witness-serialized coinbase transaction for block submission.
func CreateCoinbaseTx(tmpl *BlockTemplate, payoutAddress string, extraNonce1, extraNonce2 string, witnessCommitment []byte, sc *ShareChain) ([]byte, error) {
	extraNonce, err := hex.DecodeString(extraNonce1 + extraNonce2)
	if err != nil {
		return nil, fmt.Errorf("could not decode extranonce: %v", err)
	}

	tx, err := buildCoinbaseMsgTx(tmpl, payoutAddress, extraNonce, witnessCommitment, sc)
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
func SplitCoinbaseForStratum(tmpl *BlockTemplate, payoutAddress string, witnessCommitment []byte, sc *ShareChain) (string, string, error) {
	marker := []byte{0xde, 0xad, 0xbe, 0xef, 0xca, 0xfe, 0xba, 0xbe}

	tx, err := buildCoinbaseMsgTx(tmpl, payoutAddress, marker, witnessCommitment, sc)
	if err != nil {
		return "", "", err
	}

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
