#!/bin/bash
# The stand-in agent, run INSIDE the reminal session (typed into its shell),
# as an agent's `reminal mcp` would be: it runs each request the driver
# drops in its folder (default /tmp/q; NNN.req: a tool, then its JSON arguments) through
# `reminal mcp`, and leaves the answer beside it (NNN.res).
Q=${1:-/tmp/q}
mkdir -p "$Q"
echo "stand-in agent ready"
while true; do
  for req in "$Q"/*.req; do
    [ -e "$req" ] || continue
    res="${req%.req}.res"; [ -e "$res" ] && continue
    tool=$(head -1 "$req"); args=$(tail -n +2 "$req")
    : > "$res.tmp"
    # Its input stays open until the answer is in, then closes: reminal mcp ends.
    ( printf '%s\n%s\n%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"rig","version":"1"}}}' '{"jsonrpc":"2.0","method":"notifications/initialized"}' "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"$tool\",\"arguments\":$args}}"
      for i in $(seq 1200); do [ -s "$res.tmp" ] && break; sleep 0.1; done ) \
      | reminal mcp 2>/dev/null | grep --line-buffered -m1 '"id":2' > "$res.tmp"
    mv "$res.tmp" "$res"
    echo "did $tool"
  done
  sleep 0.2
done
