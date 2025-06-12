# p2pool-go-VTC

This is a Go-based implementation of the p2pool protocol, with initial support for Vertcoin (VTC). The goal is to create a modern, high-performance, and cross-platform p2pool node that is easy to set up and maintain.

### Current State: Alpha (Functional)

The node is currently in a **functional alpha stage**. The core P2P, RPC, and Stratum functionalities are working together. The application can be compiled and run to create a functional mining pool.

**What's Working:**
* Connects to `vertcoind` and continuously fetches block templates.
* Runs a full Stratum server that accepts connections from miners.
* Handles the `mining.subscribe` and `mining.authorize` handshake.
* Serves mining jobs to miners based on live network data.
* Receives share submissions from miners.
* **Performs full cryptographic validation of submitted shares against the job difficulty.**

The primary missing feature is the broadcasting of these valid shares to the P2P network, which is the final step to make this a profitable, collaborative pool.

## Prerequisites

1.  A running and fully synced `vertcoind` instance with RPC enabled in `vertcoin.conf`.
2.  The `verthash.dat` file. The pool will generate this on first run, but it's much faster to copy it from your `~/.vertcoin/` directory.
3.  An installation of the [Go language](https://go.dev/doc/install) (version 1.18 or newer).

## Installation & Running

1.  **Clone the repository:**
    ```bash
    git clone [https://github.com/CADMonkey21/p2pool-go-VTC.git](https://github.com/CADMonkey21/p2pool-go-VTC.git)
    cd p2pool-go-VTC
    ```

2.  **Install dependencies:**
    This command will find and download all the necessary libraries.
    ```bash
    go mod tidy
    ```

3.  **Configure the pool:**
    Copy the example configuration file and edit it with your personal settings (RPC credentials, payout address, etc.).
    ```bash
    cp config.example.yaml config.yaml
    nano config.yaml
    ```

4.  **Run the pool:**
    ```bash
    go run .
    ```
    You can now point your Verthash-compatible miner to your pool's IP address on the Stratum port you configured (default is `9172`).

## Status

Here is a more detailed breakdown of the project's current status:

- **P2P Layer**
    - [x] Wire protocol implementation for P2P messages
    - [x] Peer manager for connecting to seed nodes and maintaining connections
    - [x] Correctly handle P2P protocol version `3501`
    - [x] Process `addrs` messages to discover new peers
    - [ ] Retrieving and processing the sharechain from peers
- **RPC Client**
    - [x] Connecting to a fullnode over RPC
    - [x] Retrieve block template from fullnode (`getblocktemplate`)
- **Stratum Server & Share Logic**
    - [x] Listen for and accept miner connections
    - [x] Handle `mining.subscribe` and `mining.authorize` handshake
    - [x] Send jobs (`mining.notify`) to authorized miners
    - [x] Receive share submissions (`mining.submit`) from miners
    - [x] **Validate submitted shares (Full cryptographic verification)**
- **Next Steps**
    - [ ] Add accepted shares to the local sharechain
    - [ ] Submit valid shares to the P2P network
    - [ ] Compose and submit a block to `vertcoind` when a block-finding share is found
    - [ ] Web frontend for statistics

## Contributing

Contributions are welcome! Please feel free to open an issue to discuss a bug or new feature, or submit a pull request with your improvements.

## Donate

If you want to support the development of this project, feel free to donate!

**Vertcoin:** `vtc1qx9wlulctjps59jnlcg04z3jwnkku5tgwkj0j0l`
