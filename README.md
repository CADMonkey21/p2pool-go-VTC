# p2pool-go-VTC

This is a modern, from-scratch implementation of a peer-to-peer (p2pool) mining pool for Vertcoin, written entirely in Go. It is designed for performance, stability, and ease of use, and is fully compatible with the existing Vertcoin p2pool network.

This project was built to be a functional, feature-complete, and high-performance replacement for older, Python-based p2pool implementations.

---

### Features

The node is in a **fully functional, feature-complete, and stable state**. All core logic for P2P networking, share processing, and payouts has been implemented and tested.

* **Full Stratum Server:** Allows any standard Verthash mining software to connect.
* **P2P Networking:** Connects to legacy Python peers and other nodes to form a decentralized mining network.
* **Live Web Dashboard:** A built-in web interface automatically refreshes every 5 seconds to display real-time pool and miner statistics in a clean, pretty-printed JSON format.
* **Color-Coded Logging:** Important events like finding blocks, receiving new work, and warnings are color-coded in the console for easy monitoring at a glance.
* **Automatic PPLNS Payouts:** Automatically calculates and distributes block rewards based on the Pay-Per-Last-N-Shares model.
* **Robust Variable Difficulty (Vardiff):** A stable and highly configurable vardiff engine automatically adjusts difficulty for miners of all sizes.
* **Daemon Integration:** Communicates directly with your Vertcoin daemon (`vertcoind`) for block templates, transaction submissions, and maturity checks.
* **Persistent Share Chain:** Remembers the share chain across restarts by saving to `shares.dat`, allowing the pool to resume where it left off.

---

## Prerequisites

1.  A **synced `vertcoind`** with RPC enabled in `vertcoin.conf`.
2.  The **`verthash.dat`** dataset. Copy it from `~/.vertcoin/` or let the node download on first start.
3.  **Go 1.18+** (Go 1.22 recommended).
4.  An **open P2P port** on your router/firewall (default is `9346`) forwarded to the machine running the node.

---

## Configuration & Ports

The pool is configured using a single `config.yaml` file. You can create your own by copying the provided `config.example.yaml`. Before running, you must correctly configure your ports and firewall.

| Port         | Setting in `config.yaml` | Default | Purpose                                                          | Firewall / Port Forwarding Action                                                              |
| :----------- | :----------------------- | :------ | :--------------------------------------------------------------- | :--------------------------------------------------------------------------------------------- |
| **P2P Port** | `p2pPort`                | `9346`  | For your node to talk to other p2pool nodes.                     | **Required:** Must be **port-forwarded** from your internet router to the machine running the node. |
| **Stratum Port** | `stratumPort`            | `9172`  | For your miners to connect to your node. Also serves the web UI. | **Allow on local firewall.** Only forward from your router if you want to run a public pool. |
| **RPC Port** | `rpcPort`                | `5888`  | For your node to talk to your local Vertcoin daemon.             | **No action needed.** This is a local connection and should **not** be exposed to the internet. |

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
    Point your Verthash-compatible miner to your pool's IP address on the Stratum port you configured (default is `9172`). The web dashboard will also be available at this address (e.g., `http://<your-ip>:9172`).

---

## Configuration

The `config.yaml` file allows you to configure all aspects of the pool.

* **RPC Connection:** These settings must match your `vertcoin.conf`.
* **Pool Settings:** `poolAddress` is the **most important setting**. This is your personal VTC address that will receive fee payouts and your own mining rewards. The `pplns_window` determines how many past shares are included in a payout calculation.
* **Vardiff Settings:** These allow you to fine-tune the automatic difficulty adjustment. The defaults are a stable starting point.
    * `targetTime`: The ideal time in seconds for a miner to find one share.
    * `retargetTime`: How often the algorithm checks the miner's performance. For stability, this should be 3-5x longer than `targetTime`.

## Status

Here is a more detailed breakdown of the project's current status:

-   **P2P Layer**
    -   [x] Wire-protocol (version, ping, addrs, shares, get_shares)
    -   [x] Peer manager (dial, listen, discovery)
    -   [x] Stable handshakes & keep-alive
-   **RPC Client**
    -   [x] Connect / auth to vertcoind
    -   [x] Fetch block templates & network stats
    -   [x] Block submission
-   **Stratum Server & Share Logic**
    -   [x] `subscribe` / `authorize` handshake
    -   [x] Vardiff engine (stable and configurable)
    -   [x] Verthash PoW, target validation & share creation
    -   [x] Block finding detection
    -   [x] Broadcast miner shares to P2P
-   **Share Chain & Payouts**
    -   [x] Correctly validate shares from local miners and network peers
    -   [x] Stable share chain management (linking, orphan handling)
    -   [x] PPLNS payout logic implemented
-   **UI & Monitoring**
    -   [x] Live-updating web dashboard for stats
    -   [x] Color-coded console logging for important events
-   **Persistence**
    -   [x] On-disk sharechain (`shares.dat`)
    -   [x] Fast loading of persisted share chain
-   **Next Steps**
    -   [ ] General code cleanup and further hardening
    -   [ ] Expanded dashboard features (e.g., historical graphs)
    -   [ ] Community-requested improvements

## Contributing

Contributions are welcome! Please feel free to open an issue to discuss a bug or new feature, or submit a pull request with your improvements.

## Donate

If you want to support the development of this project, feel free to donate!

**Vertcoin:** `vtc1qx9wlulctjps59jnlcg04z3jwnkku5tgwkj0j0l`
