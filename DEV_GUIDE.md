# p2pool-go-VTC: A Developer's Guide

This guide provides an architectural overview of `p2pool-go-VTC` for developers looking to understand its internals, contribute features, or adapt the codebase.

---

## High-Level Architecture

`p2pool-go-VTC` is a concurrent Go application that implements a decentralized, non-custodial peer-to-peer mining pool for Vertcoin. It interacts with three external systems:

1. **Other `p2pool-go-VTC` Nodes:** Uses `go-libp2p` (Kademlia DHT, mDNS, and GossipSub) to discover peers and broadcast validated mining shares across the mesh.
2. **A Local `vertcoind` Daemon:** Fetches SegWit block templates via JSON-RPC, performs live BIP22 candidate block consensus checks (`mode: "proposal"`), submits solved blocks (`submitblock`), and tracks block confirmations.
3. **Verthash Miners:** Accepts incoming miner connections over Stratum (`mining.subscribe`, `mining.authorize`, `mining.submit`), dynamically adjusts share difficulty (Vardiff), and distributes work containing direct PPLNS coinbase outputs.

---

## File and Directory Structure

```text
config/             # YAML configuration loader (config.yaml) and CLI flag overrides
logging/            # Color-coded console and file logging utilities
net/                # Vertcoin Mainnet/Testnet parameters and Verthash engine bindings
p2p/                # go-libp2p host, persistent identity (peer.key), mDNS, and GossipSub
rpc/                # JSON-RPC client for vertcoind (templates, BIP22 proposals, submitblock)
stratum/            # Stratum server, miner rate monitors, Vardiff loop, and job broadcaster
util/               # Endianness helpers and Merkle branch/root calculators
verthash/           # Verthash PoW hashing implementation backed by verthash.dat
web/                # HTTP dashboard API (/api/stats) and static frontend assets (web/static/)
work/               # Core share construction, PPLNS coinbase builder, ShareChain, and WorkManager
.gitignore          # Ignores compiled binaries, sharechain.json, peer.key, and runtime logs
README.md           # Project overview and setup instructions
DEV_GUIDE.md        # Developer architecture guide
PRE_SETUP_GUIDE.md  # Prerequisite installation guide (Go, vertcoind, verthash.dat, firewall)
LOG_GUIDE.md        # Guide to interpreting console log messages
config.example.yaml # Example configuration template
main.go             # Entry point, cmux TCP multiplexer, P2P share listener, and CLI stats logger
```

---

## Core Components Deep Dive

### 1. P2P Networking (`p2p/node.go` & `main.go`)

Instead of legacy Python P2Pool's custom 2011 TCP wire protocol, `p2pool-go-VTC` uses **`go-libp2p`**:
- **Persistent Identity:** `loadOrCreateIdentity()` generates an Ed25519 private key on first run and saves it to `peer.key` so the node's multiaddr (`/ip4/<IP>/tcp/9348/p2p/<PeerID>`) remains permanent across restarts.
- **Discovery:** Combines Kademlia DHT (`go-libp2p-kad-dht`) for WAN bootstrap peers configured in `config.yaml` with zero-config `mDNS` (`p2p/discovery/mdns`) for automatic local LAN peer discovery.
- **GossipSub Mesh:** Nodes subscribe to the `vtc-p2pool-shares-v1` topic. When a local miner submits a valid share, `StratumServer` broadcasts the serialized `work.Share` over GossipSub. Incoming peer shares are read from `p2pNode.IncomingShares`, cryptographically re-verified via `CalculateHashes()` and `IsValid()`, and added to `ShareChain`.

### 2. Daemon RPC & Consensus Verification (`rpc/` & `work/manager.go`)

- **`rpc/client.go`**: Lightweight HTTP Basic Auth JSON-RPC client (`req.Close = true` to avoid keep-alive EOF drops).
- **`rpc/blocks.go`**:
  - `GetBlockTemplate()`: Requests SegWit-enabled templates (`"rules": ["segwit"]`).
  - `ProposeBlock()`: Executes a BIP22 dry-run (`getblocktemplate` with `"mode": "proposal"`) so `vertcoind` runs `TestBlockValidity()` on the complete candidate block (header, witness coinbase, PPLNS outputs, BIP141 witness commitment, and transactions) while ignoring the PoW hash target.
  - `SubmitBlock()`: Sends a solved `wire.MsgBlock` to `vertcoind` via `submitblock` and inspects `resp.Result` to ensure `null` (accepted) is returned rather than a rejection reason string.

### 3. Direct Non-Custodial PPLNS Coinbase (`work/coinbase.go`)

- **`CalculatePPLNSOutputs()`**: Walks the current `ShareChain` tip back up to `PPLNSWindow` (`2160` shares), weights each share by its target difficulty, deducts the configured pool fee (`config.Active.Fee` payable to `config.Active.PoolAddress`), and deterministically sorts and allocates satoshis across all contributing miner addresses.
- **`buildCoinbaseMsgTx()`**: Builds a version-2 SegWit coinbase transaction containing the BIP34 block height, extranonce, all PPLNS + pool fee `TxOut` outputs, the `0xaa21a9ed` SegWit witness commitment `OP_RETURN` output, and the 32-byte zero witness nonce.
- **`SplitCoinbaseForStratum()` & `RebuildCoinbaseFromStratumParts()`**: Splits the non-witness serialized coinbase around an 8-byte extranonce marker into `coinb1` and `coinb2` for `mining.notify`. When a share is submitted, `RebuildCoinbaseFromStratumParts()` reconstructs the exact witness-enabled coinbase from the job's cached `Coinb1` and `Coinb2` so the Merkle root never drifts if new shares arrive while a job is active on the GPU.

### 4. Stratum Server & Vardiff (`stratum/`)

- **Byte-Order Formatting:** `formatStratumPrevHash()` word-swaps the 32-byte little-endian previous block hash into the exact 4-byte word order expected by `VerthashMiner` / `ccminer`.
- **Vardiff Engine:** `vardiffLoop()` monitors each client's `RateMonitor` and adjusts `mining.set_difficulty` to target `vardiff.targetTime` seconds per share, immediately rolling `CurTime` and dispatching a fresh `mining.notify` job so `VerthashMiner` applies the new difficulty without hashing duplicate nonces.

### 5. Single-Port Multiplexing & Web UI (`main.go` & `web/`)

- **`cmux`**: Multiplexes incoming TCP connections on `stratumPort` (`9172`)—routing HTTP `GET` requests to `httpSrv` (`web/dashboard.go` and `./web/static`) and raw JSON-RPC lines to `StratumServer`.
- **Multi-Rig Aggregation:** `web/dashboard.go` aggregates multiple connections sharing the same Vertcoin wallet address into a single combined row on the dashboard while tracking individual client rate monitors underneath.
