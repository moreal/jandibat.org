# 로컬 CockroachDB 실행 가이드 (Docker Compose)

v26.2 개발 데이터는 `cockroach-data-v26` 볼륨에 저장합니다. 이전 v23.1 볼륨은 직접 열 수 없으므로 자동 삭제하거나 재사용하지 않습니다. 필요한 기존 데이터는 지원되는 중간 버전을 거치는 CockroachDB 공식 upgrade 절차로 별도 이관합니다.

## 1) DB 실행

```bash
make db-up
```

- SQL 포트: `localhost:26257`
- DB 콘솔(Admin UI): `http://localhost:18080`
- 기본 DB: `jandibat`

## 2) 스키마 적용

```bash
make db-migrate
```

## 3) SQL 쉘 접속

```bash
make db-shell
```

쉘 접속 후:

```sql
USE jandibat;
```

## 4) 종료

```bash
make db-down
```
