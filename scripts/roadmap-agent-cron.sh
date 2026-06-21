#!/bin/bash
# Controller script for roadmap agent cron
# Runs every 15 mins, manages a 2-hour window

WORKSPACE="/Users/pc/code/vulos/vulos"
STATE_FILE="/tmp/roadmap-agents-starttime"
SESSIONS_FILE="/tmp/roadmap-agents-sessions"
NUM_AGENTS=10
WINDOW_HOURS=2
AGENT_PROMPT="Review the roadmap files in $WORKSPACE/roadmap/. Pick a high-priority item that hasn't been implemented yet and start implementing it. Focus on AI.md, NETWORK.md, APP-STORE.md as these are critical. Work in the $WORKSPACE directory. Implement the feature fully with tests if possible."

# Initialize start time if not set
if [ ! -f "$STATE_FILE" ]; then
  echo $(date +%s) > "$STATE_FILE"
  echo "[$(date)] Starting 2-hour roadmap agent session"
fi

START_TIME=$(cat "$STATE_FILE")
CURRENT_TIME=$(date +%s)
ELAPSED=$((CURRENT_TIME - START_TIME))
WINDOW_SECS=$((WINDOW_HOURS * 3600))

# Check if 2 hours have passed
if [ $ELAPSED -ge $WINDOW_SECS ]; then
  echo "[$(date)] 2-hour window expired. Stopping agent cron."
  rm -f "$STATE_FILE" "$SESSIONS_FILE"
  exit 0
fi

# Check if agents are still running
if [ -f "$SESSIONS_FILE" ]; then
  ACTIVE_COUNT=0
  while IFS= read -r line; do
    pid=$(echo "$line" | cut -d: -f1)
    if kill -0 "$pid" 2>/dev/null; then
      ACTIVE_COUNT=$((ACTIVE_COUNT + 1))
    fi
  done < "$SESSIONS_FILE"

  if [ $ACTIVE_COUNT -ge $NUM_AGENTS ]; then
    echo "[$(date)] $ACTIVE_COUNT agents still active, skipping launch"
    exit 0
  else
    echo "[$(date)] Only $ACTIVE_COUNT agents active, need to replenish to $NUM_AGENTS"
    rm -f "$SESSIONS_FILE"
  fi
fi

# Launch agents
echo "[$(date)] Launching $NUM_AGENTS agents..."
> "$SESSIONS_FILE"

for i in $(seq 1 $NUM_AGENTS); do
  (
    cd "$WORKSPACE"
    nohup opencode run "$AGENT_PROMPT" --fork > /tmp/roadmap-agent-$i.log 2>&1 &
  ) &
  echo $!:roadmap-agent-$i >> "$SESSIONS_FILE"
  echo "Launched roadmap-agent-$i (PID: $!)"
done

echo "[$(date)] All $NUM_AGENTS agents launched"