# 보안 검토 증거

PR 또는 release마다 아래 템플릿을 복사해 `YYYY-MM-DD-<short-sha>.md`로 저장합니다. 저장소에 기록할 때 실제 사용자 식별자와 secret을 제거합니다. 모든 control ID는 정확히 한 번 있어야 하며 validator와 CI gate가 형식, 검토 SHA, 검토 뒤 code drift를 검사합니다.

`Commit SHA`는 검토한 code commit의 정확한 lowercase 40-hex SHA입니다. `Reviewer`/`Security owner`는 `@github-login`, 검토 시각은 UTC RFC3339여야 합니다. `PASS` evidence는 immutable GitHub Actions run/artifact URL과 64-hex SHA-256을 함께 쓰거나 `command:<실행 명령>; artifact-sha256:<64hex>` 형식을 사용합니다. `N/A`는 GitHub issue/PR URL과 구체적 rationale을 함께 써야 합니다. 임의 설명, 짧은 SHA, 자동 생성 PASS는 거부됩니다.

검토할 code를 먼저 commit하고 사람이 그 exact SHA를 검토한 뒤, review record만 별도 후속 commit으로 추가합니다. `make security-review-gate SECURITY_REVIEW=... SECURITY_REVIEW_BASE=...`는 reviewed SHA가 현재 commit의 조상이며 그 이후 변경이 review record 하나뿐인지 확인합니다. Review와 code를 같은 commit에 넣는 self-reference는 지원하지 않습니다.

```markdown
# Security review — <change>

| Metadata | Value |
| --- | --- |
| Commit SHA | NOT RUN |
| Scope | NOT RUN |
| Security owner | NOT RUN |

| Control | Status | Evidence or N/A rationale | Reviewer | Reviewed at UTC |
| --- | --- | --- | --- | --- |
| COM-01 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| COM-02 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| COM-03 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| COM-04 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| COM-05 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| COM-06 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| COM-07 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| COM-08 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| MAG-01 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| MAG-02 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| MAG-03 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| MAG-04 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| MAG-05 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| MAG-06 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| WEB-01 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| WEB-02 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| WEB-03 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| WEB-04 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| WEB-05 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| WEB-06 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| OAU-01 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| OAU-02 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| OAU-03 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| OAU-04 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| OAU-05 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| OAU-06 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| OAU-07 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| PRV-01 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| PRV-02 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| PRV-03 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| PRV-04 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| PRV-05 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| PRV-06 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| SVG-01 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| SVG-02 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| SVG-03 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| SVG-04 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| SVG-05 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| OPS-01 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| OPS-02 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| OPS-03 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| OPS-04 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| OPS-05 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| OPS-06 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| OPS-07 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
| OPS-08 | NOT RUN | NOT RUN | NOT RUN | NOT RUN |
```
