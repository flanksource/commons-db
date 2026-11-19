# Redsync source attribution

This directory contains the mutex implementation, Redis interfaces, Lua scripts, and Valkey adapter from [go-redsync/redsync v4.18.1](https://github.com/go-redsync/redsync/tree/bf91415b639c4063e557bdfd462910bf7b0afafc), copyright Mahmud Ridwan and contributors. See the [BSD 3-Clause license](LICENSE) for the exact notice and redistribution terms.

The copied files use local import paths. The [commons-db Redis adapter](../../redis.go) wraps the copied pool to coordinate admission with its reader and group locks. Redis client adapters for other libraries are outside this copy.
