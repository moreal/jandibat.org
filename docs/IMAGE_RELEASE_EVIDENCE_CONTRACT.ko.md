# 이미지 릴리스 증거 계약

`scripts/image-release.mjs publish EVIDENCE`는 `api`, `worker`, `maintenance`, `web`, `restore-tools`의 로컬 archive 검사와 registry 스캔이 모두 성공한 뒤 릴리스 증거를 기록합니다. 다섯 SHA tag를 게시 전 확인하고, 각 이미지의 digest 참조와 tag를 게시 후 다시 조회하여 둘 다 후보 manifest digest와 같은지 확인합니다. 어느 단계든 실패하면 `GITHUB_OUTPUT`에 배포 참조를 쓰지 않습니다. 이미 게시한 image를 자동으로 삭제하지 않으므로 중간 실패 시 registry에는 일부 tag가 남을 수 있습니다. 같은 소스 SHA로 재실행할 때 기존 tag가 같은 digest일 때만 계속 진행합니다.

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
