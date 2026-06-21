#!/bin/bash
# Agent launcher for roadmap implementation
# Usage: ./launch-roadmap-agents.sh [num_agents]

NUM_AGENTS=${1:-10}
AGENT_TYPE="general"
WORKSPACE="/Users/pc/code/vulos/vulos"

echo "[$(date)] Launching $NUM_AGENTS agents for roadmap implementation..."

for i in $(seq 1 $NUM_AGENTS); do
  opencode --spawn-agent "$AGENT_TYPE" \
    --prompt "Review the roadmap files in $WORKSPACE/roadmap/. Pick a high-priority item that hasn't been implemented yet and start implementing it. Focus on AI.md, NETWORK.md, APP-STORE.md as these are critical. Work in the $WORKSPACE directory. Implement the feature fully with tests if possible." \
    --name "roadmap-agent-$i" &
  echo "Launched agent roadmap-agent-$i (PID: $!)"
done

wait
echo "[$(date)] All agents launched successfully"