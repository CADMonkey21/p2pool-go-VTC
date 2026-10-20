# p2pool-go-VTC

This is a modern, from-scratch implementation of a peer-to-peer (p2pool) mining pool for Vertcoin, written entirely in Go. It is designed for high performance, network stability, and zero-custody administration.

This node has been **live-verified on Vertcoin Mainnet**—successfully validating real mining shares across a multi-node WAN mesh, verifying block templates via BIP22 proposal checks against `vertcoind`, solving Mainnet blocks, and paying out miners directly inside the block's coinbase transaction.

---

## Official Public Seed & Node (`p2poolvtc.com`)

An official cloud-hosted bootstrap seed node and public dashboard is live at:
- **Live Dashboard:** [http://p2poolvtc.com:9172](http://p2poolvtc.com:9172)
- **Public Stratum Endpoint:** `stratum+tcp://p2poolvtc.com:9172`
- **Bootstrap P2P Multiaddrs** (pre-configured in `config.example.yaml`):
  - `/dns4/p2poolvtc.com/tcp/9348/p2p/12D3KooWNiQ2TSXEhFgZrsJuuk9xxNheph8b23pK4RHaPSQFNDwU`
  - `/ip4/138.68.53.157/tcp/9348/p2p/12D3KooWNiQ2TSXEhFgZrsJuuk9xxNheph8b23pK4RHaPSQFNDwU`

---

## Features

- **Daemon-less `-light` Mode (One-Click Miner Ready):** Run `./p2pool-go-vtc -light` without a local `vertcoind`—receives P2P block templates, builds local PPLNS payouts, and relays solved blocks to full-node peers.
- **Store-Then-Verify ShareChain V2 (`Type: 24`):** Enforces pool-wide 15s share difficulty (`200`-share lookbehind), cumulative-work tip selection, work-capped PPLNS window (`8640` shares / `36h`), and PoW-committed `"P2PV"` coinbase metadata.
- **Full Stratum Server:** Compatible with all standard Verthash mining software (e.g., `VerthashMiner`, One-Click Miner).
- **Decentralized `go-libp2p` Mesh:** Powered by the same networking stack used by Ethereum's consensus layer, featuring Kademlia DHT, automatic UPnP NAT port mapping, persistent cryptographic Peer IDs (`peer.key`), and zero-configuration local LAN `mDNS` peer discovery.
- **Single-Port Multiplexing (`cmux`):** Automatically serves both TCP Stratum mining traffic and the HTTP Web Dashboard over a single shared port (`9172`).
- **Live Host-Aware Web Dashboard:** A built-in web interface that refreshes every 5 seconds to display real-time pool hashrates, multi-rig aggregated miner performance, worker difficulties, recent blocks, and estimated payouts, automatically adapting its branding and Stratum connection string to your domain or IP.
- **Direct Non-Custodial PPLNS Payouts:** Calculates and embeds proportional PPLNS payouts (and pool fees) directly into the block's coinbase transaction outputs (`gentx`), eliminating the need for custodial pool hot wallets or secondary `sendmany` distribution transactions.
- **Live BIP22 Consensus Verification:** Automatically runs `TestBlockValidity()` against `vertcoind` (`getblocktemplate` proposal mode) on every new block height to guarantee block headers, SegWit witness commitments, Merkle roots, and coinbase outputs pass 100% of Vertcoin Core consensus rules while mining.
- **Robust Variable Difficulty (Vardiff):** Automatically tunes individual miner target difficulties to maintain steady share submission rates across rigs of any size.
- **Persistent Share Chain & Block History:** Saves and reloads the active share chain (`sharechain.json`) and found block history (`blocks.json`) across node restarts.

---

## Prerequisites

1. A **synced `vertcoind`** instance with RPC enabled in `vertcoin.conf`.
2. The **`verthash.dat`** dataset located in your Vertcoin data directory or specified in `config.yaml`.
3. **Go 1.20+** (Go 1.22+ recommended).
4. An **open P2P port** on your firewall/router (default `9348`).

See [PRE_SETUP_GUIDE.md](PRE_SETUP_GUIDE.md) for step-by-step instructions on setting up these prerequisites.

---

## Configuration & Ports

The pool is configured using a single `config.yaml` file (copied from `config.example.yaml`).

| Port | Setting in `config.yaml` | Default | Purpose | Firewall / Port Forwarding Action |
| :--- | :--- | :--- | :--- | :--- |
| **P2P Port** | `p2pPort` | `9348` | For `go-libp2p` node-to-node GossipSub communication and DHT/mDNS discovery. | **Required:** Must be **port-forwarded** if connecting to external nodes over the internet. |
| **Stratum / Web Port** | `stratumPort` | `9172` | Multiplexed (`cmux`) port for miner Stratum connections and the HTTP Web UI. | **Allow on local firewall.** Only port-forward on your router if hosting a public node. |
| **RPC Port** | `rpcPort` | `5888` | Local JSON-RPC connection to your Vertcoin daemon (`vertcoind`). | **No action needed.** Keep restricted to `127.0.0.1` (`localhost`). |

---

## Installation & Running

1. **Clone the repository:**
   ```bash
   git clone [https://github.com/CADMonkey21/p2pool-go-VTC.git](https://github.com/CADMonkey21/p2pool-go-VTC.git)
   cd p2pool-go-VTC
   ```

2. **Install dependencies:**
   ```bash
   go mod tidy
   ```

3. **Configure the node:**
   ```bash
   cp config.example.yaml config.yaml
   nano config.yaml
   ```

4. **Build and run the node:**
   ```bash
   go build -o p2pool-go-vtc .
   ./p2pool-go-vtc
   ```
   Point your Verthash miner to `stratum+tcp://<your-node-ip>:9172` with your Vertcoin wallet address as the username, and open `http://<your-node-ip>:9172` in your browser to view the live dashboard.

### Command-Line Flags & OCM Readiness API
```bash
./p2pool-go-vtc -light -datadir /path/to/data -stratum 9172 -p2p 9348 -verthash /path/to/verthash.dat -loglevel info
```
- **Readiness Endpoint (`/api/status`):** Returns JSON (`"ready": true`, `"has_template": true`, `"synced": true`, `"peers"`, `"shares_total"`, `"height"`) for One-Click Miner health checks.
- **Dashboard Stats Endpoint (`/api/stats`):** Returns live hashrates, active miners, PPLNS payouts, and connected P2P peers.

See [LOG_GUIDE.md](LOG_GUIDE.md) for an overview of console log messages and levels.

---

## Status

*Legend: `[x]` = Complete and Verified on Mainnet*

- **P2P Layer (`go-libp2p` & GossipSub)**
  - [x] Persistent Ed25519 Peer Identity (`peer.key`)
  - [x] Kademlia DHT & Bootstrap multiaddr peer connections (IPv4 & `/dns4/`)
  - [x] Zero-config local LAN `mDNS` peer discovery
  - [x] Real-time WAN GossipSub share broadcasting & cryptographic PoW verification
  - [x] Official Public Bootstrap Seed Node deployed (`p2poolvtc.com`)
  - [x] Multi-node WAN ShareChain synchronization across peers
- **RPC Client (`vertcoind`)**
  - [x] HTTP Basic Auth JSON-RPC client with SegWit `getblocktemplate` support
  - [x] Live BIP22 `proposal` mode consensus verification (`TestBlockValidity`)
  - [x] Direct `submitblock` execution and rejection string validation
- **Stratum Server & Share Logic**
  - [x] `mining.subscribe`, `mining.authorize`, `mining.set_difficulty`, and `mining.notify`
  - [x] Dynamic Vardiff engine with immediate timestamp-rolled job updates
  - [x] Verthash PoW validation (`verthash.dat`), target checks, and exact Stratum coinbase reconstruction
- **Share Chain & Payouts**
  - [x] Deterministic PPLNS weight calculation across the ShareChain window
  - [x] Mainnet block discovery and submission verified
  - [x] Direct non-custodial coinbase PPLNS + pool fee payouts verified on Mainnet
  - [x] Automatic 100-block maturity tracking
- **UI, Monitoring & Persistence**
  - [x] Single-port `cmux` HTTP Web Dashboard & `/api/stats` JSON endpoint
  - [x] Dynamic Host-Aware Web UI branding (`p2poolvtc.com` / local IP detection)
  - [x] Multi-rig address aggregation and Synchronized CLI/Web UI metrics
  - [x] On-disk ShareChain (`sharechain.json`) and Found Block (`blocks.json`) persistence

---

## Contributing

Contributions are welcome! Feel free to open an issue or submit a pull request. See [DEV_GUIDE.md](DEV_GUIDE.md) for an architectural overview of the codebase.

## Donate

If you want to support the development of this project, feel free to donate!

**Vertcoin (VTC):** *(click the copy icon on the right)*
```text
vtc1qx9wlulctjps59jnlcg04z3jwnkku5tgwkj0j0l
```
