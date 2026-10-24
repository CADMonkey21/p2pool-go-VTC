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
.gitignore          # Ignores compiled binaries, sharechain.db, sharechain.json, peer.key, and runtime logs
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
- **P2P Stream Protocols (`libp2p`):**
  - `/p2pool/sync/1.0.0`: Hydrates the contiguous verified ShareChain on startup.
  - `/p2pool/sharereq/1.0.0`: Requests missing parent ancestor shares by hash for topological verification.
  - `/p2pool/sync/1.1.0` & `/p2pool/sharereq/1.1.0`: The same JSON, zstd-compressed in each direction (decompressed size capped at 64 MiB). Dialers offer 1.1.0 first and fall back to 1.0.0 for older peers; both versions stay registered.
  - `/p2pool/template/1.0.0` & `/p2pool/blockrelay/1.0.0`: Pushes/serves `BlockTemplate` JSON to `-light` nodes and relays solved `wire.MsgBlock` hex back to full-node peers.
- **GossipSub Mesh:** Nodes subscribe to the `vtc-p2pool-shares-v1` topic. When a local miner submits a valid share, `StratumServer` broadcasts the serialized `work.Share` over GossipSub. Incoming peer shares are read from `p2pNode.IncomingShares`, cryptographically re-verified via `CalculateHashes()` and `IsValid()`, and added to `ShareChain`.
- **Share Admission:** A GossipSub topic validator drops shares that fail checks needing no sharechain state before they are delivered or forwarded: timestamps more than 2 hours ahead of the local clock or older than the 72-hour retention window (ignored, since they depend on our clock), a declared target easier than `MinPoolShareDiff`, or a failed `IsValid()`. `ShareChain.AddShares` applies the same time and target checks to synced shares. Every stream message is decoded through `decodeLimited` with a per-protocol size cap.
- **Share Timestamps (consensus):** Version and payout rules gate on the later of a share's timestamp and its parent's, so a share cannot skip them by backdating itself. From `ConsensusV3ActivationTime` (parent timestamp), a share's timestamp must not be earlier than the median of its last 11 non-stale ancestors. The PPLNS window is cut by timestamp, so these bounds stop one share from shrinking or capturing it.

### 2. Daemon RPC & Consensus Verification (`rpc/` & `work/manager.go`)

- **`rpc/client.go`**: Lightweight HTTP Basic Auth JSON-RPC client (`req.Close = true` to avoid keep-alive EOF drops).
- **`rpc/blocks.go`**:
  - `GetBlockTemplate()`: Requests SegWit-enabled templates (`"rules": ["segwit"]`).
  - `ProposeBlock()`: Executes a BIP22 dry-run (`getblocktemplate` with `"mode": "proposal"`) so `vertcoind` runs `TestBlockValidity()` on the complete candidate block (header, witness coinbase, PPLNS outputs, BIP141 witness commitment, and transactions) while ignoring the PoW hash target.
  - `SubmitBlock()`: Sends a solved `wire.MsgBlock` to `vertcoind` via `submitblock` and inspects `resp.Result` to ensure `null` (accepted) is returned rather than a rejection reason string.

### 3. Direct Non-Custodial PPLNS Coinbase (`work/coinbase.go`)

- **`CalculatePPLNSOutputs()`**: Walks the current `ShareChain` tip back up to `PPLNSWindow` (`8640` shares / `36h` work-capped window), weights each share by its target difficulty, and deterministically sorts and allocates satoshis across all contributing miner addresses.
- **Payout addresses:** Shares must carry a standard payout (P2WPKH, P2WSH, P2TR, P2PKH or P2SH); others are rejected by `IsValid()`. Segwit addresses are decoded per BIP 173/350. From `TaprootPayoutActivationTime`, by share timestamp, witness v1 (Taproot) payouts get their real `OP_1 <32 bytes>` script; before it, nodes encoded them as base58 and paid an unspendable script, so Stratum refuses Taproot addresses until then. The pool fee is not paid: every node must compute the same outputs, so the split pays miners only.
- **`buildCoinbaseMsgTx()`**: Builds a version-2 SegWit coinbase transaction containing the BIP34 block height, extranonce, all PPLNS `TxOut` outputs, the `0xaa21a9ed` SegWit witness commitment `OP_RETURN` output, the `"P2PV"` `OP_RETURN` share metadata commitment (`ShareVersionV2 = 24`), and the 32-byte zero witness nonce.
- **`SplitCoinbaseForStratum()` & `RebuildCoinbaseFromStratumParts()`**: Splits the non-witness serialized coinbase around an 8-byte extranonce marker into `coinb1` and `coinb2` for `mining.notify`. When a share is submitted, `RebuildCoinbaseFromStratumParts()` reconstructs the exact witness-enabled coinbase from the job's cached `Coinb1` and `Coinb2` so the Merkle root never drifts if new shares arrive while a job is active on the GPU.

### 4. Stratum Server & Vardiff (`stratum/`)

- **Byte-Order Formatting:** `formatStratumPrevHash()` word-swaps the 32-byte little-endian previous block hash into the exact 4-byte word order expected by `VerthashMiner` / `ccminer`.
- **Vardiff Engine:** `vardiffLoop()` monitors each client's `RateMonitor` and adjusts `mining.set_difficulty` to target `vardiff.targetTime` seconds per share, immediately rolling `CurTime` and dispatching a fresh `mining.notify` job so `VerthashMiner` applies the new difficulty without hashing duplicate nonces.

### 5. Single-Port Multiplexing & Web UI (`main.go` & `web/`)

- **`cmux`**: Multiplexes incoming TCP connections on `stratumPort` (`9172`)—routing HTTP `GET` requests to `httpSrv` (`web/dashboard.go` and `./web/static`) and raw JSON-RPC lines to `StratumServer`.
- **Multi-Rig Aggregation:** `web/dashboard.go` aggregates multiple connections sharing the same Vertcoin wallet address into a single combined row on the dashboard while tracking individual client rate monitors underneath.
- **Sharechain Persistence & Export (`work/sharestore.go`):** Persists verified shares incrementally in a `bbolt` database (`sharechain.db`), automatically migrating any legacy `sharechain.json` once on startup (renaming it to `sharechain.json.migrated`). Verified shares can be exported as JSON anytime via `-dump-shares` or the loopback-only `/api/shares?limit=N` HTTP endpoint.

### Reconnect Vardiff & Difficulty Hints (`stratum/server.go`)
- **In-Memory Reconnect Cache**: `StratumServer` caches each authorized worker's latest difficulty (`host|worker` and `worker`) for 30 minutes (`reconnectDiffTTL`) whenever Vardiff updates or a client disconnects.
- **ShareChain Cold-Start Estimation**: If a node restarts (clearing the in-memory cache), `estimateDiffFromShareChain` inspects the last 30 minutes of verified shares in `ShareChain` for the authorizing payout address and computes an appropriate starting difficulty for `target_time`.
- **Miner Hint Override**: `parseAuthorizeUserAndHint` supports explicit starting difficulty hints via `+<diff>` on the username (`vtc1...+0.5`) or `d=<diff>` / `diff=<diff>` / `sd=<diff>` in the Stratum password field, clamped to `[min_diff, pool_share_diff]`.
