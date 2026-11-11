#!/usr/bin/env bash
# Hosts matches on this machine as an Amazon GameLift Servers Anywhere compute and serves the browser client,
# so you can sign in, press Find match, and play. Everything listens on 127.0.0.1 only; no inbound port is
# opened to any network. Press Ctrl+C to stop; the script deregisters the compute on exit.
#
# Works with both fleet types: on the Anywhere fleet it also runs the game server on this machine; on the EC2
# fleet the service runs it, and this script only serves the page.
#
# Needs: the deployed stack, AWS credentials for its account and Region, the AWS CLI, and Go.
# Create players first with ./scripts/create-test-player.sh <name>.
# Settings (environment variables):
#   STACK_NAME, PORT (game server port, 33430), WEB_PORT (page port, 8080), COMPUTE_NAME
#   NO_OPEN=1      don't open the page in the default browser
set -euo pipefail

STACK="${STACK_NAME:-GameServerAndMatchmakingStack}"
PORT="${PORT:-33430}"
WEB_PORT="${WEB_PORT:-8080}"
COMPUTE="${COMPUTE_NAME:-local-dev-${USER:-user}}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d)"
PIDS=()
TAIL_PID=""

stack_output() {
  aws cloudformation describe-stacks --stack-name "$STACK" \
    --query "Stacks[0].Outputs[?contains(OutputKey, '$1')].OutputValue | [0]" --output text
}

FLEET="$(stack_output FleetId)"
FLEET_TYPE="$(stack_output FleetType)"
LOCATION="$(stack_output LocationName)"
API_URL="$(stack_output ApiUrl)"
CLIENT_ID="$(stack_output UserPoolClientId)"
REGION="$(aws configure get region 2>/dev/null || true)"
REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-$REGION}}"
if [[ -z "$FLEET" || "$FLEET" == "None" || -z "$API_URL" || "$API_URL" == "None" ]]; then
  echo "Could not read the outputs of stack $STACK. Check the stack name and the AWS Region." >&2
  exit 1
fi

# The programs this script starts (game server and page server) never call AWS with your credentials, so they
# start with an empty environment plus PATH. Only this script and the AWS CLI calls in it use your credentials.
CLEAN_ENV=(env -i "PATH=$PATH")

# Stops a process and everything it started. The parent goes first, so a loop below can't start a new
# game server process after its current one is stopped.
kill_tree() {
  local child children
  children="$(pgrep -P "$1" 2>/dev/null || true)"
  kill "$1" 2>/dev/null || true
  for child in $children; do kill_tree "$child"; done
}

cleanup() {
  trap - EXIT INT TERM
  for pid in "${PIDS[@]:-}"; do [[ -n "$pid" ]] && kill_tree "$pid"; done
  [[ -n "$TAIL_PID" ]] && { kill_tree "$TAIL_PID"; wait "$TAIL_PID" 2>/dev/null || true; }
  if [[ "$FLEET_TYPE" != "ec2" ]]; then
    aws gamelift deregister-compute --fleet-id "$FLEET" --compute-name "$COMPUTE" >/dev/null 2>&1 || true
    echo "Stopped and deregistered compute $COMPUTE."
  else
    echo "Stopped. The EC2 fleet keeps running until you delete the stack."
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

echo "Building the server and the browser client..."
(cd "$ROOT/server" && go build -o "$WORK/server" ./cmd/server && go build -o "$WORK/web" ./cmd/web)

# Before step 1 on an Anywhere fleet: you register the machine and start the processes yourself. (On a managed EC2
# fleet the service does both.) RegisterCompute adds this machine to the fleet's custom location, with the
# address players will connect to.
if [[ "$FLEET_TYPE" == "ec2" ]]; then
  echo "This stack uses a managed EC2 fleet: Amazon GameLift Servers runs the game server for you."
  echo "  This script only serves the page."
else
echo "Registering this machine as compute $COMPUTE in $LOCATION..."
aws gamelift register-compute --compute-name "$COMPUTE" --fleet-id "$FLEET" \
  --location "$LOCATION" --ip-address 127.0.0.1 >/dev/null
echo "  Your machine is now compute $COMPUTE in fleet $FLEET, location $LOCATION."
echo "  The service will send players to 127.0.0.1:$PORT, which is the game server on this machine."
ENDPOINT="$(aws gamelift describe-compute --fleet-id "$FLEET" --compute-name "$COMPUTE" \
  --query Compute.GameLiftServiceSdkEndpoint --output text)"

# One game server process hosts one match and then exits, so keep starting a new one. Each process gets a
# short-lived auth token from GetComputeAuthToken and the connection settings in GAMELIFT_SDK_* variables,
# which InitSDKFromEnvironment reads (server/gamelift/process.go).
host_matches() {
  cd "$WORK" # the server SDK writes its own log folder in the current directory; keep it with the other temporary files
  local n=0
  while true; do
    n=$((n + 1))
    local token
    # A failed call (expired credentials, a network error) must not end this loop, or the page would keep
    # running with no game server behind it.
    token="$(aws gamelift get-compute-auth-token --fleet-id "$FLEET" --compute-name "$COMPUTE" \
      --query AuthToken --output text)" || { echo "get-compute-auth-token failed; retrying in 5 seconds" >&2; sleep 5; continue; }
    "${CLEAN_ENV[@]}" GAMELIFT_SDK_WEBSOCKET_URL="$ENDPOINT" GAMELIFT_SDK_FLEET_ID="$FLEET" GAMELIFT_SDK_HOST_ID="$COMPUTE" \
      GAMELIFT_SDK_PROCESS_ID="proc-$(date +%s)-$n" GAMELIFT_SDK_AUTH_TOKEN="$token" \
      "$WORK/server" -gamelift -port "$PORT" -bind-host 127.0.0.1 \
      -allow-origin "http://127.0.0.1:$WEB_PORT,http://localhost:$WEB_PORT" -log-dir "$WORK/logs" \
      >>"$WORK/server.log" 2>&1 || true
    sleep 1
  done
}
: >"$WORK/server.log"
host_matches &
PIDS+=("$!")
fi

"${CLEAN_ENV[@]}" "$WORK/web" -listen "127.0.0.1:$WEB_PORT" \
  -api-url "$API_URL" -region "$REGION" -client-id "$CLIENT_ID" >"$WORK/web.log" 2>&1 &
WEB_PID=$!
PIDS+=("$WEB_PID")
sleep 1
# Stop early if the page server did not start (for example, the port is already in use).
if ! kill -0 "$WEB_PID" 2>/dev/null; then
  echo "The page server did not start. Is port $WEB_PORT already in use?" >&2
  cat "$WORK/web.log" >&2
  exit 1
fi

URL="http://127.0.0.1:$WEB_PORT/"
echo
echo "Open $URL and sign in with a player from ./scripts/create-test-player.sh."
echo "Press Find match in two browser windows side by side to play each other, or in one and wait about 15 seconds to play the computer."
echo "Press Ctrl+C to stop."
if [[ "${NO_OPEN:-0}" != "1" ]] && command -v open >/dev/null 2>&1; then open "$URL" || true; fi
echo
if [[ "$FLEET_TYPE" == "ec2" ]]; then
  echo "The game server's log is on the fleet instance; find it in the Amazon GameLift Servers console (fleet, Game sessions, Logs)."
  wait
  exit 0
fi
( tail -n +1 -f "$WORK/server.log" | awk '/level=(INFO|WARN|ERROR)/ { gsub(/gameSessionId=[^ ]+ /, ""); print; fflush() }' ) &
TAIL_PID=$!
wait "$TAIL_PID"
