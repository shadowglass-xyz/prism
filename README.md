# shadowglass.xyz - prism

A lightweight orchestrator for cheaply running containers that are resiliant
even to instances being destroyed and restarted. This means that the
containers must be able to run on spot instances and come back up as soon as
a new instance is assigned

## Development Requirements

Development tools required are as follows:

- [vagrant](https://www.vagrantup.com/)
- [just](https://github.com/casey/just)

1. Bring up development VMs and control process with `just dev`.

This will start a modd process which monitors all go files.
It will rebuild and run the control process on your local computer.
It will rebuild the agent as linux-amd64, push the image into vagrant,
move it into location, and restarts the systemd unit for the agent,
finally it will start monitoring the output of the agent.
