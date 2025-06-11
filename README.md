# P2Pool Implementation in Go

**WARNING: This is very early pre-beta work. Don't use it yet.**

This is a re-implementation of P2Pool in Go. Initially this supports Vertcoin, but it's aimed to be multicoin.

## Status

Currently the following things are planned/completed:

- [X] Allow connecting to p2pool nodes (wire protocol implementation)
- [X] Peermanager for managing connections to other peers
- [ ] Retrieving the sharechain from other peers
- [ ] Building the sharechain
- [ ] Validating the sharechain
- [X] Connecting to a fullnode over RPC
- [X] Retrieve block template from fullnode
- [ ] Compose block from share data
- [X] Stratum server
- [ ] Submit shares to p2pool network
- [ ] Web frontend

If you have any ideas, feel free to submit them as either issues or (better yet) pull requests.

## Donate

If you want to support the development of this project, feel free to donate!

Vertcoin: `vtc1qx9wlulctjps59jnlcg04z3jwnkku5tgwkj0j0l`
