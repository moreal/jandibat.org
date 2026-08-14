export function privateConsentEligible(
  supportsPrivateData: boolean,
  authMethod: string,
): boolean {
  return supportsPrivateData && (authMethod === "oauth2" || authMethod === "token");
}

export function privateConsentValue(
  supportsPrivateData: boolean,
  authMethod: string,
  checked: boolean,
): boolean {
  return privateConsentEligible(supportsPrivateData, authMethod) && checked;
}
