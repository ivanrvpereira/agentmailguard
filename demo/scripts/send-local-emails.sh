#!/usr/bin/env bash
set -euo pipefail

LOCAL_EMAIL_ENDPOINT="${LOCAL_EMAIL_ENDPOINT:-http://localhost:8787/cdn-cgi/handler/email}"
ENVELOPE_FROM="${ENVELOPE_FROM:-demo-sender@example.test}"
ENVELOPE_TO="${ENVELOPE_TO:-inbox@agentmailguard.dev}"

if [ "$#" -eq 0 ]; then
  set -- demo/emails/*.eml
fi

for file in "$@"; do
  if [ ! -f "$file" ]; then
    echo "missing email fixture: $file" >&2
    exit 1
  fi

  status=$(
    curl -sS -o /tmp/agentmailguard-demo-email.out -w "%{http_code}" \
      --request POST "$LOCAL_EMAIL_ENDPOINT" \
      --url-query "from=$ENVELOPE_FROM" \
      --url-query "to=$ENVELOPE_TO" \
      --header "Content-Type: message/rfc822" \
      --data-binary "@$file"
  )

  if [ "$status" -lt 200 ] || [ "$status" -ge 300 ]; then
    echo "failed: $file ($status)" >&2
    cat /tmp/agentmailguard-demo-email.out >&2
    exit 1
  fi

  echo "sent: $file ($status)"
done
