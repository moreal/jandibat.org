import type { WebAuthnCredentialDto } from "@jandibat/contracts";

type PasskeyOptions = Record<string, unknown>;

const MAX_BASE64URL_LENGTH = 16_384;
const MAX_CREDENTIAL_DESCRIPTORS = 100;
const BASE64URL_PATTERN = /^[A-Za-z0-9_-]+$/;

export class PasskeyInputError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "PasskeyInputError";
  }
}

function base64UrlToBuffer(value: unknown, field: string): ArrayBuffer {
  if (
    typeof value !== "string" ||
    value.length === 0 ||
    value.length > MAX_BASE64URL_LENGTH ||
    value.length % 4 === 1 ||
    !BASE64URL_PATTERN.test(value)
  ) {
    throw new PasskeyInputError(`Passkey ${field} 값이 올바르지 않습니다.`);
  }
  const normalized = value.replaceAll("-", "+").replaceAll("_", "/");
  const padded = normalized.padEnd(Math.ceil(normalized.length / 4) * 4, "=");
  try {
    const bytes = Uint8Array.from(atob(padded), (character) =>
      character.charCodeAt(0),
    );
    return bytes.buffer;
  } catch {
    throw new PasskeyInputError(`Passkey ${field} 값이 올바르지 않습니다.`);
  }
}

function bufferToBase64Url(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer);
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary)
    .replaceAll("+", "-")
    .replaceAll("/", "_")
    .replaceAll("=", "");
}

function recordValue(value: unknown, field: string): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new PasskeyInputError(`Passkey ${field} 값이 올바르지 않습니다.`);
  }
  return value as Record<string, unknown>;
}

function credentialDescriptors(
  value: unknown,
  field: "excludeCredentials" | "allowCredentials",
): PublicKeyCredentialDescriptor[] | undefined {
  if (value === undefined) return undefined;
  if (!Array.isArray(value) || value.length > MAX_CREDENTIAL_DESCRIPTORS) {
    throw new PasskeyInputError(`Passkey ${field} 값이 올바르지 않습니다.`);
  }
  return value.map((candidate, index) => {
    const descriptor = recordValue(candidate, `${field}[${index}]`);
    if (descriptor.type !== "public-key") {
      throw new PasskeyInputError(`Passkey ${field}[${index}].type 값이 올바르지 않습니다.`);
    }
    return {
      ...(descriptor as unknown as PublicKeyCredentialDescriptor),
      id: base64UrlToBuffer(descriptor.id, `${field}[${index}].id`),
    };
  });
}

export function creationOptionsFromJson(
  value: PasskeyOptions,
): PublicKeyCredentialCreationOptions {
  const user = recordValue(value.user, "user");
  if (typeof user.name !== "string" || typeof user.displayName !== "string") {
    throw new PasskeyInputError("Passkey user 값이 올바르지 않습니다.");
  }
  return {
    ...(value as unknown as PublicKeyCredentialCreationOptions),
    challenge: base64UrlToBuffer(value.challenge, "challenge"),
    user: {
      ...(user as unknown as PublicKeyCredentialUserEntity),
      id: base64UrlToBuffer(user.id, "user.id"),
    },
    excludeCredentials: credentialDescriptors(
      value.excludeCredentials,
      "excludeCredentials",
    ),
  };
}

export function requestOptionsFromJson(
  value: PasskeyOptions,
): PublicKeyCredentialRequestOptions {
  return {
    ...(value as unknown as PublicKeyCredentialRequestOptions),
    challenge: base64UrlToBuffer(value.challenge, "challenge"),
    allowCredentials: credentialDescriptors(
      value.allowCredentials,
      "allowCredentials",
    ),
  };
}

function passkeyOperationError(error: unknown): Error {
  if (!(error instanceof DOMException)) {
    return error instanceof Error
      ? error
      : new Error("Passkey 요청을 완료하지 못했습니다.");
  }
  const messages: Partial<Record<string, string>> = {
    AbortError: "Passkey 요청이 취소되었습니다. 다시 시도해 주세요.",
    InvalidStateError: "이 인증 정보는 이미 이 기기에 등록되어 있습니다.",
    NotAllowedError: "Passkey 요청이 취소되었거나 만료되었습니다. 다시 시도해 주세요.",
    NotSupportedError: "이 기기에서는 요청한 Passkey 방식을 지원하지 않습니다.",
    SecurityError: "현재 주소에서는 Passkey를 안전하게 사용할 수 없습니다.",
  };
  return new Error(messages[error.name] ?? "Passkey 요청을 완료하지 못했습니다.");
}

function serializeCredential(credential: PublicKeyCredential): WebAuthnCredentialDto {
  const response = credential.response;
  const authenticatorAttachment: WebAuthnCredentialDto["authenticatorAttachment"] =
    credential.authenticatorAttachment === "platform" ||
    credential.authenticatorAttachment === "cross-platform"
      ? (credential.authenticatorAttachment as "platform" | "cross-platform")
      : null;
  const common = {
    id: credential.id,
    rawId: bufferToBase64Url(credential.rawId),
    type: "public-key" as const,
    authenticatorAttachment,
    clientExtensionResults: { ...credential.getClientExtensionResults() },
  };

  if (response instanceof AuthenticatorAttestationResponse) {
    return {
      ...common,
      response: {
        attestationObject: bufferToBase64Url(response.attestationObject),
        clientDataJSON: bufferToBase64Url(response.clientDataJSON),
        transports: response.getTransports?.() ?? [],
      },
    };
  }

  if (response instanceof AuthenticatorAssertionResponse) {
    return {
      ...common,
      response: {
        authenticatorData: bufferToBase64Url(response.authenticatorData),
        clientDataJSON: bufferToBase64Url(response.clientDataJSON),
        signature: bufferToBase64Url(response.signature),
        userHandle: response.userHandle
          ? bufferToBase64Url(response.userHandle)
          : null,
      },
    };
  }

  throw new Error("지원하지 않는 Passkey 응답입니다.");
}

export function passkeyAvailable(): boolean {
  return (
    typeof window !== "undefined" &&
    window.isSecureContext &&
    "PublicKeyCredential" in window &&
    typeof navigator.credentials?.create === "function" &&
    typeof navigator.credentials?.get === "function"
  );
}

export async function createPasskey(
  options: PasskeyOptions,
): Promise<WebAuthnCredentialDto> {
  if (!passkeyAvailable()) {
    throw new Error("이 브라우저에서는 Passkey를 사용할 수 없습니다.");
  }
  let credential: Credential | null;
  try {
    credential = await navigator.credentials.create({
      publicKey: creationOptionsFromJson(options),
    });
  } catch (error) {
    throw passkeyOperationError(error);
  }
  if (!(credential instanceof PublicKeyCredential)) {
    throw new Error("Passkey 만들기가 취소되었습니다.");
  }
  return serializeCredential(credential);
}

export async function getPasskey(
  options: PasskeyOptions,
): Promise<WebAuthnCredentialDto> {
  if (!passkeyAvailable()) {
    throw new Error("이 브라우저에서는 Passkey를 사용할 수 없습니다.");
  }
  let credential: Credential | null;
  try {
    credential = await navigator.credentials.get({
      publicKey: requestOptionsFromJson(options),
    });
  } catch (error) {
    throw passkeyOperationError(error);
  }
  if (!(credential instanceof PublicKeyCredential)) {
    throw new Error("Passkey 로그인이 취소되었습니다.");
  }
  return serializeCredential(credential);
}
