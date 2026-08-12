# Monitoring provisioning

이 디렉터리는 운영 Prometheus/Grafana에 반영할 versioned 기준 파일입니다.

- `prometheus-rules.yaml`: route/environment/region별 5xx error-ratio recording rule, multi-window burn alert, queue-age와 provider-token revoke DLQ alert.
- `grafana-dashboard.json`: HTTP latency/error, queue/freshness, ingest/audit, DB pool과 revoke DLQ 운영 dashboard.

로컬 구문 gate는 다음과 같습니다.

```sh
make monitoring-check
```

배포자는 같은 release SHA의 rule 파일을 Prometheus rule provisioner에, dashboard JSON을 Prometheus datasource가 연결된 Grafana에 반영합니다. API/worker/maintenance의 `/metrics`는 공개 ingress에 노출하지 않고 monitoring network에서만 scrape합니다. 각 target은 `build_sha`, `environment`, `region` label을 가져야 하며, staging에서 다음을 증거로 남깁니다.

1. Prometheus rule reload/evaluation 성공과 active alert rule 목록.
2. API, worker, maintenance target의 scrape 성공 및 세 resource label 값.
3. 각 dashboard panel이 조회되는 screenshot과 datasource UID.
4. synthetic 5xx/queue/DLQ fixture로 해당 alert가 firing 후 resolved 되는 시각.

`make monitoring-check`는 YAML/JSON 파싱만 수행하므로 실제 PromQL 평가나 datasource 연결을 증명하지 않습니다. Runtime은 `pgxpool` acquire latency를 `db_pool_wait_duration_seconds` histogram으로 계측하고 dashboard는 이 bucket의 5분 rate로 p99를 계산합니다. 누적 `db_pool_wait_seconds`는 총 대기량 관측용이며 p99로 해석하지 않습니다.
