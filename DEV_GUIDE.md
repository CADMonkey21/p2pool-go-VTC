# p2pool-go-VTC: A Developer's Guide

This guide provides a deep dive into the architecture and codebase of `p2pool-go-VTC`. It's intended for developers who want to contribute to the project, understand its inner workings, or fork it for their own purposes.

---

## High-Level Architecture

The `p2pool-go-VTC` node is a Go application that implements a decentralized, peer-to-peer mining pool for Vertcoin. It communicates with three main external actors:

1. **Other p2pool nodes:** For discovering peers, sharing work (shares), and maintaining the decentralized nature of the pool.  
2. **A local `vertcoind` daemon:** For getting new block templates, submitting found blocks, and getting network information.  
3. **Miners:** Standard Verthash mining software connects to the node via the Stratum protocol to receive work and submit shares.  

The application is architected around a few key components that run as concurrent goroutines:

* **PeerManager:** Handles all P2P networking.  
* **WorkManager:** Manages the acquisition of block templates from `vertcoind` and orchestrates the creation of new work for miners.  
* **ShareChain:** A data structure that holds the recent history of all shares, which is used for PPLNS payouts.  
* **StratumServer:** Listens for and manages connections from miners.  
* **WebServer:** Provides a simple JSON API and web dashboard for monitoring the node's status.  

---

## File and Directory Structure

The repository is organized into several packages, each with a specific responsibility:

```
p2p/             # P2P networking logic
rpc/             # RPC client for vertcoind
stratum/         # Stratum server for miners
work/            # Core logic for shares, sharechain, and block templates
wire/            # P2P message definitions and serialization
web/             # Web dashboard UI
verthash/        # Verthash hashing implementation
logging/         # Color-coded logging utilities
config/          # Configuration loading and management
main.go          # Main application entry point
README.md
```

---

## Deep Dive into Core Components

### 1. P2P Layer (`p2p/` & `wire/`)

The P2P layer is responsible for the node's communication with other p2pool nodes on the network.

* **`p2p/peermanager.go`**: Manages a pool of connected peers, discovers new peers, handles incoming connections, and broadcasts messages (like new shares).  
* **`p2p/peer.go`**: Defines the `Peer` struct representing a single connection, handles handshake (`version`/`verack`), pings, and share chain synchronization.  
* **`wire/`**: Defines P2P messages (`MsgVersion`, `MsgShares`, `MsgGetShares`, etc.) and handles serialization/deserialization. `P2PoolConnection` wraps `net.Conn` using `gob` encoding/decoding.

### 2. RPC Client (`rpc/`)

Communicates with the local `vertcoind` daemon.

* **`rpc/client.go`**: Defines `Client` struct, handles JSON-RPC requests/responses for `getblocktemplate`, `submitblock`, `getmininginfo`, etc.  
* **`rpc/blocks.go`**: Contains structs for block data returned by RPC calls (`BlockInfo`, `BlockHeaderInfo`).

### 3. Stratum Server & Share Logic (`stratum/` & `work/`)

Handles miner interactions.

* **`stratum/server.go`**: Manages miners, handles Stratum handshake (`mining.subscribe`, `mining.authorize`), share submissions, and job broadcasting.  
* **`stratum/client.go`**: Represents a connected miner with worker info, difficulty, and hashrate tracked by `RateMonitor`.  
* **`work/share.go`**: `CreateShare` constructs a `p2pwire.Share` object from miner submissions, handling coinbase, witness commitments, and merkle links.  
* **`verthash/`**: Verthash PoW validation, implemented in `hasher_impl.go`, uses `verthash.dat`.

### 4. Share Chain & Payouts (`work/`)

Manages PPLNS payouts.

* **`work/sharechain.go`**: `ShareChain` is a linked list of `ChainShare` objects. Handles new shares, orphans, pool stats, and `GetProjectedPayouts` for expected payouts.  
* **`work/manager.go`**: `WorkManager` orchestrates fetching new block templates, submitting blocks, and processing payouts.

### 5. UI & Monitoring (`web/` & `logging/`)

* **`web/dashboard.go`**: HTTP server serving a single-page dashboard, periodically fetching stats from `/?json=1`.  
* **`logging/log.go`**: Color-coded logging (Debug, Info, Warn, Error, Success, Notice).

### 6. Persistence

Ensures share chain survives restarts.

* **`work/sharechain.go`**: `Commit` saves the chain to `sharechain.dat`, `Load` reconstructs it on startup.  
* **`wire/readwrite.go`**: `ReadShares` and `WriteShares` serialize/deserialize shares using `gob`.

---

## Future Improvements & Next Steps

* **Expanded Dashboard Features:** Add historical graphs for hashrate, efficiency, and payouts, likely requiring a lightweight database.  
* **Code Cleanup and Hardening:** Refactor, add unit tests, improve error handling.  
* **Configuration Reloading:** Support hot-reloading of config without restarting.  
* **Improved Peer Discovery:** Enhance network resilience with advanced peer discovery mechanisms.

---

I hope this guide is helpful for future developers.

