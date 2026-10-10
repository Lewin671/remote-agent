# Image for remote-agent-server: the binary next to this file and nothing else.
FROM scratch
COPY remote-agent-server /remote-agent-server
USER 65534:65534
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/remote-agent-server", "--listen", "0.0.0.0:8080", "--db", "/data/remote-agent.db"]
