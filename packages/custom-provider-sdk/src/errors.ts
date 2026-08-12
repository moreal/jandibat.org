export type CustomProviderValidationIssue = {
  path: string;
  code: string;
  message: string;
};

export class CustomProviderSdkError extends Error {
  readonly code: string;

  constructor(message: string, code: string) {
    super(message);
    this.name = "CustomProviderSdkError";
    this.code = code;
  }
}

export class CustomProviderValidationError extends CustomProviderSdkError {
  readonly issues: readonly CustomProviderValidationIssue[];

  constructor(issues: readonly CustomProviderValidationIssue[]) {
    super("Custom provider 요청 값이 올바르지 않습니다.", "validation_failed");
    this.name = "CustomProviderValidationError";
    this.issues = Object.freeze(issues.map((issue) => Object.freeze({ ...issue })));
  }
}

export class CustomProviderApiError extends CustomProviderSdkError {
  readonly status: number;
  readonly requestId?: string;
  readonly retryAfter?: string;

  constructor(
    status: number,
    code: string,
    requestId?: string,
    retryAfter?: string,
  ) {
    super(`Custom provider API 요청이 실패했습니다. (${status})`, code);
    this.name = "CustomProviderApiError";
    this.status = status;
    this.requestId = requestId;
    this.retryAfter = retryAfter;
  }
}

export class CustomProviderNetworkError extends CustomProviderSdkError {
  constructor() {
    super("Custom provider API에 연결할 수 없습니다.", "network_error");
    this.name = "CustomProviderNetworkError";
  }
}

export class CustomProviderResponseError extends CustomProviderSdkError {
  constructor() {
    super("Custom provider API 응답 형식이 올바르지 않습니다.", "invalid_response");
    this.name = "CustomProviderResponseError";
  }
}
