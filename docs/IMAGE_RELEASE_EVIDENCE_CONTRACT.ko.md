# 이미지 릴리스 증거 계약

`scripts/image-release.mjs publish EVIDENCE`는 `api`, `worker`, `maintenance`, `web`, `restore-tools`의 로컬 archive 검사와 registry 스캔이 모두 성공한 뒤 릴리스 증거를 기록합니다. 첫 registry 조회 전에 다섯 로컬 스캔 영수증과 restore-tools runtime·secure·라이선스 검토 증거를 검사합니다. 이 사전 검증에 실패하면 registry inspect/copy/tag와 `release.json`/`GITHUB_OUTPUT` 출력을 모두 시작하지 않습니다. 다섯 SHA tag를 게시 전 확인하고, 각 이미지의 digest 참조와 tag를 게시 후 다시 조회하여 둘 다 후보 manifest digest와 같은지 확인합니다. 어느 단계든 실패하면 `GITHUB_OUTPUT`에 배포 참조를 쓰지 않습니다. 이미 게시한 image를 자동으로 삭제하지 않으므로 registry 단계 중간 실패 시 registry에는 일부 tag가 남을 수 있습니다. 같은 소스 SHA로 재실행할 때 기존 tag가 같은 digest일 때만 계속 진행합니다.

증거 디렉터리의 `release.json`은 다음 필드만 갖는 JSON 객체입니다.

```json
{
  "schemaVersion": 1,
  "sourceSha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "images": [
    {
      "name": "api",
      "tag": "ghcr.io/owner/repo/api:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      "ref": "ghcr.io/owner/repo/api@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
      "imageId": "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
      "manifestDigest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
      "scanReceipt": "api-sha256-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.release.json",
      "scanReceiptSha256": "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
    }
  ]
}
```

실제 `images` 배열에는 다섯 이름이 각각 정확히 한 번씩 있습니다. `sourceSha`는 소문자 40자리 Git SHA이며 모든 `tag`의 suffix와 같습니다. `ref`는 각 이미지의 immutable registry manifest digest를 포함합니다. `imageId`는 Docker image config digest이며 `manifestDigest`와 다른 값일 수 있습니다. `scanReceipt`는 증거 디렉터리에 있는 파일의 basename이고, `scanReceiptSha256`은 그 파일 바이트의 소문자 SHA-256 hex입니다.

각 registry 스캔 영수증은 `name`, `target`, `imageId`, `manifestDigest`, `artifacts`만 포함합니다. `target`은 `registry:` 뒤에 해당 집계 항목의 `ref`를 붙인 값입니다. `artifacts`는 `spdx`, `syft`, `grype` 세 객체를 갖고, 각각 `file`과 `sha256`만 갖습니다. `file`은 같은 증거 디렉터리의 basename(`api-sha256-….spdx.json` 같은 형태)이며 `sha256`은 해당 파일 바이트의 소문자 64자리 hex입니다. Syft와 Grype 결과의 source image ID와 manifest digest도 이 영수증의 값과 일치해야 합니다. 로컬 archive 스캔 영수증은 같은 artifact 형식을 쓰지만 `manifestDigest`가 `null`이고 `target`은 `docker:sha256:…`입니다.

증거 디렉터리를 다른 경로로 복사하거나 이동한 뒤 `node scripts/image-release.mjs validate /새/증거/경로`를 실행합니다. 검증기는 다섯 이름과 source SHA, tag/ref 관계, 영수증 및 세 artifact의 이름·해시·image ID·manifest digest 관계를 확인합니다. 또한 SPDX 형식, Syft와 Grype의 source identity, Grype 결과 배열에 High/Critical 항목이 없는지도 다시 검사합니다. 집계 파일과 스캔 영수증의 절대 경로, 경로 탐색, 누락 또는 변경된 파일은 실패합니다. `release.json`과 스캔 영수증에는 runner의 절대 경로, 환경 변수, credential, DSN, 원문 secret을 포함하지 않습니다. Archive import 단계의 `${name}.json`은 로컬 archive와 layout 경로를 담는 작업용 입력이며 이동 검증 계약의 대상이 아닙니다. 운영 보관 시에도 이동 검증을 수행하여 릴리스 증거 전송 중 변경을 감지합니다.

## 보조 증거와 라이선스 승인 경계

`release.json` schema version 1과 기존 스캔 영수증 스키마는 변경하지 않습니다. 이동 검증에는 registry 스캔뿐 아니라 다섯 이미지의 로컬 스캔 영수증·SPDX·Syft·Grype 파일과 다음 보조 JSON 파일도 함께 보관해야 합니다. `validate`는 registry에 접근하지 않고 `publish`와 동일한 사전 검증을 적용합니다.

- `restore-tools-runtime.json` (schemaVersion 1): `passed`, 정확한 release source SHA와 최종 image ID, 고정 APK preflight의 base index/amd64 child 및 donor digest, APK DB 해시, 일곱 package/version/license 선언과 donor license/notice 해시를 확인합니다. `sbom`과 `scanReceipt`는 같은 디렉터리에 보관한 최종 image ID의 로컬 Syft/영수증 basename 및 실제 바이트 SHA-256과 일치해야 합니다.
- `restore-tools-secure.json` (schema 2): 같은 source SHA·image ID·로컬 `sbom: {file, sha256}` 및 donor index digest에 연결합니다. `phase`는 `passed`이고 defaultUser, verifiedConnection, dns, bootstrapReruns, migrationReruns, grantsRoles, rotation, wrongCa, wrongHostname, wrongPassword, cleanup 검사는 모두 명시적 `true`여야 합니다. producer는 SBOM 누락/다른 image ID를 Docker 실행 전에 거부합니다. 이전 schema 1 secure 증거는 새 검증을 통과하지 못합니다.
- `license-review.json`과 `license-attestation.json`: 아래 별도 검토 기록입니다. runtime/secure 검증과 라이선스 승인은 서로를 대체하지 못합니다. 기술 검사와 다섯 이미지의 High/Critical 0건이 모두 통과해도 라이선스가 pending이면 게시할 수 없습니다.

runtime producer는 계속 `licenseReview: {status: "license-review-pending"}`을 생성합니다. 이를 해제하려면 후속 사람 검토가 필요하며, 승인 시 runtime의 상태는 정확히 `{status: "approved", record: "license-review.json"}`이어야 합니다. 라이선스 식별자 자체나 임의의 reviewer 문자열은 승인 근거가 아닙니다.

라이선스 기록(schemaVersion 1, status `approved`)은 `sourceSha`, `imageId`, `sbom`, `runtime`, `donor`, `donorLicenses`를 runtime 증거와 정확히 연결하고 `author`를 기록합니다. `packages`는 일곱 APK의 정확한 name/version/license 전체를 담으며 각 항목에 `source`, `licenseText`, `notice`, `sourceOffer`의 `{file, sha256}` 참조를 포함합니다. `donorObligations`에도 같은 형식으로 donor의 license/notice/source-offer 의무 검토 문서를 연결합니다. 모든 참조는 라이선스 기록 commit에 존재하는 비어 있지 않은 일반 텍스트 파일의 실제 바이트 해시여야 합니다. 의무가 없다는 판단도 그 이유를 해당 사람이 검토한 문서에 남깁니다. 파서는 법적 내용의 충분성을 판단하지 않습니다.

독립 검토 attestation(schemaVersion 1, status `approved`)은 `reviewer`, 유효한 UTC `reviewedAt`, `review: {commit, file: "license-review.json", sha256}`를 담습니다. 라이선스 기록과 다른 후속 commit에 기록되어야 하며 reviewer는 author와 달라야 합니다. `image-release.mjs`의 `licenseReviewTrust`는 attestation의 정확한 `{commit, file: "license-attestation.json", sha256}`를 고정하는 별도 신뢰 기준입니다. 환경 변수나 제출된 사이드카가 이 값을 지정할 수 없습니다. 검증기는 스크립트가 속한 저장소의 Git 객체에서 기록과 문서 바이트를 읽고 해시·commit ancestry를 검사하며, 이동된 두 JSON 복사본도 커밋된 바이트와 일치해야 합니다. 따라서 이동 검증 시 신뢰 핀과 해당 Git 이력을 포함한 검증기 checkout이 필요합니다. 워킹트리에서만 수정한 검토 문서는 승인으로 인정하지 않습니다.

**현재 신뢰 핀은 `null`이며 실제 승인 기록을 만들지 않았습니다.** 따라서 실제 `publish`와 승인된 release로서의 `validate`는 계속 차단됩니다. 후속 핀 설치는 독립적인 사람 검토를 거쳐야 합니다. 코드는 사람의 신원과 독립성을 자체적으로 증명하지 못하므로, 신뢰 핀을 승인하는 사람 검토 절차가 그 신뢰 경계입니다. 테스트는 임시 저장소와 임시 검증기 복사본에서만 합성 기록/핀을 사용합니다.

검토 기록은 기술 증거 생성 후 별도 commit에 들어가므로 이미지 source SHA와 검토 commit은 구분합니다. 후속 게시 흐름은 검토된 정확한 source/image/SBOM을 재사용하고, 다른 source SHA로 다시 빌드하면 새 검토를 받아야 합니다. 기존 human security-review gate, immutable-tag 정책, 다섯 이미지의 `grype --fail-on high`는 유지합니다. 라이선스 핀 설치만으로 GHCR 게시 권한을 부여하지 않으며, 실제 publish-only 실행·registry 영수증·동일 SHA 재실행은 별도 정확한 승인이 필요합니다. 이 증거가 없으면 M4는 partial이고 H1/H2, native restore/RPO/RTO, 실제 S3와 production은 이후 별도 단계입니다.
