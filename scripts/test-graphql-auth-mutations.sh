#!/bin/sh
set -eu

schema=graphql/schema.graphql
for field in \
  'requestMagicLink(input: RequestMagicLinkInput!): RequestMagicLinkPayload!' \
  'beginPasskeyRegistration: BeginPasskeyRegistrationPayload!' \
  'finishPasskeyRegistration(input: FinishPasskeyRegistrationInput!): FinishPasskeyRegistrationPayload!' \
  'beginPasskeySignIn: BeginPasskeySignInPayload!' \
  'finishPasskeySignIn(input: FinishPasskeySignInInput!): FinishPasskeySignInPayload!' \
  'signOut: SignOutPayload!' \
  'revokeSession(input: RevokeSessionInput!): RevokeSessionPayload!' \
  'revokeOtherSessions: RevokeOtherSessionsPayload!'; do
  if ! grep -Fq "$field" "$schema"; then
    echo "GraphQL auth mutation contract is missing: $field" >&2
    exit 1
  fi
done

for payload in RequestMagicLink BeginPasskeyRegistration FinishPasskeyRegistration BeginPasskeySignIn FinishPasskeySignIn SignOut RevokeSession RevokeOtherSessions; do
  if ! grep -Fq "type ${payload}Payload implements MutationPayload" "$schema"; then
    echo "GraphQL auth mutation lacks typed payload: $payload" >&2
    exit 1
  fi
done

if grep -Eq '^type (PasskeyOptions|PasskeyCredential|Viewer|UserProfile) implements Node' "$schema"; then
  echo 'auth value objects must not implement Relay Node' >&2
  exit 1
fi
if grep -Eq '(^|[[:space:]])consumeMagicLink[[:space:]]*\(' "$schema"; then
  echo 'Magic Link consumption must remain an HTTP callback edge' >&2
  exit 1
fi
if sed -n '/^type Session implements Node {/,/^}/p' "$schema" |
   grep -Eiq '(^|[[:space:]])(token|tokenHash|sessionToken|credential)[[:space:]]*:'; then
  echo 'Session Node must not expose bearer or credential material' >&2
  exit 1
fi

echo 'GraphQL auth mutations have typed payloads without extra Nodes'
