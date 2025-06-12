# p2pool-go-VTC

This is a Go-based implementation of the p2pool protocol, with initial support for Vertcoin (VTC). The goal is to create a modern, high-performance, and cross-platform p2pool node that is easy to set up and maintain.

### Current State: Alpha

The node is currently in an **alpha stage**. The core P2P, RPC, and Stratum functionalities are working. You can compile and run the node, connect it to `vertcoind`, and have a miner connect to it. The pool will serve jobs to the miner, and the miner will be able to submit shares back.

**The primary missing feature is the cryptographic validation of submitted shares and broadcasting them to the P2P network.**

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
- **Stratum Server**
    - [x] Listen for and accept miner connections
    - [x] Handle `mining.subscribe` and `mining.authorize` handshake
    - [x] Send jobs (`mining.notify`) to authorized miners
    - [x] Receive share submissions (`mining.submit`) from miners
- **Share & Block Logic**
    - [ ] Validate submitted shares (cryptographic verification)
    - [ ] Submit valid shares to the P2P network
    * [ ] Compose and submit a block to `vertcoind` when a block-finding share is found
- **Frontend**
    - [ ] Web frontend for statistics

## Contributing

Contributions are welcome! Please feel free to open an issue to discuss a bug or new feature, or submit a pull request with your improvements.

## Donate

If you want to support the development of this project, feel free to donate!

**Vertcoin:** `vtc1qx9wlulctjps59jnlcg04z3jwnkku5tgwkj0j0l`
