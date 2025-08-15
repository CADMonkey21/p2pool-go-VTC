# P2Pool-Go-VTC: Prerequisite Setup Guide

This guide provides step-by-step instructions for setting up the necessary prerequisites to run your `p2pool-go-vtc` node. It is intended for users on a Linux-based system (like Debian, Ubuntu, or CentOS).

---

### Step 1: Install Go

Your node is written in Go, so you need the Go compiler and tools installed.

**For Debian/Ubuntu:**
```bash
sudo apt update
sudo apt install golang-go -y
```

**For Fedora/CentOS/RHEL:**
```bash
sudo dnf install golang -y
```

**For other systems (macOS, Windows):**
Download the official installer from the [Go website](https://go.dev/dl/).

After installation, verify it was successful by running:
```bash
go version
```
You should see an output like `go version go1.22.5 linux/amd64`.

---

### Step 2: Set Up `vertcoind`

Your p2pool node needs to communicate with a fully synced Vertcoin daemon.

1.  **Install Vertcoin Core:** Follow the official instructions to install the Vertcoin Core wallet on your system.

2.  **Configure `vertcoin.conf`:** You must enable the RPC server so p2pool can communicate with it. Edit or create the configuration file at `~/.vertcoin/vertcoin.conf` and add the following lines, choosing a strong username and password:

    ```ini
    # ~/.vertcoin/vertcoin.conf
    rpcuser=your_rpc_user
    rpcpassword=your_very_strong_rpc_password
    server=1
    txindex=1
    ```

3.  **Run and Sync:** Start `vertcoind`. The first time you run it, it will need to download and sync the entire Vertcoin blockchain. This can take several hours. **You must let it sync completely before starting your p2pool node.**

---

### Step 3: Locate or Create `verthash.dat`

The Verthash algorithm requires a large data file (over 1GB) to function.

1.  **Automatic Generation:** The first time you run `vertcoind`, it will automatically create the `verthash.dat` file for you.
2.  **Location:** The file is typically located in your Vertcoin data directory.
    * **Standard Install:** `~/.vertcoin/verthash.dat`
    * **Snap Install:** `~/snap/vertcoin-core/common/.vertcoin/verthash.dat`

Your p2pool node needs to be able to find this file. You have two options:

**Option A (Recommended): Copy the file**
This is the simplest method. Copy the file to the default location that p2pool checks.
```bash
# Create the directory if it doesn't exist
mkdir -p ~/.vertcoin/

# Copy the file (use the correct source path for your system)
cp ~/snap/vertcoin-core/common/.vertcoin/verthash.dat ~/.vertcoin/
```

**Option B: Use the config file**
If you don't want to move the file, you can tell p2pool exactly where to find it. Open your `config.yaml` and set the full path:
```yaml
# config.yaml
verthash_dat_file: "/root/snap/vertcoin-core/common/.vertcoin/verthash.dat"
```

---

### Step 4: Configure Firewall (ufw)

You must open the necessary ports in your server's firewall to allow other nodes and your miners to connect. These commands are for `ufw` (Uncomplicated Firewall), which is standard on Ubuntu/Debian.

1.  **Allow SSH (so you don't get locked out!):**
    ```bash
    sudo ufw allow ssh
    ```

2.  **Allow the P2P Port:** This is required for your node to talk to other p2pool nodes.
    ```bash
    sudo ufw allow 9348/tcp
    ```

3.  **Allow the Stratum Port:** This is required for your miners to connect to the node.
    ```bash
    sudo ufw allow 9172/tcp
    ```

4.  **Enable the Firewall:**
    ```bash
    sudo ufw enable
    ```

5.  **Check the Status:** Verify that your rules are active.
    ```bash
    sudo ufw status
    ```
    The output should show that ports `22`, `9348`, and `9172` are allowed.

---

### Step 5: Final Steps

Once Go is installed, `vertcoind` is synced, `verthash.dat` is accessible, and your firewall is configured, you can proceed with the instructions in the `README.md`:

1.  Clone the `p2pool-go-VTC` repository.
2.  Install dependencies with `go mod tidy`.
3.  Create and edit your `config.yaml` to match your RPC settings and pool address.
4.  Run the node with `go run .`.

