dev:
    modd

control:
    go run shadowglass/cmd/control

agent:
    GOOS=linux GOARCH=arm64 go build -o build/agent shadowglass/cmd/agent
    docker compose up

install-tools:
    go install github.com/cortesi/modd/cmd/modd@latest

# vim: ts=4 sts=4 sw=4 et
