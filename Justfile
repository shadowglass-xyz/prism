dev:
    modd

agent:
    GOOS=linux GOARCH=arm64 go build -o build/agent shadowglass/cmd/agent

    vagrant upload shadowglass-agent.conf /tmp/shadowglass-agent.conf debian
    vagrant ssh debian -- -t "sudo mv /tmp/shadowglass-agent.conf /etc/systemd/system/shadowglass-agent.service && sudo chown root:root /etc/systemd/system/shadowglass-agent.service && sudo systemctl daemon-reload"

    vagrant upload ./build/agent /tmp/agent debian
    vagrant ssh debian -- -t "sudo mv /tmp/agent /usr/local/bin/shadowglass-agent && sudo systemctl restart shadowglass-agent"
    vagrant ssh debian -- -t "sudo journalctl -fu shadowglass-agent"

up:
    vagrant up

down:
    vagrant suspend debian

install-tools:
    go install github.com/cortesi/modd/cmd/modd@latest

# vim: ts=4 sts=4 sw=4 et
