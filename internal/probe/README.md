# Probe domain

This package is the server-side boundary for TZA Probe. It deliberately keeps
the three responsibilities that make a probe run observable in one package,
while separating their implementation files:

- `route.go` owns the embedded target catalogue, route scheduling, Agent event
  dispatch, and structured result storage/querying.
- `route_tasks.go` owns independently assigned return-route tasks. Every task
  names its target and explicit client UUID list; an empty client list never
  means all machines.
- `latency.go` keeps compatibility helpers for legacy managed Ping settings;
  new Monitoring Center latency tasks use the normal PingTask API directly.
- `targets_embedded.json` is the offline catalogue shipped with the Core. The
  admin UI reads it through the RPC and never fetches a third-party list. The
  public target is the catalogue's `*.ip.zstaticcdn.com` hostname; snapshot IPs
  are retained only as catalogue metadata and are not used as probe targets.

The RPC layer in `web/rpc/jsonrpc/admin.probes.go` only validates transport
parameters, permissions, and persistence calls. The public theme consumes
`public:getCarrierRouteStats`; it does not execute traceroute or TcpQuality.

New configuration stores independently assigned route tasks in
`carrier_route_tasks`. Each task contains its own interval, target hostname,
backup hostname, and explicit client UUID list; an empty list never means all
machines. The first startup with a legacy `carrier_route_selections` key runs a
one-time migration that snapshots the currently known clients into equivalent
tasks. Legacy `carrier_ping_selections` remains readable for the managed Ping
compatibility helpers, while new latency tasks use the normal PingTask API.
