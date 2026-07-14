#!/usr/bin/env bash
# Launch Claude Code (Opus, high effort) against the Go-proxy migration plan.
# Autonomous, skip-permissions. Run this yourself when you have token budget.
#
# Usage:
#   bash /root/antigravity-go-proxy/launch-claude.sh
#
# Notes:
# - Runs from the project dir so CLAUDE.md auto-loads.
# - IS_SANDBOX=1 is REQUIRED because this box runs as root and Claude Code
#   refuses --dangerously-skip-permissions under root without it.
# - Uses print mode (-p) so it runs to completion non-interactively.
#   --max-turns is high because this is a multi-phase build; raise if it stops early.
# - Output is teed to a log so a mid-run token stall leaves a resumable trail.
#   If it stops on a session/token limit, resume with:
#     cd /root/antigravity-go-proxy && IS_SANDBOX=1 claude -p "Continue PLAN.md from the last incomplete phase. Read git log to see what's done." --dangerously-skip-permissions --model opus --effort high --max-turns 200

set -euo pipefail
cd /root/antigravity-go-proxy

# Preflight: confirm the bypass flag works before the long run (per claude-code skill).
echo "test" | IS_SANDBOX=1 claude -p "Reply OK" --dangerously-skip-permissions --max-turns 1 \
  || { echo "PREFLIGHT FAILED — check 'claude auth status' and IS_SANDBOX"; exit 1; }

LOG="/root/antigravity-go-proxy/claude-run-$(date +%Y%m%d-%H%M%S).log"
echo "Launching Claude Code (opus/high). Log: $LOG"

IS_SANDBOX=1 claude -p "$(cat PROMPT.txt)" \
  --dangerously-skip-permissions \
  --model opus \
  --effort high \
  --max-turns 200 \
  --verbose 2>&1 | tee "$LOG"

echo "Done. Full transcript: $LOG"
