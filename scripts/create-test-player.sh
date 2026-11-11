#!/usr/bin/env bash
# Creates a player in the stack's user pool with a random password and prints the password once.
# Usage: ./scripts/create-test-player.sh <player-name>
# Settings (environment variables): STACK_NAME.
set -euo pipefail

STACK="${STACK_NAME:-GameServerAndMatchmakingStack}"
NAME="${1:-}"
if [[ ! "$NAME" =~ ^[A-Za-z0-9_-]{3,32}$ ]]; then
  echo "Usage: $0 <player-name>  (3 to 32 letters, digits, hyphens, or underscores)" >&2
  exit 2
fi

POOL="$(aws cloudformation describe-stacks --stack-name "$STACK" \
  --query "Stacks[0].Outputs[?contains(OutputKey, 'UserPoolId')].OutputValue | [0]" --output text)"
if [[ -z "$POOL" || "$POOL" == "None" ]]; then
  echo "Could not read the user pool from stack $STACK. Check the stack name and the AWS Region." >&2
  exit 1
fi

# 24 random hex characters (96 bits) plus one of each class the password policy requires.
PASSWORD="$(openssl rand -hex 12)Aa1!"

aws cognito-idp admin-create-user --user-pool-id "$POOL" --username "$NAME" --message-action SUPPRESS >/dev/null
if ! aws cognito-idp admin-set-user-password --user-pool-id "$POOL" --username "$NAME" --password "$PASSWORD" --permanent; then
  aws cognito-idp admin-delete-user --user-pool-id "$POOL" --username "$NAME" >/dev/null 2>&1 || true # no half-made player
  exit 1
fi

echo "Player name: $NAME"
echo "Password:    $PASSWORD"
echo "The password is shown only now. To remove the player: aws cognito-idp admin-delete-user --user-pool-id $POOL --username $NAME"
