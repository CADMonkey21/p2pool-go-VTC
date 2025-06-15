# p2pool-go-VTC

This is a Go-based implementation of the p2pool protocol, with initial support for Vertcoin (VTC). The goal is to create a modern, high-performance, and cross-platform p2pool node that is easy to set up and maintain.

### Current State: Alpha (Core Engine Complete)

The node is currently in a **functional alpha stage**. All core engine components are complete and working as demonstrated on the live network. The application can be compiled and run to create a functional mining pool that:
* Connects to `vertcoind` and receives work.
* Establishes and maintains stable P2P connections with other live nodes.
* Correctly handles `ping`/`pong` keep-alive messages.
* Runs a full Stratum server that accepts miners, serves them jobs, and dynamically adjusts their difficulty (Vardiff).
* **Performs full cryptographic validation of submitted shares and accepts valid work.**

The core engine is complete. The next phase of development will focus on implementing the sharechain logic by fully processing shares received from other peers and correctly serializing shares broadcast to the network.

## Prerequisites

1.  A running and fully synced `vertcoind` instance with RPC enabled in `vertcoin.conf`.
2.  The `verthash.dat` file. The pool will generate this on first run, but it's much faster to copy it from your `~/.vertcoin/` directory.
3.  An installation of the [Go language](https://go.dev/doc/install) (version 1.18 or newer).
4.  An open P2P port (default `9171` or `9346` depending on the p2pool network) on your router/firewall, forwarded to the machine running the node.

## Installation & Running

1.  **Clone the repository:**
    ```bash
    git clone [https://github.com/CADMonkey21/p2pool-go-VTC.git](https://github.com/CADMonkey21/p2pool-go-VTC.git)
    cd p2pool-go-VTC
    ```

2.  **Install dependencies:**
    ```bash
    go mod tidy
    ```

3.  **Configure the pool:**
    Copy the example configuration file and edit it with your personal settings.
    ```bash
    cp config.example.yaml config.yaml
    nano config.yaml
    ```

4.  **Run the pool:**
    ```bash
    go run .
    ```
    Point your Verthash-compatible miner to your pool's IP address on the Stratum port you configured (default is `9172`).

## Status

Here is a more detailed breakdown of the project's current status:

-   **P2P Layer**
    -   [x] Wire protocol implementation for P2P messages
    -   [x] Peer manager for connecting to peers
    -   [x] **Successful P2P Handshake with Live Nodes**
    -   [x] **Handles `ping`/`pong` messages**
    -   [ ] Process incoming `addrs` and `shares` messages
-   **RPC Client**
    -   [x] Connecting to a fullnode over RPC
    -   [x] Retrieve block template from fullnode
-   **Stratum Server & Share Logic**
    -   [x] Listen for and accept miner connections
    -   [x] Handle `mining.subscribe` and `mining.authorize` handshake
    -   [x] Send jobs (`mining.notify`) to authorized miners and broadcast new jobs
    -   [x] Implement Variable Difficulty (Vardiff) engine
    -   [x] **Full cryptographic validation of submitted shares**
-   **Next Steps**
    -   [ ] Implement full serialization for outgoing `shares` messages.
    -   [ ] Fully process received shares and build the sharechain.
    -   [ ] Compose and submit a block to `vertcoind` when a block-finding share is found.
    -   [ ] Web frontend for statistics.

## Contributing

Contributions are welcome! Please feel free to open an issue to discuss a bug or new feature, or submit a pull request with your improvements.

## Donate

If you want to support the development of this project, feel free to donate!

**Vertcoin:** `vtc1qx9wlulctjps59jnlcg04z3jwnkku5tgwkj0j0l`

