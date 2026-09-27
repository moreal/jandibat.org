# Restore-tools 런타임 후보 2 결정 — CockroachDB 26.2.7 사전 검사

## 상태와 근거

이 문서는 후보 선택 결정이지 이미지 채택·배포 승인이나 검증 완료 기록이 아니다. 승인된 [최소 런타임 설계](2026-09-26-restore-tools-minimal-runtime-design.md)의 fallback 순서를 따른다.

읽기 전용 [Linux probe run 36311686493](https://github.com/moreal/jandibat.org/actions/runs/36311686493)(소스 `265aa7cfff13b3c1b541581879ea324e7a6e315e`, [sanitized artifact 10928548330](https://github.com/moreal/jandibat.org/actions/runs/36311686493/artifacts/10928548330))에서 첫 후보 `registry.access.redhat.com/ubi10/ubi-micro`는 `trust-inventory`로 거부됐다. 증거의 index digest는 `sha256:37fadb004c6bea628fcdd81376c8fb77bd8d9fd432d90503af4d9e76b1ff7191`, amd64 child는 `sha256:10c387629fa69cf794e3f975dc78e5e177ffc980364c1e999273f112268f62c9`이다. 이 정확한 amd64 이미지를 로컬에서 파일 목록만 읽어 확인했을 때 RPM DB는 있었으나 설계가 요구하는 일반 CA 신뢰 번들은 없었다. 이것은 현재 이미지 digest에 대한 결과이며 다른 태그/버전의 속성으로 일반화하지 않는다. 상세 진행 기록은 ignored `.superpowers/sdd/2026-09-26-restore-tools-minimal-runtime/progress.md`에 있다.

기존 CockroachDB 26.2.5 전체 이미지는 run `36215677122`에서 High RPM 일치 44건으로 거부됐다. 이 최초 결정 시점에는 후보 2의 스캔 결과를 아직 몰랐다. 이후의 실제 결과는 아래에 기록한다.

## 다음 후보와 경계

다음은 공식 `cockroachdb/cockroach:v26.2.7` 기본 UBI 이미지다. 2026-09-27 UTC에 로컬 `docker buildx imagetools inspect --raw` 공개 레지스트리 조회에서 index digest `sha256:9464ae30465b887295459b98d129a76d074caeaa4c86836d369c6d2fdddd1685`와 linux/amd64 child `sha256:a0a144676e1648929f2838c3574d2ac02f1d29186f66207fc0149c7f3f9bcb6d`를 관측했다. 이는 CI의 Nix-pinned Skopeo/Syft/Grype 검증을 대체하지 않는다. 공식 릴리스 문서는 26.2.7 기본 이미지와 별도의 실험적 `-noble` 변형을 구분한다. 실험적 변형은 선택하지 않는다.

사전 검사는 고정 index에서 amd64 child, 실제 RPM DB·패키지 소유권, glibc/ELF 의존성, CA 신뢰, 라이선스/고지, 변경하지 않은 `grype --fail-on high`를 검증한다. 26.2.7 CLI를 도너/런타임으로 채택한다면 기존 26.2.5 secure DB 서버와의 TLS·SQL·migration/restore 호환성을 격리 환경에서 별도로 입증해야 한다. 그전에는 Dockerfile, DB 서버 버전, 운영 권한, 배포를 바꾸지 않는다. 실패하면 원인과 package/CVE/fix-state를 기록하고 설계의 세 번째 후보인 지원되는 최소 glibc 런타임을 동일 기준으로 평가한다.

참고: [CockroachDB v26.2 릴리스](https://docs.cockroachlabs.com/docs/releases/v26.2), [Red Hat UBI 이미지 종류](https://docs.redhat.com/en/documentation/red_hat_enterprise_linux/10/html/building_running_and_managing_containers/types-of-container-images).

## 후보 2 결과와 후보 3 결정

읽기 전용 [Linux probe run 36316968562](https://github.com/moreal/jandibat.org/actions/runs/36316968562)(소스 `f792534e53967cb4ea882a363ef8a403a15db117`, sanitized artifact `10930733165`)에서 후보 2의 고정 index와 amd64 child가 위의 값과 일치했다. `umoci` 추출물의 임시 디렉터리 정리까지 통과하여 완전한 dossier가 생성됐지만, Grype 데이터베이스 시각 `2026-09-27T06:30:30Z` 기준 High/Critical 일치 29건 때문에 `status=rejected`였다. 일치는 9개 고유 취약점 ID에 걸쳐 `libblkid`, `libmount`, `libsmartcols`, `libuuid` 각 6건, `pcre2`, `pcre2-syntax` 각 2건, `rpm-sequoia` 1건이었고, 29건 모두 `fixState=not-fixed`였다. 이 숫자는 해당 digest와 스캐너 데이터베이스의 관측값이며, 다른 이미지나 시점의 결과가 아니다. 스캔 문턱을 낮추거나 이 패키지를 숨기지 않는다.

설계의 다음 순서에 따라 후보 3은 Red Hat의 지원되는 `registry.access.redhat.com/ubi10/ubi-minimal` glibc 이미지로 정한다. 공개 OCI index를 2026-09-27 UTC에 읽기 전용으로 조회해 `sha256:e3a5632d7ae8a97e06f634522d06187f12793e90ac0d7b51bc671c83a96d8eda`, linux/amd64 child `sha256:d22eab33a72231f57ca04dbb296eb0d1d248bd37b8d5edb6bb18f4bbb80517ea`를 관측했다. 바이너리 donor는 처음 승인된 `cockroachdb/cockroach:v26.2.5@sha256:771325a0586bf61d53322d24f5a6de8962568b0fc181fa45db364278e5961282`로 되돌려 기존 secure DB 서버와 버전을 맞춘다. Red Hat의 [공식 이미지 카탈로그](https://catalog.redhat.com/en/software/containers/ubi10-minimal/66f16af45db83414cddcfc99)와 [RHEL 10 설명서](https://docs.redhat.com/en/documentation/red_hat_enterprise_linux/10/html/building_running_and_managing_containers/adding-software-to-a-ubi-container)는 이 이미지가 유지되는 UBI minimal이며 `microdnf`를 포함한다고 설명한다. 이는 신뢰 번들, RPM 소유권, glibc ABI, 라이선스, High/Critical 0건을 입증하지 않는다. 특히 패키지 관리 구성 요소가 후보 2의 취약 패키지를 유지할 가능성도 있으므로 동일한 불변 index·amd64·trust·ELF·RPM·license·`grype --fail-on high` 읽기 전용 Linux probe로 먼저 거부/통과를 결정한다. 통과 전에는 Dockerfile, GHCR, 운영 DB, 배포를 바꾸지 않는다.
