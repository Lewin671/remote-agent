# Image for remote-agent-server. Build the binary first:
#   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o deploy/remote-agent-server ./cmd/remote-agent-server
FROM scratch
COPY remote-agent-server /remote-agent-server
USER 65534:65534
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/remote-agent-server", "--listen", "0.0.0.0:8080", "--db", "/data/remote-agent.db"]
