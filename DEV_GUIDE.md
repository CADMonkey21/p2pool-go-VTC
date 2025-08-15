# p2pool-go-VTC: A Developer's Guide

This guide provides a deep dive into the architecture and codebase of `p2pool-go-VTC`. It's intended for developers who want to contribute to the project, understand its inner workings, or fork it for their own purposes.

---

## High-Level Architecture

The `p2pool-go-VTC` node is a Go application that implements a decentralized, peer-to-peer mining pool for Vertcoin. It communicates with three main external actors:

1.  **Other p2pool nodes:** For discovering peers, sharing work (shares), and maintaining the decentralized nature of the pool.
2.  **A local `vertcoind` daemon:** For getting new block templates, submitting found blocks, and getting network information.
3.  **Miners:** Standard Verthash mining software connects to the node via the Stratum protocol to receive work and submit shares.

The application is architected around a few key components that run as concurrent goroutines:

* **PeerManager:** Handles all P2P networking.
* **WorkManager:** Manages the acquisition of block templates from `vertcoind` and orchestrates the creation of new work for miners.
* **ShareChain:** A data structure that holds the recent history of all shares, which is used for PPLNS payouts.
* **StratumServer:** Listens for and manages connections from miners.
* **WebServer:** Provides a simple JSON API and web dashboard for monitoring the node's status.

---

## File and Directory Structure

The repository is organized into several packages, each with a specific responsibility:

├── p2p/             # P2P networking logic├── rpc/             # RPC client for vertcoind├── stratum/         # Stratum server for miners├── work/            # Core logic for shares, sharechain, and block templates├── wire/            # P2P message definitions and serialization├── web/             # Web dashboard UI├── verthash/        # Verthash hashing implementation├── logging/         # Color-coded logging utilities├── config/          # Configuration loading and management├── main.go          # Main application entry point└── README.md
---

## Deep Dive into Core Components

### 1. P2P Layer (`p2p/` & `wire/`)

The P2P layer is responsible for the node's communication with other p2pool nodes on the network.

* **`p2p/peermanager.go`**: This is the heart of the P2P system. It manages a pool of connected peers, discovers new peers, and handles incoming connections. It also contains the `peerConnectorLoop` which periodically tries to connect to new peers if the number of active connections is low. The `PeerManager` is also responsible for broadcasting messages to all connected peers, such as new shares that your miners have found.
* **`p2p/peer.go`**: This file defines the `Peer` struct, which represents a single connection to another p2pool node. It handles the initial handshake process, which involves exchanging `version` and `verack` messages. Each `Peer` has its own goroutines for monitoring the connection, sending periodic pings, and handling the initial synchronization of the share chain.
* **`wire/`**: This package contains the definitions for all the P2P messages, such as `MsgVersion`, `MsgShares`, `MsgGetShares`, etc. It also handles the serialization and deserialization of these messages for sending over the network. The `P2PoolConnection` struct in `wire/conn.go` is a wrapper around a `net.Conn` that uses `gob` for encoding and decoding messages.

### 2. RPC Client (`rpc/`)

The RPC client is a fairly straightforward component that communicates with the local `vertcoind` daemon.

* **`rpc/client.go`**: This file defines the `Client` struct, which handles the creation of JSON-RPC requests and the parsing of responses. It includes methods for all the necessary RPC calls, such as `getblocktemplate`, `submitblock`, and `getmininginfo`.
* **`rpc/blocks.go`**: This contains the structs for the data returned by `getblock` and `getblocktemplate`, such as `BlockInfo` and `BlockHeaderInfo`.

### 3. Stratum Server & Share Logic (`stratum/` & `work/`)

This is where the interaction with miners happens.

* **`stratum/server.go`**: The `StratumServer` struct manages all connected miners. It listens for new connections and handles the Stratum protocol handshake (`mining.subscribe`, `mining.authorize`) and share submissions (`mining.submit`). It also runs a `jobBroadcaster` goroutine that sends new work to all authorized miners whenever a new block template is received from the `WorkManager`.
* **`stratum/client.go`**: The `Client` struct represents a single connected miner. It contains information about the miner's worker name, difficulty, and hashrate, which is tracked by the `RateMonitor`.
* **`work/share.go`**: This file contains the crucial `CreateShare` function, which takes a valid submission from a miner and constructs a `p2pwire.Share` object. This involves creating the coinbase transaction, calculating the witness commitment for SegWit, and building the merkle links.
* **`verthash/`**: The Verthash implementation is used to validate the proof-of-work for each share submitted by miners. The `hasher_impl.go` file provides the main implementation, which uses a pre-generated `verthash.dat` file.

### 4. Share Chain & Payouts (`work/`)

The share chain is the backbone of the PPLNS (Pay-Per-Last-N-Shares) system.

* **`work/sharechain.go`**: The `ShareChain` struct is a linked list of `ChainShare` objects that stores the most recent shares. It has methods for adding new shares, resolving orphaned shares (shares whose parents haven't been seen yet), and calculating pool statistics. The `GetProjectedPayouts` function is where the PPLNS logic is implemented, calculating the expected payout for each miner based on their contributions to the current PPLNS window.
* **`work/manager.go`**: The `WorkManager` orchestrates the entire process. Its `WatchBlockTemplate` goroutine periodically fetches new work from `vertcoind`. When a block is found, the `SubmitBlock` function is called, and a `PayoutBlock` is added to the `PendingBlocks` list. The `WatchMaturedBlocks` goroutine periodically checks the status of pending blocks and triggers the `ProcessPayout` function when a block has reached 100 confirmations.

### 5. UI & Monitoring (`web/` & `logging/`)

* **`web/dashboard.go`**: This file contains the logic for the web dashboard. It's a simple HTTP server that serves a single HTML page with a JavaScript frontend that periodically fetches a JSON endpoint (`/?json=1`) to update the stats. The `DashboardStats` struct defines the structure of the JSON response, which includes a wide range of statistics about the pool, network, and connected miners.
* **`logging/log.go`**: This is a simple logging package that provides color-coded log levels (Debug, Info, Warn, Error, Success, Notice) for easy monitoring of the node's status in the console.

### 6. Persistence

To ensure that the pool can be restarted without losing its history, the share chain is persisted to disk.

* **`work/sharechain.go`**: The `Commit` method in `sharechain.go` is responsible for saving the current share chain to a file named `sharechain.dat`. The `Load` method is called on startup to read the shares from this file and reconstruct the share chain in memory.
* **`wire/readwrite.go`**: The actual serialization is handled by the `ReadShares` and `WriteShares` functions, which use the `gob` package to encode and decode the slice of `Share` structs.

---

## Future Improvements & Next Steps

Based on the `README.md` and the current state of the code, here are some potential areas for future development:

* **Expanded Dashboard Features:** The current dashboard is functional but could be enhanced with historical graphs for hashrate, efficiency, and payouts. This would likely involve adding a lightweight database to store historical stats.
* **Code Cleanup and Hardening:** As with any beta project, there's always room for refactoring, adding more unit tests, and improving error handling throughout the codebase.
* **Configuration Reloading:** Currently, the configuration is only loaded on startup. A nice feature would be to allow for hot-reloading of the configuration file without restarting the node.
* **Improved Peer Discovery:** The current peer discovery mechanism is based on a list of seed hosts and the `addrs` message. More advanced peer discovery mechanisms could be implemented to make the network more resilient.

I hope this guide is helpful for future developers.

