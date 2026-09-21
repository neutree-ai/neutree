# Plugin is verified and installed at build time, including for offline deployments.
ARG GRAFANA_BASE_IMAGE=grafana/grafana:11.5.3
FROM ${GRAFANA_BASE_IMAGE}
USER root
RUN mkdir -p /opt/neutree/grafana-plugins \
    && wget -q https://grafana.com/api/plugins/volkovlabs-echarts-panel/versions/6.6.0/download -O /tmp/business-charts.zip \
    && echo '4a3665df7c681d5984eb2d03ec54fdc76c153e297b1cad6b6158135c80a95d9e  /tmp/business-charts.zip' | sha256sum -c - \
    && unzip -q /tmp/business-charts.zip -d /opt/neutree/grafana-plugins \
    && rm /tmp/business-charts.zip \
    && chmod -R a+rX /opt/neutree/grafana-plugins
# Keep bundled plugins outside the existing /var/lib/grafana persistent volume.
ENV GF_PATHS_PLUGINS=/opt/neutree/grafana-plugins \
    GF_PLUGINS_PREINSTALL_DISABLED=true
USER 472
