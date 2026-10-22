# Atlas compatibility

This directory preserves the public SQL Server Atlas API through typed aliases and forwarding functions to [sqlglot-go/database/atlas/sqlserver](https://github.com/flanksource/sqlglot-go/tree/main/database/atlas/sqlserver). The owning implementation contains the driver registration, HCL codec, SQL inspection and Atlas helper ports. Update that implementation rather than adding a second driver here.

The Apache-2.0 license and original Atlas attribution are retained in `LICENSE.atlas` and `NOTICE`. Refresh instructions live with the sqlglot-go implementation.
