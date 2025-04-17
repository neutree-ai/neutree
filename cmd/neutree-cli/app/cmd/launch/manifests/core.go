package manifests

import (
	"embed"
)

//go:embed db.tar
var NeutreeCoreDBInitScripts embed.FS

var NeutreePrometheusScrapeConfig = `
global:
  scrape_interval: 30s # Set the scrape interval to every 30 seconds. Default is every 1 minute.

scrape_configs:
# Scrape from each Ray node as defined in the service_discovery.json provided by Ray.
- job_name: 'neutree'
  file_sd_configs:
  - files:
    - '/etc/prometheus/scrape/*.json'
`

//nolint:lll
var NeutreeCoreDockerComposeManifests = `
version: "3.8"

services:
  postgres:
    image: postgres:13
    container_name: postgres
    environment:
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: pgpassword
      POSTGRES_DB: aippp
    volumes:
      - pg_data:/var/lib/postgresql/data
      - {{ .WorkDir}}/db/init-scripts:/docker-entrypoint-initdb.d
    ports:
      - "5432:5432"
    networks:
      - neutree
    restart: always
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U postgres"]
      interval: 2s
      timeout: 15s
      retries: 10

  auth:
    image: supabase/gotrue:v2.170.0
    container_name: auth
    ports:
      - "9999:9999"
    environment:
      GOTRUE_MAILER_URLPATHS_CONFIRMATION: "/verify"
      GOTRUE_JWT_SECRET: {{ .JwtSecret}}
      GOTRUE_JWT_EXP: 3600
      GOTRUE_JWT_DEFAULT_GROUP_NAME: api_user
      GOTRUE_DB_DRIVER: postgres
      DB_NAMESPACE: auth
      GOTRUE_API_HOST: 0.0.0.0
      PORT: 9999
      GOTRUE_DISABLE_SIGNUP: "false"
      API_EXTERNAL_URL: http://localhost:9999
      GOTRUE_SITE_URL: http://localhost:9999
      GOTRUE_MAILER_AUTOCONFIRM: "true"
      GOTRUE_LOG_LEVEL: INFO
      DATABASE_URL: "postgres://auth_admin:auth_admin_password@postgres:5432/aippp"
      GOTRUE_COOKIE_KEY: "aippp"
    networks:
      - neutree
    depends_on:
      postgres:
        condition: service_healthy
    restart: on-failure
    healthcheck:
      test:
        [
          "CMD-SHELL",
          "wget -q -O /dev/null http://localhost:9999/health || exit 1",
        ]
      interval: 2s
      timeout: 15s
      retries: 10

  migration:
    image: migrate/migrate
    container_name: migration
    depends_on:
      auth:
        condition: service_healthy
    command: -source=file://migrations -database "postgres://postgres:pgpassword@postgres:5432/aippp?sslmode=disable" up
    volumes:
      - {{ .WorkDir}}/db/migrations:/migrations
    networks:
      - neutree
    restart: none
    deploy:
      restart_policy:
        condition: none

  post-migration-hook:
    image: postgres:13
    container_name: post-migration-hook
    networks:
      - neutree
    depends_on:
      migration:
        condition: service_completed_successfully
    volumes:
      - {{ .WorkDir}}/db/seed:/seed
    command: 'bash -c ''for file in $$(find /seed -name "*.sql" | sort); do echo "Executing seed file:" $$file; psql postgres://postgres:pgpassword@postgres:5432/aippp?sslmode=disable -f $$file; done'''
    restart: none
    deploy:
      restart_policy:
        condition: none

  postgrest:
    image: postgrest/postgrest:latest
    container_name: postgrest
    environment:
      PGRST_DB_URI: postgres://postgres:pgpassword@postgres:5432/aippp
      PGRST_DB_SCHEMA: api
      PGRST_SERVER_HOST: 0.0.0.0
      PGRST_SERVER_PORT: 6432
      PGRST_JWT_SECRET: {{ .JwtSecret}}
      PGRST_DB_EXTRA_SEARCH_PATH: auth
      PGRST_DB_AGGREGATES_ENABLED: 1
    ports:
      - "6432:6432"
    depends_on:
      migration:
        condition: service_completed_successfully
    networks:
      - neutree
    restart: always

  postgres-meta:
    image: supabase/postgres-meta:v0.86.0
    container_name: postgres-meta
    environment:
      PG_META_HOST: "0.0.0.0"
      PG_META_PORT: 8080
      PG_META_DB_HOST: "postgres"
      PG_META_DB_NAME: "aippp"
      PG_META_DB_USER: "postgres"
      PG_META_DB_PORT: 5432
      PG_META_DB_PASSWORD: "pgpassword"
    ports:
      - "8080:8080"
    networks:
      - neutree
    depends_on:
      migration:
        condition: service_completed_successfully

  neutree-core:
    image: neutree-ai/neutree-core:{{ .NeutreeCoreVersion}}
    container_name: neutree-core
    command:
      - ./neutree-core
      - --storage-access-url=http://postgrest:6432
      - --storage-jwt-secret={{ .JwtSecret}}
      - --controller-workers=5
      - --default-cluster-version=v1
    volumes:
      - {{.WorkDir}}/collect:/etc/neutree/collect
    networks:
      - neutree
    restart: always
    depends_on:
      - postgrest

  vmagent:
    container_name: vmagent
    image: victoriametrics/vmagent:{{ .VictoriaMetricsVersion}}
    depends_on:
      - "neutree-core"
    ports:
      - 8429:8429
    volumes:
      - vmagentdata:/vmagentdata
      - {{ .WorkDir}}/metrics/prometheus-cluster.yml:/etc/prometheus/prometheus.yml
      - {{ .WorkDir}}/collect/metrics:/etc/prometheus/scrape
    command:
      - "--promscrape.config=/etc/prometheus/prometheus.yml"
      - "--promscrape.configCheckInterval=10s"
      - "--remoteWrite.url={{ .MetricsRemoteWriteURL}}"
    restart: always

networks:
  neutree:
    driver: bridge

volumes:
  pg_data:
  vmagentdata:
`
