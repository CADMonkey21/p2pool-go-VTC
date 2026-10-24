package p2p

import (
	"encoding/json"
	"io"

	"github.com/klauspost/compress/zstd"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// The 1.1.0 protocols carry the same JSON as their 1.0.0 versions, zstd-compressed
// in each direction. Dialers offer both and peers that predate them get 1.0.0.
const SyncZProtocolID = protocol.ID("/p2pool/sync/1.1.0")
const ShareReqZProtocolID = protocol.ID("/p2pool/sharereq/1.1.0")

// maxZstdPayload caps the decompressed size of a zstd stream payload, about four times
// the ~16 MB of a full 10000-share sync window.
const maxZstdPayload = 64 << 20

var zstdProtocols = map[protocol.ID]bool{SyncZProtocolID: true, ShareReqZProtocolID: true}

var zstdEncoder, _ = zstd.NewWriter(nil)

// readJSON decodes one JSON value from s, decompressing it on a zstd protocol.
func readJSON(s network.Stream, v any) error {
	return decodeJSON(s, zstdProtocols[s.Protocol()], v)
}

func decodeJSON(r io.Reader, z bool, v any) error {
	if z {
		zr, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(maxZstdPayload))
		if err != nil {
			return err
		}
		defer zr.Close()
		r = io.LimitReader(zr, maxZstdPayload)
	} else {
		r = io.LimitReader(r, maxZstdPayload)
	}
	return json.NewDecoder(r).Decode(v)
}

// writeJSON encodes v to s, compressing it on a zstd protocol.
func writeJSON(s network.Stream, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writePayload(s, append(b, '\n'))
}

// writePayload writes encoded JSON to s, compressing it on a zstd protocol.
func writePayload(s network.Stream, b []byte) error {
	_, err := s.Write(encodePayload(b, zstdProtocols[s.Protocol()]))
	return err
}

func encodePayload(b []byte, z bool) []byte {
	if z {
		return zstdEncoder.EncodeAll(b, nil)
	}
	return b
}
