# Restore-tools 런타임 후보 2 결정 — CockroachDB 26.2.7 사전 검사

## 상태와 근거

이 문서는 후보 선택 결정이지 이미지 채택·배포 승인이나 검증 완료 기록이 아니다. 승인된 [최소 런타임 설계](2026-09-26-restore-tools-minimal-runtime-design.md)의 fallback 순서를 따른다.

읽기 전용 [Linux probe run 36311686493](https://github.com/moreal/jandibat.org/actions/runs/36311686493)(소스 `265aa7cfff13b3c1b541581879ea324e7a6e315e`, [sanitized artifact 10928548330](https://github.com/moreal/jandibat.org/actions/runs/36311686493/artifacts/10928548330))에서 첫 후보 `registry.access.redhat.com/ubi10/ubi-micro`는 `trust-inventory`로 거부됐다. 증거의 index digest는 `sha256:37fadb004c6bea628fcdd81376c8fb77bd8d9fd432d90503af4d9e76b1ff7191`, amd64 child는 `sha256:10c387629fa69cf794e3f975dc78e5e177ffc980364c1e999273f112268f62c9`이다. 이 정확한 amd64 이미지를 로컬에서 파일 목록만 읽어 확인했을 때 RPM DB는 있었으나 설계가 요구하는 일반 CA 신뢰 번들은 없었다. 이것은 현재 이미지 digest에 대한 결과이며 다른 태그/버전의 속성으로 일반화하지 않는다. 상세 진행 기록은 ignored `.superpowers/sdd/2026-09-26-restore-tools-minimal-runtime/progress.md`에 있다.

기존 CockroachDB 26.2.5 전체 이미지는 run `36215677122`에서 High RPM 일치 44건으로 거부됐다. 새 후보의 스캔 결과는 아직 모른다.

## 다음 후보와 경계

다음은 공식 `cockroachdb/cockroach:v26.2.7` 기본 UBI 이미지다. 2026-09-27 UTC에 로컬 `docker buildx imagetools inspect --raw` 공개 레지스트리 조회에서 index digest `sha256:9464ae30465b887295459b98d129a76d074caeaa4c86836d369c6d2fdddd1685`와 linux/amd64 child `sha256:a0a144676e1648929f2838c3574d2ac02f1d29186f66207fc0149c7f3f9bcb6d`를 관측했다. 이는 CI의 Nix-pinned Skopeo/Syft/Grype 검증을 대체하지 않는다. 공식 릴리스 문서는 26.2.7 기본 이미지와 별도의 실험적 `-noble` 변형을 구분한다. 실험적 변형은 선택하지 않는다.

사전 검사는 고정 index에서 amd64 child, 실제 RPM DB·패키지 소유권, glibc/ELF 의존성, CA 신뢰, 라이선스/고지, 변경하지 않은 `grype --fail-on high`를 검증한다. 26.2.7 CLI를 도너/런타임으로 채택한다면 기존 26.2.5 secure DB 서버와의 TLS·SQL·migration/restore 호환성을 격리 환경에서 별도로 입증해야 한다. 그전에는 Dockerfile, DB 서버 버전, 운영 권한, 배포를 바꾸지 않는다. 실패하면 원인과 package/CVE/fix-state를 기록하고 설계의 세 번째 후보인 지원되는 최소 glibc 런타임을 동일 기준으로 평가한다.

참고: [CockroachDB v26.2 릴리스](https://docs.cockroachlabs.com/docs/releases/v26.2), [Red Hat UBI 이미지 종류](https://docs.redhat.com/en/documentation/red_hat_enterprise_linux/10/html/building_running_and_managing_containers/types-of-container-images).
