# p2pool-go-VTC


This is a **Go‑based** implementation of the P2Pool protocol with first‑class support for **Vertcoin (VTC)**.  
The aim is a modern, high‑performance, cross‑platform node that is simple to deploy on anything from a Raspberry Pi to a full server.

---

### Current State: Alpha (Stable P2P + Functional Mining)

The node is in a **functional alpha**. Recent work fixed the share‑chain resolver and the wire‑protocol edge cases, so a Go node can:

* handshake with legacy Python peers and stay connected,
* accept Verthash miners over Stratum (`9172`),
* fully validate miner shares and broadcast them to the network.

Shares from some legacy peers still fail a PoW test (target too easy) – investigation is ongoing – but the core loop is now solid.

---

## Prerequisites

1. A **synced `vertcoind`** with RPC enabled in `vertcoin.conf`.
2. The **`verthash.dat`** dataset (≈ 6 GB). Copy it from `~/.vertcoin/` or let the node download on first start.
3. **Go 1.18+** (Go 1.22 recommended).
4. An **open P2P port** on your router/​firewall – this fork defaults to **`19172`** – forwarded to the machine running the node.

---

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
    -   [x] Wire-protocol (version, ping, addrs, shares, get_shares)
    -   [x] Peer manager (dial, listen :19172, discovery)
    -   [x] Stable handshakes & keep‑alive
    -   [x] Outgoing and incoming shares (deserialization bug fixed)
    -   [ ] Some legacy shares fail PoW check (target mismatch).
-   **RPC Client**
    -   [x] Connect / auth to vertcoind
    -   [x] Fetch block templates & network stats
-   **Stratum Server & Share Logic**
    -   [x] subscribe / authorize handshake
    -   [x] Vardiff engine
    -   [x] Verthash PoW, target test & share creation
    -   [x] Broadcast miner shares to P2P
-   **Persistence**
    -   [x] On‑disk sharechain (shares.dat) with periodic autosave
-   **Next Steps**
    -   [ ] Investigate / filter “easy” legacy shares
    -   [ ] Implement payout splitter and block‑submission
    -   [ ] Add web dashboard for stats

## Contributing

Contributions are welcome! Please feel free to open an issue to discuss a bug or new feature, or submit a pull request with your improvements.

## Donate

If you want to support the development of this project, feel free to donate!

**Vertcoin:** `vtc1qx9wlulctjps59jnlcg04z3jwnkku5tgwkj0j0l`
