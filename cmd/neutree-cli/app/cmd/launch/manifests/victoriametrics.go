package manifests

var VictoriaMetricsDockerComposeManifests = `
version: "3.8"

services:
  vmstorage:
    container_name: vmstorage
    image: victoriametrics/vmstorage:{{ .VictoriaMetricsVersion}}
    ports:
      - 8482:8482 # metrics
      - 8400:8400 # insert access port
      - 8401:8401 # select access port
    volumes:
      - storagedata:/storage
    command:
      - "--storageDataPath=/storage"
    restart: always

  # vminsert is ingestion frontend. It receives metrics pushed by vmagent,
  # pre-process them and distributes across configured vmstorage shards.
  vminsert:
    container_name: vminsert
    image: victoriametrics/vminsert:{{ .VictoriaMetricsVersion}}
    depends_on:
      - "vmstorage"
    command:
	  {{range $ip := .DeployIps}}
      - "--storageNode={{ $ip}}:8400"
      {{end}}
    ports:
      - 8480:8480
    restart: always
  vmselect:
    container_name: vmselect
    image: victoriametrics/vmselect:{{ .VictoriaMetricsVersion}}
    depends_on:
      - "vmstorage"
    command:
	  {{range $ip := .DeployIps}}
      - "--storageNode={{ $ip}}:8401"
      {{end}}
    ports:
      - 8481:8481
    restart: always

volumes:
  storagedata: {}
`
